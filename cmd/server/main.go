package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	//"gatstogo/internal/config"
	//"gatstogo/internal/handlers/attendant"
	"gatstogo/internal/csrf"
	"gatstogo/internal/domain"
	"gatstogo/internal/middleware"
	"gatstogo/internal/payments"
	"gatstogo/internal/receipts"
	"gatstogo/internal/session"
	"gatstogo/internal/shifts"
	"gatstogo/internal/sms"
	"gatstogo/internal/tenantdb"
	"gatstogo/web/templates/components"
	"gatstogo/web/templates/pages"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

type Server struct {
	Router   *chi.Mux
	AppDB    *pgxpool.Pool // used for normal plant traffic (RLS enforced)
	AdminDB  *pgxpool.Pool // used for platform admin (bypasses RLS)
	Redis      *redis.Client
	Sessions   *session.Store
	Paystack   *payments.Client
	PINLimiter *shifts.PINLimiter
	Receipts   *receipts.CodeStore
	SMS        sms.Sender
	// NotificationEmail is GatsToGo's own real inbox, used as the base for
	// the per-ticket placeholder email Paystack's Initialize API requires
	// (see ticketEmail in tickets.go). Configurable via
	// TICKET_NOTIFICATION_EMAIL; defaults to the address actually in use.
	NotificationEmail string
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}
	s, err := CreateNewServer()
	if err != nil {
		log.Fatal("Failed to create server:", err)
	}
	defer s.Close()
	s.MountHandlers()
	srv := &http.Server{
		Addr:    ":" + getEnv("APP_PORT", "8080"),
		Handler: s.Router,
	}

	go func() {
		log.Println("Server starting on :8080")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(http.ErrServerClosed, err) {
			log.Fatal(err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Println("Server forced to shutdown:", err)
	}

	log.Println("Server exited")
}

func CreateNewServer() (*Server, error) {
	ctx := context.Background()
	// 1. App database (normal traffic - RLS enforced)
	appDB, err := pgxpool.New(ctx, os.Getenv("APP_DATABASE_URL"))
	if err != nil {
		return nil, err
	}

	if err := appDB.Ping(ctx); err != nil {
		return nil, err
	}

	// 2. Admin database (platform admin - bypasses RLS)
	adminDB, err := pgxpool.New(ctx, os.Getenv("ADMIN_DATABASE_URL"))
	if err != nil {
		appDB.Close()
		return nil, err
	}

	if err := adminDB.Ping(ctx); err != nil {
		appDB.Close()
		adminDB.Close()
		return nil, err
	}

	// 3. Redis (login sessions)
	redisURL := getEnv("REDIS_URL", "redis://localhost:6379/0")
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		appDB.Close()
		adminDB.Close()
		return nil, fmt.Errorf("invalid REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(redisOpts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		appDB.Close()
		adminDB.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	// 4. CSRF secret. Required, not defaulted: an empty/predictable HMAC
	// key would make every authenticated CSRF token forgeable.
	secret, err := loadSessionHMACSecret()
	if err != nil {
		appDB.Close()
		adminDB.Close()
		_ = rdb.Close()
		return nil, err
	}
	csrf.Secret = secret

	// 5. Paystack. Required: a server that can't take payment isn't
	// meaningfully "up" for this app's purpose.
	paystackSecretKey := os.Getenv("PAYSTACK_SECRET_KEY")
	if paystackSecretKey == "" {
		appDB.Close()
		adminDB.Close()
		_ = rdb.Close()
		return nil, fmt.Errorf("PAYSTACK_SECRET_KEY is required")
	}

	s := &Server{
		Router:            chi.NewRouter(),
		AppDB:             appDB,
		AdminDB:           adminDB,
		Redis:             rdb,
		Sessions:          session.New(rdb),
		Paystack:          payments.NewClient(paystackSecretKey),
		PINLimiter:        shifts.NewPINLimiter(rdb),
		Receipts:          receipts.NewCodeStore(rdb),
		SMS:               sms.NewLoggingSender(),
		NotificationEmail: getEnv("TICKET_NOTIFICATION_EMAIL", "gatstogofficial@gmail.com"),
	}
	return s, nil
}

func (s *Server) MountHandlers() {
	s.Router.Use(chimw.RequestID)
	s.Router.Use(chimw.Logger)
	s.Router.Use(chimw.Recoverer)
	s.Router.Use(chimw.Timeout(60 * time.Second))

	//_deps := &config.Dependencies{
	//	AppDB:   s.AppDB,
	//	AdminDB: s.AdminDB,
	//}

	//s.Router.Get("/", attendant.Test)
	s.Router.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	// Server-to-server; no session, no cookies, and deliberately no CSRF
	// middleware -- authenticity here comes entirely from the
	// X-Paystack-Signature HMAC check inside the handler itself.
	s.Router.Post("/webhooks/paystack", paystackWebhookHandler(s))

	s.Router.Get("/admin/login", adminLoginPageHandler(s))
	s.Router.With(middleware.CSRF).Post("/admin/login", adminLoginSubmitHandler(s))

	// GatsToGo's own public marketing site (cmd/server/marketing.go) --
	// registered here rather than inside the tenant group below because
	// it has to serve a bare or "www" host, which has no tenant to
	// resolve at all. "/" and "/home" branch by host at request time
	// (rootOrTenant); every other marketing path only ever makes sense on
	// that host anyway, so they're unconditional.
	s.Router.Get("/", rootOrTenant(s, customerHomeHandler(s)))
	s.Router.Get("/home", rootOrTenant(s, customerHomeHandler(s)))
	s.Router.Get("/pricing", pricingPageHandler(s))
	s.Router.Get("/about", aboutPageHandler(s))
	s.Router.Get("/terms", termsPageHandler(s))
	s.Router.Get("/privacy", privacyPageHandler(s))
	s.Router.Get("/get-started", signupPageHandler(s))
	s.Router.With(middleware.CSRF).Post("/get-started", signupSubmitHandler(s))

	// Same branded page as an unresolved tenant subdomain (renderNotFound,
	// cmd/server/marketing.go) for any route chi's own router can't match
	// at all -- previously net/http's plain-text default.
	s.Router.NotFound(func(w http.ResponseWriter, r *http.Request) { renderNotFound(w, r) })

	s.Router.Group(func(r chi.Router) {
		r.Use(middleware.RequireAdminSession(s.Sessions))
		// Mounted *after* RequireAdminSession so GetActor(ctx) is already
		// populated when a POST reaches it -- CSRF then uses the
		// authenticated, session-derived check rather than the public
		// double-submit one. GET requests (the console itself) pass
		// through CSRF unconditionally either way.
		r.Use(middleware.CSRF)

		r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			data := loadAdminConsole(r.Context(), s.AdminDB)
			actor := middleware.GetActor(r.Context())
			data.CSRFToken, _ = csrf.Token(actor.Token)
			data.ErrorMessage = adminErrorMessage(r.URL.Query().Get("error"))
			if err := pages.AdminConsole(data).Render(r.Context(), w); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		})

		r.Post("/admin/logout", adminLogoutHandler(s))
		r.Post("/admin/plants", adminCreatePlantHandler(s))
		r.Post("/admin/plants/{id}/status", adminPlantStatusHandler(s))
	})

	s.Router.Group(func(r chi.Router) {
		// The tenant lookup below (slug -> plant) is intentionally run on
		// AdminDB (the RLS-bypassing role). It's a pre-tenant, directory-style
		// lookup -- it's how a plant_id is discovered in the first place, so
		// it can't itself be scoped by the plants_isolation policy (id =
		// current_plant_id()), which needs a plant_id to already be known.
		// Every tenant-scoped query that runs *after* this middleware goes
		// through tenantdb.WithTenant(ctx, s.AppDB, plant.ID, ...) instead,
		// using the restricted pool with the plant_id actually set.
		r.Use(middleware.Tenant(s.AdminDB))

		// "/" and "/home" are registered at the top level instead (see
		// rootOrTenant above) -- they need to branch by host before a
		// tenant lookup even happens, which can't be expressed inside a
		// route group that's already unconditionally running
		// middleware.Tenant.
		r.With(middleware.CSRF).Post("/tickets", buyGasSubmitHandler(s))
		r.Get("/tickets/{reference}/callback", ticketCallbackHandler(s))

		// Customer receipts lookup. GET /receipts is public (it's both the
		// phone-entry form and, once a receipts session cookie exists, the
		// ticket list -- see receiptsPageHandler); /lookup and /verify are
		// public too (no session exists yet at that point), so CSRF on them
		// uses the double-submit cookie check. /tickets/{reference}/confirm
		// requires an actual verified session, mirroring the staff
		// terminal's public-PIN-then-protected-group shape above.
		r.Get("/receipts", receiptsPageHandler(s))
		r.With(middleware.CSRF).Post("/receipts/lookup", receiptsLookupHandler(s))
		r.With(middleware.CSRF).Post("/receipts/verify", receiptsVerifyHandler(s))
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireReceiptSession(s.Sessions))
			r.Use(middleware.CSRF)

			r.Post("/tickets/{reference}/confirm", receiptsConfirmHandler(s))
		})

		r.Get("/terminal", terminalPageHandler(s))

		// Staff terminal backend. JSON, not HTML -- see cmd/server/terminal.go.
		// /terminal/pin is public (no staff session exists yet, so CSRF here
		// uses the double-submit cookie check); everything after it requires
		// RequireStaffSession, which is also where CSRF switches to the
		// authenticated per-session check, same ordering rationale as the
		// owner/admin groups above.
		r.With(middleware.CSRF).Post("/terminal/pin", terminalPinHandler(s))
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireStaffSession(s.Sessions))
			r.Use(middleware.CSRF)

			r.Post("/terminal/redeem", terminalRedeemHandler(s))
			r.Post("/terminal/fill", terminalFillHandler(s))
			r.Post("/terminal/cash-sale", terminalCashSaleHandler(s))
			r.Post("/terminal/token-return", terminalTokenReturnHandler(s))
			r.Post("/terminal/shift/end", terminalShiftEndHandler(s))
		})

		r.Get("/owner/login", ownerLoginPageHandler(s))
		r.With(middleware.CSRF).Post("/owner/login", ownerLoginSubmitHandler(s))

		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireOwnerSession(s.Sessions))
			// Same ordering rationale as the admin group above: mounted
			// after RequireOwnerSession so POSTs in this group are checked
			// against the authenticated session's CSRF token.
			r.Use(middleware.CSRF)

			r.Get("/owner", ownerDashboardHandler(s))
			r.Get("/owner/dashboard", ownerDashboardHandler(s))
			r.Post("/owner/logout", ownerLogoutHandler(s))
			r.Post("/tickets/{reference}/void", ticketVoidHandler(s))
			r.Post("/owner/prices", ownerSetPriceHandler(s))
			r.Post("/owner/staff", ownerCreateStaffHandler(s))
			r.Post("/owner/staff/{id}/status", ownerStaffStatusHandler(s))
			r.Post("/owner/cash-movements", ownerCashMovementHandler(s))
		})

		r.Get("/home/summary", func(w http.ResponseWriter, r *http.Request) {
			plant := middleware.GetPlant(r.Context())
			if plant == nil {
				renderNotFound(w, r)
				return
			}
			if err := components.PlantSummary(plant).Render(r.Context(), w); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		})

		//r.Route("/attendant", func(r chi.Router) {
		//	r.Post("/code", attendant.EnterCode(deps))
		//})
	})
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// loadSessionHMACSecret reads SESSION_HMAC_SECRET (a base64-encoded value,
// e.g. from `openssl rand -base64 32` -- see .env.example) and decodes it
// for use as the CSRF HMAC key. Requires at least 16 bytes decoded, so a
// short/placeholder value fails startup instead of quietly weakening every
// authenticated CSRF token.
func loadSessionHMACSecret() ([]byte, error) {
	raw := os.Getenv("SESSION_HMAC_SECRET")
	if raw == "" {
		return nil, fmt.Errorf("SESSION_HMAC_SECRET is required (generate one with: openssl rand -base64 32)")
	}
	secret, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("SESSION_HMAC_SECRET must be base64-encoded: %w", err)
	}
	if len(secret) < 16 {
		return nil, fmt.Errorf("SESSION_HMAC_SECRET must decode to at least 16 bytes, got %d", len(secret))
	}
	return secret, nil
}

func (s *Server) Close() {
	if s.AppDB != nil {
		s.AppDB.Close()
	}
	if s.AdminDB != nil {
		s.AdminDB.Close()
	}
	if s.Redis != nil {
		_ = s.Redis.Close()
	}
}
func writeBytes(w http.ResponseWriter, data []byte) {
	_, err := w.Write(data)
	if err != nil {
		return
	}
}

// ownerDashboardHandler serves both /owner and /owner/dashboard. All reads
// run inside tenantdb.WithTenant against the restricted AppDB pool, scoped
// to the resolved plant, instead of the RLS-bypassing AdminDB pool the two
// routes used before -- normal tenant traffic has no reason to hold a
// bypass-RLS connection.
func ownerDashboardHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}

		var data pages.OwnerDashboardData
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			data = loadOwnerDashboard(ctx, q, plant)
			return nil
		})
		if err != nil {
			log.Println("owner dashboard: tenant-scoped query failed:", err)
			http.Error(w, "Could not load dashboard", http.StatusInternalServerError)
			return
		}

		if actor := middleware.GetActor(r.Context()); actor != nil {
			data.CSRFToken, _ = csrf.Token(actor.Token)
		}
		data.ErrorMessage = ownerErrorMessage(r.URL.Query().Get("error"))

		if err := pages.OwnerDashboard(data).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// customerHomeHandler serves both / and /home. Loads the plant's live
// price (the same query loadOwnerDashboard's "Current price" tile uses)
// instead of the hardcoded ₦1,150/kg the buy form previously showed
// regardless of what was actually in the prices table, and sets the
// public double-submit CSRF cookie the form's hidden field needs.
func customerHomeHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}

		var pricePerKg int64
		_ = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			_, pricePerKg, _ = loadCurrentPrice(ctx, q, plant.ID)
			return nil
		})

		csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
		errorMsg := buyGasErrorMessage(r.URL.Query().Get("error"))

		if err := pages.CustomerHome(plant, pricePerKg, csrfToken, errorMsg).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// buyGasErrorMessage maps the short ?error= code buyGasSubmitHandler
// redirects back with to a human-readable message. A code with no known
// mapping (or none at all) renders no banner.
func buyGasErrorMessage(code string) string {
	switch code {
	case "amount":
		return "Enter a valid amount greater than zero."
	case "details":
		return "Enter your name and phone number."
	case "channel":
		return "Choose how you will pay."
	case "payment":
		return "We could not start your payment just now. Please try again."
	case "form", "server":
		return "Something went wrong. Please try again."
	default:
		return ""
	}
}

// ownerErrorMessage maps the short ?error= code the owner write handlers
// (cmd/server/owner_write.go) redirect back with to a human-readable
// message, the same pattern buyGasErrorMessage uses for the customer
// buy-gas form.
func ownerErrorMessage(code string) string {
	switch code {
	case "price":
		return "Enter a valid price greater than zero."
	case "staff":
		return "Check the staff details -- name, phone, role, and a 4-digit PIN are all required, and the phone number must not already be in use."
	case "staff-status":
		return "Could not update that staff member."
	case "cash":
		return "Enter a valid amount, and make sure a shift is currently open."
	case "form":
		return "Something went wrong. Please try again."
	default:
		return ""
	}
}

// adminErrorMessage maps the short ?error= code the admin write handlers
// (cmd/server/admin_write.go) redirect back with to a human-readable
// message.
func adminErrorMessage(code string) string {
	switch code {
	case "missing":
		return "Fill in every field -- plant name, slug, owner name, owner phone, owner password, and a starting price are all required."
	case "slug":
		return "Slugs can only contain lowercase letters, numbers, and hyphens, and can't start or end with a hyphen."
	case "reserved-slug":
		return "That slug is reserved and can't be used for a plant."
	case "slug-taken":
		return "That slug is already in use by another plant."
	case "price":
		return "Enter a starting price per kg greater than zero."
	case "plant":
		return "Could not create that plant. Please try again."
	case "status":
		return "Could not update that plant's status."
	case "form":
		return "Something went wrong. Please try again."
	default:
		return ""
	}
}

func loadOwnerDashboard(ctx context.Context, db tenantdb.Querier, plant *domain.Plant) pages.OwnerDashboardData {
	data := pages.OwnerDashboardData{
		PlantName:    fallbackString(plant.Name, "Your plant"),
		PlantSlug:    fallbackString(plant.Slug, "plant-slug"),
		PlantCity:    ptrString(plant.City, "City coming soon"),
		PlantAddress: ptrString(plant.Address, "Plant address coming soon"),
		PlantPhone:   ptrString(plant.Phone, "Phone number coming soon"),
		DomainStatus: humanize(plant.DomainStatus, "No domain yet"),
		TodayLabel:   time.Now().Format("2 Jan 2006"),
		CurrentPrice: "Price coming soon",
	}

	var latestPrice int64
	if err := db.QueryRow(ctx, `SELECT price_per_kg FROM prices WHERE plant_id = $1 ORDER BY effective_from DESC LIMIT 1`, plant.ID).Scan(&latestPrice); err == nil {
		data.CurrentPrice = formatNaira(latestPrice) + "/kg"
	}

	var totalAmount, paidGrams, cashAmount, electronicAmount, filledGrams int64
	_ = db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(amount), 0),
		       COALESCE(SUM(size_grams), 0),
		       COALESCE(SUM(CASE WHEN channel = 'cash' THEN amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN channel <> 'cash' THEN amount ELSE 0 END), 0),
		       COALESCE(SUM(COALESCE(net_grams, 0)), 0)
		FROM tickets
		WHERE plant_id = $1
		  AND created_at >= date_trunc('day', now())
		  AND status <> 'voided'
	`, plant.ID).Scan(&data.SalesCount, &totalAmount, &paidGrams, &cashAmount, &electronicAmount, &filledGrams)

	diffGrams := filledGrams - paidGrams
	data.MoneyToday = formatNaira(totalAmount)
	data.GasSoldKg = formatKg(paidGrams)
	data.CashTotal = formatNaira(cashAmount)
	data.ElectronicTotal = formatNaira(electronicAmount)
	data.PaidForKg = formatKg(paidGrams)
	data.FilledKg = formatKg(filledGrams)
	data.DiffKg = signedKg(diffGrams)
	if latestPrice > 0 {
		data.DiffAmount = signedNaira((diffGrams * latestPrice) / 1000)
	} else {
		data.DiffAmount = "Coming soon"
	}

	data.RecentTickets = loadOwnerTickets(ctx, db, plant)
	data.Staff = loadOwnerStaff(ctx, db, plant)
	data.Shifts = loadOwnerShifts(ctx, db, plant)
	data.CashMovements = loadOwnerCashMovements(ctx, db, plant)
	data.PriceHistory = loadOwnerPriceHistory(ctx, db, plant)
	data.Activity = loadActivity(ctx, db, plant.ID, 6)
	if _, err := shifts.MostRecentOpenShiftID(ctx, db, plant.ID); err == nil {
		data.HasOpenShift = true
	}
	return data
}

func loadOwnerTickets(ctx context.Context, db tenantdb.Querier, plant *domain.Plant) []pages.OwnerTicketRow {
	rows, err := db.Query(ctx, `
		SELECT reference, code, COALESCE(customer_name, ''), COALESCE(customer_phone, ''),
		       size_grams, COALESCE(net_grams, 0), rate_per_kg, amount, channel, status,
		       created_at, paid_at, filled_at
		FROM tickets
		WHERE plant_id = $1
		ORDER BY created_at DESC
		LIMIT 8
	`, plant.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []pages.OwnerTicketRow{}
	for rows.Next() {
		var row pages.OwnerTicketRow
		var grams, netGrams, rate, amount int64
		var created time.Time
		var paidAt, filledAt sql.NullTime
		if err := rows.Scan(&row.Reference, &row.Code, &row.Customer, &row.Phone, &grams, &netGrams, &rate, &amount, &row.Channel, &row.Status, &created, &paidAt, &filledAt); err != nil {
			continue
		}
		row.Reference = fallbackString(row.Reference, "No reference")
		row.Code = fallbackString(row.Code, "0000")
		row.Customer = fallbackString(row.Customer, "Walk-in customer")
		row.Kg = formatKg(grams)
		row.NetKg = "Awaiting fill"
		if netGrams > 0 {
			row.NetKg = formatKg(netGrams)
		}
		row.Rate = formatNaira(rate) + "/kg"
		row.Amount = formatNaira(amount)
		row.Channel = humanize(row.Channel, "Payment channel")
		row.Status = strings.ToUpper(fallbackString(row.Status, "pending"))
		row.When = created.Format("2 Jan, 15:04")
		row.PaidAt = formatOptionalTime(paidAt, "Not paid yet")
		row.FilledAt = formatOptionalTime(filledAt, "Not filled yet")
		row.BadgeCSS = badgeForStatus(row.Status)
		if row.Phone == "" {
			row.Phone = "No phone"
		}
		out = append(out, row)
	}
	return out
}

func loadOwnerStaff(ctx context.Context, db tenantdb.Querier, plant *domain.Plant) []pages.OwnerStaffRow {
	rows, err := db.Query(ctx, `
		SELECT u.id, u.name, u.phone, u.role, u.active, u.created_at,
		       COUNT(DISTINCT t.id),
		       COUNT(DISTINCT s.id)
		FROM users u
		LEFT JOIN tickets t ON t.sold_by = u.id
		LEFT JOIN shifts s ON s.user_id = u.id
		WHERE u.plant_id = $1
		GROUP BY u.id, u.name, u.phone, u.role, u.active, u.created_at
		ORDER BY u.active DESC, u.role, u.name
	`, plant.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []pages.OwnerStaffRow{}
	for rows.Next() {
		var id uuid.UUID
		var rawRole string
		var active bool
		var row pages.OwnerStaffRow
		var created time.Time
		if err := rows.Scan(&id, &row.Name, &row.Phone, &rawRole, &active, &created, &row.SalesCount, &row.ShiftsCount); err != nil {
			continue
		}
		row.ID = id.String()
		row.Name = fallbackString(row.Name, "Unnamed staff")
		row.Phone = fallbackString(row.Phone, "No phone")
		row.Role = humanize(rawRole, "Staff")
		row.Active = active
		row.CanToggleActive = rawRole == "manager" || rawRole == "cashier" || rawRole == "operator"
		if active {
			row.Status = "Active"
			row.BadgeCSS = "badge-ok"
		} else {
			row.Status = "Inactive"
			row.BadgeCSS = "badge-muted"
		}
		row.Joined = created.Format("2 Jan 2006")
		out = append(out, row)
	}
	return out
}

func loadOwnerShifts(ctx context.Context, db tenantdb.Querier, plant *domain.Plant) []pages.OwnerShiftRow {
	rows, err := db.Query(ctx, `
		SELECT u.name, COALESCE(s.device_id, ''), s.opened_at, s.closed_at, COUNT(DISTINCT t.id),
		       s.opening_cash_kobo, s.closing_cash_kobo, s.tokens_issued, s.tokens_returned, COALESCE(s.notes, '')
		FROM shifts s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN tickets t ON t.shift_id = s.id OR t.filled_shift_id = s.id
		WHERE s.plant_id = $1
		GROUP BY s.id, u.name
		ORDER BY s.opened_at DESC
		LIMIT 6
	`, plant.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []pages.OwnerShiftRow{}
	for rows.Next() {
		var closed sql.NullTime
		var closing sql.NullInt64
		var returned sql.NullInt32
		var opened time.Time
		var opening int64
		var row pages.OwnerShiftRow
		if err := rows.Scan(&row.Staff, &row.Device, &opened, &closed, &row.Tickets, &opening, &closing, &row.TokensIssued, &returned, &row.Notes); err != nil {
			continue
		}
		row.Staff = fallbackString(row.Staff, "Unnamed staff")
		row.Device = fallbackString(row.Device, "No device")
		row.Opened = opened.Format("2 Jan, 15:04")
		row.Closed = "Open"
		row.ClosingCash = "Open"
		row.TokensReturned = "Open"
		if returned.Valid {
			row.TokensReturned = fmt.Sprintf("%d", returned.Int32)
		}
		row.Notes = fallbackString(row.Notes, "No note")
		row.Status = "Open"
		row.BadgeCSS = "badge-warn"
		if closed.Valid {
			row.Closed = closed.Time.Format("2 Jan, 15:04")
			row.Status = "Closed"
			row.BadgeCSS = "badge-ok"
		}
		row.OpeningCash = formatNaira(opening)
		if closing.Valid {
			row.ClosingCash = formatNaira(closing.Int64)
		}
		out = append(out, row)
	}
	return out
}

func loadOwnerCashMovements(ctx context.Context, db tenantdb.Querier, plant *domain.Plant) []pages.OwnerCashRow {
	rows, err := db.Query(ctx, `
		SELECT cm.created_at, cm.kind, cm.amount_kobo, cm.counted_kobo,
		       COALESCE(u.name, ''), COALESCE(t.code, ''), COALESCE(cm.note, '')
		FROM cash_movements cm
		LEFT JOIN users u ON u.id = cm.recorded_by
		LEFT JOIN tickets t ON t.id = cm.ticket_id
		WHERE cm.plant_id = $1
		ORDER BY cm.created_at DESC
		LIMIT 10
	`, plant.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []pages.OwnerCashRow{}
	for rows.Next() {
		var row pages.OwnerCashRow
		var created time.Time
		var amount int64
		var counted sql.NullInt64
		if err := rows.Scan(&created, &row.Kind, &amount, &counted, &row.Recorder, &row.Ticket, &row.Note); err != nil {
			continue
		}
		row.When = created.Format("2 Jan, 15:04")
		row.Kind = humanize(row.Kind, "Cash movement")
		row.Amount = signedNaira(amount)
		row.Counted = "Not counted"
		if counted.Valid {
			row.Counted = formatNaira(counted.Int64)
		}
		if row.Recorder == "" {
			row.Recorder = "System"
		}
		if row.Ticket == "" {
			row.Ticket = "No ticket"
		}
		if row.Note == "" {
			row.Note = "No note"
		}
		out = append(out, row)
	}
	return out
}

func loadOwnerPriceHistory(ctx context.Context, db tenantdb.Querier, plant *domain.Plant) []pages.OwnerPriceRow {
	rows, err := db.Query(ctx, `
		SELECT p.effective_from, p.price_per_kg, COALESCE(u.name, ''), p.created_at
		FROM prices p
		LEFT JOIN users u ON u.id = p.set_by
		WHERE p.plant_id = $1
		ORDER BY p.effective_from DESC
		LIMIT 8
	`, plant.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []pages.OwnerPriceRow{}
	for rows.Next() {
		var row pages.OwnerPriceRow
		var effective time.Time
		var created time.Time
		var price int64
		if err := rows.Scan(&effective, &price, &row.SetBy, &created); err != nil {
			continue
		}
		row.EffectiveFrom = effective.Format("2 Jan, 15:04")
		row.Price = formatNaira(price) + "/kg"
		row.CreatedAt = created.Format("2 Jan, 15:04")
		if row.SetBy == "" {
			row.SetBy = "System"
		}
		out = append(out, row)
	}
	return out
}

func loadAdminConsole(ctx context.Context, db tenantdb.Querier) pages.AdminConsoleData {
	data := pages.AdminConsoleData{}

	_ = db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE status = 'active'),
		       COUNT(*) FILTER (WHERE status = 'provisioning'),
		       COUNT(*) FILTER (WHERE status = 'suspended'),
		       COUNT(*) FILTER (WHERE status = 'closed')
		FROM plants
	`).Scan(&data.PlantCount, &data.ActiveCount, &data.OnboardingCount, &data.SuspendedCount, &data.ClosedCount)

	_ = db.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE domain_status = 'pending'),
		       COUNT(*) FILTER (WHERE domain_status = 'broken')
		FROM plants
	`).Scan(&data.DomainPending, &data.DomainBroken)

	var volume30d int64
	_ = db.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE created_at >= now() - interval '7 days'),
		       COALESCE(SUM(size_grams) FILTER (WHERE created_at >= now() - interval '30 days' AND status <> 'voided'), 0)
		FROM tickets
	`).Scan(&data.TotalTickets7d, &volume30d)
	data.TotalVolume30d = formatKg(volume30d)
	data.Plants = loadAdminPlants(ctx, db)
	data.RecentActivity = loadActivity(ctx, db, nil, 8)
	data.ReservedSlugs = loadReservedSlugs(ctx, db)
	data.PlatformStaff = loadPlatformStaff(ctx, db)
	data.SystemTables = loadSystemTables(ctx, db)
	return data
}

func loadAdminPlants(ctx context.Context, db tenantdb.Querier) []pages.AdminPlantRow {
	rows, err := db.Query(ctx, `
		SELECT p.id, p.name, COALESCE(p.city, ''), COALESCE(p.address, ''), COALESCE(p.phone, ''),
		       p.slug, p.status, COALESCE(p.custom_domain, ''), p.domain_status,
		       (SELECT MIN(t.created_at) FROM tickets t WHERE t.plant_id = p.id),
		       (SELECT COUNT(*) FROM tickets t WHERE t.plant_id = p.id AND t.created_at >= now() - interval '7 days'),
		       (SELECT COALESCE(SUM(t.size_grams), 0) FROM tickets t WHERE t.plant_id = p.id AND t.created_at >= now() - interval '30 days' AND t.status <> 'voided'),
		       (SELECT MAX(t.created_at) FROM tickets t WHERE t.plant_id = p.id),
		       (SELECT COUNT(*) FROM users u WHERE u.plant_id = p.id),
		       (SELECT COUNT(*) FROM prices pr WHERE pr.plant_id = p.id),
		       (SELECT COUNT(*) FROM shifts s WHERE s.plant_id = p.id AND s.closed_at IS NULL),
		       (SELECT COUNT(*) FROM activity_log a WHERE a.plant_id = p.id),
		       p.created_at, p.updated_at
		FROM plants p
		ORDER BY p.created_at DESC
		LIMIT 40
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []pages.AdminPlantRow{}
	for rows.Next() {
		var id uuid.UUID
		var row pages.AdminPlantRow
		var firstTxn, lastTxn sql.NullTime
		var created, updated time.Time
		var volume int64
		if err := rows.Scan(&id, &row.Name, &row.City, &row.Address, &row.Phone, &row.Slug, &row.Status, &row.Domain, &row.DomainStatus, &firstTxn, &row.Txns7d, &volume, &lastTxn, &row.StaffCount, &row.PriceCount, &row.OpenShifts, &row.ActivityCount, &created, &updated); err != nil {
			continue
		}
		row.ID = id.String()
		row.RawStatus = strings.ToLower(fallbackString(row.Status, "provisioning"))
		row.Name = fallbackString(row.Name, "Unnamed plant")
		if row.City == "" {
			row.City = "Coming soon"
		}
		if row.Address == "" {
			row.Address = "Address coming soon"
		}
		if row.Phone == "" {
			row.Phone = "Phone coming soon"
		}
		if row.Domain == "" {
			row.Domain = row.Slug + ".gatstogo.local"
		}
		row.FirstTxn = "No transaction yet"
		if firstTxn.Valid {
			row.FirstTxn = firstTxn.Time.Format("2 Jan 2006")
		}
		row.LastTxn = "No transaction yet"
		if lastTxn.Valid {
			row.LastTxn = lastTxn.Time.Format("2 Jan, 15:04")
		}
		row.Volume30d = formatKg(volume)
		row.Status = strings.ToUpper(fallbackString(row.Status, "provisioning"))
		row.DomainStatus = strings.ToUpper(fallbackString(row.DomainStatus, "none"))
		row.CreatedAt = created.Format("2 Jan 2006")
		row.UpdatedAt = updated.Format("2 Jan 2006")
		row.BadgeCSS = badgeForStatus(row.Status)
		row.DomainBadge = badgeForDomain(row.DomainStatus)
		out = append(out, row)
	}
	return out
}

func loadPlatformStaff(ctx context.Context, db tenantdb.Querier) []pages.AdminStaffRow {
	rows, err := db.Query(ctx, `
		SELECT name, phone, active, COUNT(DISTINCT plant_id)
		FROM users
		WHERE role = 'admin'
		GROUP BY id, name, phone, active
		ORDER BY active DESC, name
		LIMIT 12
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []pages.AdminStaffRow{}
	for rows.Next() {
		var row pages.AdminStaffRow
		var active bool
		if err := rows.Scan(&row.Name, &row.Phone, &active, &row.Plants); err != nil {
			continue
		}
		row.Name = fallbackString(row.Name, "Unnamed admin")
		row.Phone = fallbackString(row.Phone, "No phone")
		if active {
			row.Status = "Active"
		} else {
			row.Status = "Inactive"
		}
		out = append(out, row)
	}
	return out
}

func loadSystemTables(ctx context.Context, db tenantdb.Querier) []pages.AdminSystemTable {
	defs := []pages.AdminSystemTable{
		{Name: "plants", Role: "Tenant profile, branding, domain, and lifecycle.", Description: "One row per gas plant."},
		{Name: "reserved_slugs", Role: "Protected subdomains that plants cannot claim.", Description: "Keeps admin, api, support, and system names safe."},
		{Name: "users", Role: "Owners, managers, cashiers, operators, and admins.", Description: "Access is scoped to a plant."},
		{Name: "prices", Role: "Immutable price history used when tickets are sold.", Description: "Never overwrite old rates."},
		{Name: "shifts", Role: "Cashier/operator work sessions and drawer totals.", Description: "One open shift per user per plant."},
		{Name: "tickets", Role: "Customer purchases, payment state, and receipt proof.", Description: "The main transaction table."},
		{Name: "cash_movements", Role: "Opening float, sales, deposits, payouts, counts, and closing.", Description: "Tied back to shifts."},
		{Name: "activity_log", Role: "Audit trail for plant and platform actions.", Description: "Useful for support and dispute review."},
	}
	for i := range defs {
		defs[i].Records = countRows(ctx, db, defs[i].Name)
		defs[i].Health = "Ready"
	}
	return defs
}

func countRows(ctx context.Context, db tenantdb.Querier, table string) int64 {
	allowed := map[string]bool{
		"plants":         true,
		"reserved_slugs": true,
		"users":          true,
		"prices":         true,
		"shifts":         true,
		"tickets":        true,
		"cash_movements": true,
		"activity_log":   true,
	}
	if !allowed[table] {
		return 0
	}
	var count int64
	_ = db.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count)
	return count
}

func loadActivity(ctx context.Context, db tenantdb.Querier, plantID any, limit int) []pages.OwnerActivityRow {
	query := `
		SELECT action, COALESCE(subject, ''), created_at
		FROM activity_log
		ORDER BY created_at DESC
		LIMIT $1
	`
	args := []any{limit}
	if plantID != nil {
		query = `
			SELECT action, COALESCE(subject, ''), created_at
			FROM activity_log
			WHERE plant_id = $1
			ORDER BY created_at DESC
			LIMIT $2
		`
		args = []any{plantID, limit}
	}
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []pages.OwnerActivityRow{}
	for rows.Next() {
		var row pages.OwnerActivityRow
		var created time.Time
		if err := rows.Scan(&row.Action, &row.Subject, &created); err != nil {
			continue
		}
		if row.Subject == "" {
			row.Subject = "No subject"
		}
		row.When = created.Format("2 Jan, 15:04")
		out = append(out, row)
	}
	return out
}

func loadReservedSlugs(ctx context.Context, db tenantdb.Querier) []string {
	rows, err := db.Query(ctx, `SELECT slug FROM reserved_slugs ORDER BY slug LIMIT 40`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			continue
		}
		out = append(out, slug)
	}
	return out
}

func formatNaira(kobo int64) string {
	naira := float64(kobo) / 100
	return "₦" + formatWholeNumber(int64(math.Round(naira)))
}

func signedNaira(kobo int64) string {
	if kobo > 0 {
		return "+" + formatNaira(kobo)
	}
	if kobo < 0 {
		return "-" + formatNaira(-kobo)
	}
	return formatNaira(0)
}

func formatKg(grams int64) string {
	return fmt.Sprintf("%.2f kg", float64(grams)/1000)
}

func signedKg(grams int64) string {
	if grams > 0 {
		return "+" + formatKg(grams)
	}
	if grams < 0 {
		return "-" + formatKg(-grams)
	}
	return formatKg(0)
}

func formatWholeNumber(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	prefix := len(s) % 3
	if prefix == 0 {
		prefix = 3
	}
	out = append(out, s[:prefix]...)
	for i := prefix; i < len(s); i += 3 {
		out = append(out, ',')
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}

func badgeForStatus(status string) string {
	switch strings.ToLower(status) {
	case "active", "paid", "filled":
		return "badge-ok"
	case "pending", "provisioning":
		return "badge-warn"
	case "voided", "suspended", "closed", "expired":
		return "badge-danger"
	default:
		return "badge-muted"
	}
}

func badgeForDomain(status string) string {
	switch strings.ToLower(status) {
	case "active":
		return "badge-ok"
	case "pending":
		return "badge-warn"
	case "broken":
		return "badge-danger"
	default:
		return "badge-muted"
	}
}

func formatOptionalTime(value sql.NullTime, fallback string) string {
	if !value.Valid {
		return fallback
	}
	return value.Time.Format("2 Jan, 15:04")
}

func humanize(value string, fallback string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "_", " "))
	if value == "" {
		return fallback
	}
	return strings.Title(value)
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func ptrString(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}
