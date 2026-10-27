package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gatstogo/internal/audit"
	"gatstogo/internal/auth"
	"gatstogo/internal/csrf"
	"gatstogo/internal/domain"
	"gatstogo/internal/hours"
	"gatstogo/internal/middleware"
	"gatstogo/internal/prices"
	"gatstogo/internal/pumps"
	"gatstogo/internal/reviews"
	"gatstogo/internal/session"
	"gatstogo/internal/shifts"
	staffsvc "gatstogo/internal/staff"
	"gatstogo/internal/tenantdb"
	"gatstogo/internal/tickets"
	"gatstogo/web/templates/components"
	"gatstogo/web/templates/pages"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These endpoints (besides terminalPageHandler) are JSON, not HTML -- the
// staff terminal is a fetch()-driven single-page UI (see
// web/templates/pages/terminal.templ), unlike the rest of this
// server-rendered app. writeJSON/writeJSONError are this file's
// equivalent of the render calls everywhere else.

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

var errTerminalForbidden = errors.New("terminal: forbidden")

// ---- GET /terminal (the page itself) ----

// terminalInitPayload is embedded in the page as JSON (see
// terminal.templ) for the frontend to bootstrap from -- the real staff
// list and live price, replacing what used to be hardcoded demo data
// baked into the JS itself.
type terminalInitPayload struct {
	PlantName      string                  `json:"plantName"`
	PlantLogo      string                  `json:"plantLogo"` // plant's uploaded logo, or the GatsToGo mark
	TodayLabel     string                  `json:"todayLabel"`
	Staff          []terminalStaffPayload  `json:"staff"`
	ManageStaff    []terminalManageStaff   `json:"manageStaff"`
	Pumps          []terminalPumpPayload   `json:"pumps"`
	Reviews        []terminalReviewPayload `json:"reviews"`
	ReviewCount    int64                   `json:"reviewCount"`
	ReviewAverage  string                  `json:"reviewAverage"`
	CSRFToken      string                  `json:"csrfToken"`
	PricePerKgKobo int64                   `json:"pricePerKgKobo"`
	CurrentSession *terminalSessionPayload `json:"currentSession,omitempty"`
}

type terminalPumpPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	SortOrder int    `json:"sort_order"`
	BusyCount int    `json:"busy_count"`
	OpenShift bool   `json:"open_shift"`
}

type terminalStaffPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	// ShiftOpenSince is set only when this staff member already has an
	// open shift at this plant (formatted "3:04 PM") -- picking their
	// tile and starting would resume it (StartOrResume), not open a new
	// one. Empty means no open shift.
	ShiftOpenSince string `json:"shiftOpenSince,omitempty"`
}

type terminalManageStaff struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Active bool   `json:"active"`
}

type terminalReviewPayload struct {
	Ticket      string `json:"ticket"`
	Phone       string `json:"phone"`
	Rating      int    `json:"rating"`
	Stars       string `json:"stars"`
	Note        string `json:"note"`
	SubmittedAt string `json:"submitted_at"`
}

type terminalSessionPayload struct {
	ShiftID         string `json:"shift_id"`
	UserID          string `json:"user_id"`
	Name            string `json:"name"`
	Role            string `json:"role"`
	OpeningCashKobo int64  `json:"opening_cash_kobo"`
	PumpID          string `json:"pump_id,omitempty"`
	PumpName        string `json:"pump_name,omitempty"`
	FillsCompleted  int    `json:"fills_completed"`
	GramsDispensed  int64  `json:"grams_dispensed"`
	CashHeldKobo    int64  `json:"cash_held_kobo"`
	CanEditPrice    bool   `json:"can_edit_price"`
	CanManageStaff  bool   `json:"can_manage_staff"`
	CanManagePumps  bool   `json:"can_manage_pumps"`
	CSRFToken       string `json:"csrf_token"`
}

func terminalPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}

		var (
			staff       []shifts.StaffMember
			pumpList    []pumps.Pump
			pricePerKg  int64
			openSince   map[uuid.UUID]time.Time
			staffRows   []terminalManageStaff
			reviewRows  []terminalReviewPayload
			reviewCount int64
			reviewAvg   string
		)
		_ = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			var err error
			if staff, err = shifts.ListStaff(ctx, q, plant.ID); err != nil {
				log.Println("terminal page: list staff failed:", err)
			}
			if _, pricePerKg, err = loadCurrentPrice(ctx, q, plant.ID); err != nil {
				log.Println("terminal page: load price failed:", err)
			}
			if openSince, err = shifts.OpenShiftStarts(ctx, q, plant.ID); err != nil {
				log.Println("terminal page: load open shifts failed:", err)
			}
			if pumpList, err = pumps.List(ctx, q, plant.ID); err != nil {
				log.Println("terminal page: load pumps failed:", err)
			}
			if staffRows, err = listTerminalManageStaff(ctx, q, plant.ID); err != nil {
				log.Println("terminal page: load staff management rows failed:", err)
			}
			if reviewRows, reviewCount, reviewAvg = loadTerminalReviews(ctx, q, plant); reviewAvg == "" {
				reviewAvg = "No reviews yet"
			}
			return nil // best-effort: an empty staff list or zero price still renders a usable (if unusable-until-fixed) page
		})

		plantName := strings.TrimSpace(plant.Name)
		if plantName == "" {
			plantName = "Gas plant" // same fallback pages.StaffTerminal's own <title>/data-plant uses
		}
		plantLogo := components.BrandMarkPrimary
		if plant.LogoPath != nil && strings.TrimSpace(*plant.LogoPath) != "" {
			plantLogo = strings.TrimSpace(*plant.LogoPath)
		}
		payload := terminalInitPayload{
			PlantName:      plantName,
			PlantLogo:      plantLogo,
			TodayLabel:     time.Now().Format("2 Jan 2006"),
			Staff:          make([]terminalStaffPayload, len(staff)),
			ManageStaff:    staffRows,
			Pumps:          make([]terminalPumpPayload, len(pumpList)),
			Reviews:        reviewRows,
			ReviewCount:    reviewCount,
			ReviewAverage:  reviewAvg,
			CSRFToken:      middleware.EnsurePublicCSRFCookie(w, r),
			PricePerKgKobo: pricePerKg,
		}
		for i, m := range staff {
			payload.Staff[i] = terminalStaffPayload{ID: m.ID.String(), Name: m.Name, Role: m.Role}
			if openedAt, ok := openSince[m.ID]; ok {
				payload.Staff[i].ShiftOpenSince = openedAt.Format("3:04 PM")
			}
		}
		for i, p := range pumpList {
			payload.Pumps[i] = terminalPumpPayload{ID: p.ID.String(), Name: p.Name, Status: p.Status, SortOrder: p.SortOrder, BusyCount: p.BusyCount, OpenShift: p.OpenShift}
		}
		payload.CurrentSession = loadCurrentTerminalSession(r.Context(), s, w, r, plant.ID)

		// Passed straight through to templ.JSONScript, which does its own
		// json.Marshal -- previously this pre-marshaled to a string and
		// interpolated it as {initJSON} inside a literal <script> tag,
		// which templ does NOT run Go-expression interpolation inside
		// (script bodies are treated as opaque text, same as any other
		// HTML element's raw text content) -- so the page was actually
		// shipping the eight literal characters "{ initJSON }" to the
		// browser instead of real JSON, and every terminal page load
		// would fail at JSON.parse(...) before the terminal could boot at
		// all. Only caught by rendering this page for real against a
		// live server; the JS itself had only ever been tested with a
		// hand-rolled harness that supplied a mocked init object directly,
		// never through templ's actual renderer.
		if err := pages.StaffTerminal(plant, payload).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func loadCurrentTerminalSession(ctx context.Context, s *Server, w http.ResponseWriter, r *http.Request, plantID uuid.UUID) *terminalSessionPayload {
	token, ok := session.ReadCookie(r, session.CookieStaff)
	if !ok {
		return nil
	}
	data, err := s.Sessions.Load(ctx, token)
	if err != nil {
		session.ClearCookie(w, session.CookieStaff)
		return nil
	}
	staffRole := data.Role == session.RoleManager || data.Role == session.RoleCashier || data.Role == session.RoleOperator
	if !staffRole || data.PlantID != plantID.String() || data.ShiftID == "" {
		session.ClearCookie(w, session.CookieStaff)
		return nil
	}
	userID, err := uuid.Parse(data.UserID)
	if err != nil {
		session.ClearCookie(w, session.CookieStaff)
		return nil
	}
	shiftID, err := uuid.Parse(data.ShiftID)
	if err != nil {
		session.ClearCookie(w, session.CookieStaff)
		return nil
	}

	payload := &terminalSessionPayload{ShiftID: shiftID.String(), UserID: userID.String()}
	err = tenantdb.WithTenant(ctx, s.AppDB, plantID, func(ctx context.Context, q tenantdb.Querier) error {
		var role string
		var active bool
		if err := q.QueryRow(ctx, `
			SELECT u.name, u.role, u.active, u.can_edit_price, u.can_manage_staff, u.can_manage_pumps,
			       s.opening_cash_kobo, COALESCE(s.pump_id::text, ''), COALESCE(p.name, '')
			FROM users u
			JOIN shifts s ON s.user_id = u.id AND s.plant_id = u.plant_id
			LEFT JOIN pumps p ON p.id = s.pump_id AND p.plant_id = s.plant_id
			WHERE u.id = $1
			  AND u.plant_id = $2
			  AND s.id = $3
			  AND s.closed_at IS NULL
			  AND u.role IN ('manager', 'cashier', 'operator')
		`, userID, plantID, shiftID).Scan(
			&payload.Name, &role, &active, &payload.CanEditPrice, &payload.CanManageStaff, &payload.CanManagePumps,
			&payload.OpeningCashKobo, &payload.PumpID, &payload.PumpName,
		); err != nil {
			return err
		}
		if !active {
			return errTerminalForbidden
		}
		payload.Role = role
		var err error
		payload.FillsCompleted, payload.GramsDispensed, payload.CashHeldKobo, err = shifts.Summary(ctx, q, plantID, shiftID)
		return err
	})
	if err != nil {
		session.ClearCookie(w, session.CookieStaff)
		return nil
	}

	_ = s.Sessions.Touch(ctx, token, session.StaffBackstopTTL)
	session.WriteCookie(w, session.CookieStaff, token, session.StaffBackstopTTL)
	payload.CSRFToken, _ = csrf.Token(token)
	return payload
}

func listTerminalManageStaff(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID) ([]terminalManageStaff, error) {
	rows, err := q.Query(ctx, `
		SELECT id::text, name, role, active
		FROM users
		WHERE plant_id = $1
		  AND role IN ('manager', 'cashier', 'operator')
		ORDER BY
		  CASE role
		    WHEN 'manager' THEN 1
		    WHEN 'cashier' THEN 2
		    ELSE 3
		  END,
		  name
	`, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []terminalManageStaff{}
	for rows.Next() {
		var item terminalManageStaff
		if err := rows.Scan(&item.ID, &item.Name, &item.Role, &item.Active); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func loadTerminalReviews(ctx context.Context, q tenantdb.Querier, plant *domain.Plant) ([]terminalReviewPayload, int64, string) {
	stats, err := reviews.Stats(ctx, q, plant.ID)
	if err != nil {
		return nil, 0, "No reviews yet"
	}
	rows, err := reviews.List(ctx, q, plant.ID, 30)
	if err != nil {
		return nil, stats.Count, formatReviewAverage(stats.Average, stats.Count)
	}
	out := make([]terminalReviewPayload, 0, len(rows))
	for _, r := range rows {
		out = append(out, terminalReviewPayload{
			Ticket:      shortReviewTicketRef(r.TicketReference, plant.Slug),
			Phone:       fallbackString(r.CustomerPhone, "No phone"),
			Rating:      r.Rating,
			Stars:       reviewStarsText(r.Rating),
			Note:        fallbackString(r.Note, "No note"),
			SubmittedAt: r.SubmittedAt.Format("2 Jan, 15:04"),
		})
	}
	return out, stats.Count, formatReviewAverage(stats.Average, stats.Count)
}

func terminalManagerCan(ctx context.Context, q tenantdb.Querier, plantID, userID uuid.UUID, permission string) error {
	var query string
	switch permission {
	case "can_edit_price":
		query = `
			SELECT can_edit_price
			FROM users
			WHERE id = $1
			  AND plant_id = $2
			  AND role = 'manager'
			  AND active = TRUE
		`
	case "can_manage_staff":
		query = `
			SELECT can_manage_staff
			FROM users
			WHERE id = $1
			  AND plant_id = $2
			  AND role = 'manager'
			  AND active = TRUE
		`
	case "can_manage_pumps":
		query = `
			SELECT can_manage_pumps
			FROM users
			WHERE id = $1
			  AND plant_id = $2
			  AND role = 'manager'
			  AND active = TRUE
		`
	default:
		return errTerminalForbidden
	}

	var allowed bool
	if err := q.QueryRow(ctx, query, userID, plantID).Scan(&allowed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errTerminalForbidden
		}
		return err
	}
	if !allowed {
		return errTerminalForbidden
	}
	return nil
}

// ---- POST /terminal/pin (public: no staff session exists yet) ----

func terminalPinHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}

		userID, err := uuid.Parse(r.FormValue("user_id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "choose your name")
			return
		}
		pin := r.FormValue("pin")
		if pin == "" {
			writeJSONError(w, http.StatusBadRequest, "enter your PIN")
			return
		}
		openingCashNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("opening_cash")), 64)
		if err != nil || openingCashNaira < 0 {
			writeJSONError(w, http.StatusBadRequest, "enter the opening cash amount")
			return
		}
		openingCashKobo := int64(math.Round(openingCashNaira * 100))
		deviceID := strings.TrimSpace(r.FormValue("device_id"))
		rawPumpID := strings.TrimSpace(r.FormValue("pump_id"))
		var requestedPumpID *uuid.UUID
		if rawPumpID != "" {
			pumpID, err := uuid.Parse(rawPumpID)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "choose a valid pump")
				return
			}
			requestedPumpID = &pumpID
		}

		// Checked before touching the database -- a locked-out user
		// shouldn't get a fresh 20-second window just by retrying.
		locked, retryAfter, err := s.PINLimiter.Locked(r.Context(), userID)
		if err != nil {
			log.Println("terminal pin: lock check failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
		if locked {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":               "terminal locked, try again shortly",
				"retry_after_seconds": int(retryAfter.Seconds()),
			})
			return
		}

		var (
			name, role                           string
			pinHash                              *string
			active, canEditPrice, canManageStaff bool
			canManagePumps, found                bool
		)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			row := q.QueryRow(ctx, `
				SELECT name, role, pin_hash, active, can_edit_price, can_manage_staff, can_manage_pumps FROM users
				WHERE id = $1 AND plant_id = $2 AND role IN ('manager', 'cashier', 'operator')
			`, userID, plant.ID)
			switch scanErr := row.Scan(&name, &role, &pinHash, &active, &canEditPrice, &canManageStaff, &canManagePumps); scanErr {
			case nil:
				found = true
				return nil
			case pgx.ErrNoRows:
				found = false
				return nil
			default:
				return scanErr
			}
		})
		if err != nil {
			log.Println("terminal pin: lookup failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong")
			return
		}

		hash := ""
		if pinHash != nil {
			hash = *pinHash
		}
		if !found || !active || !auth.VerifyPassword(hash, pin) {
			if found {
				if err := s.PINLimiter.RecordFailure(r.Context(), userID); err != nil {
					log.Println("terminal pin: record failure error:", err)
				}
			}
			writeJSONError(w, http.StatusUnauthorized, "incorrect PIN")
			return
		}
		_ = s.PINLimiter.Reset(r.Context(), userID)
		if role == string(session.RoleOperator) && requestedPumpID == nil {
			writeJSONError(w, http.StatusBadRequest, "operators must choose a pump")
			return
		}

		var (
			shift                        *shifts.Shift
			resumed                      bool
			fillsCompleted               int
			gramsDispensed, cashHeldKobo int64
			pumpName                     string
			sessionPumpID                string
		)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if requestedPumpID != nil {
				if err := pumps.ValidateShiftPump(ctx, q, plant.ID, *requestedPumpID, userID); err != nil {
					return err
				}
			}
			sh, wasResumed, err := shifts.StartOrResume(ctx, q, plant.ID, userID, openingCashKobo, deviceID, requestedPumpID)
			if err != nil {
				return err
			}
			shift, resumed = sh, wasResumed
			if sh.PumpID != nil {
				sessionPumpID = sh.PumpID.String()
				_ = q.QueryRow(ctx, `SELECT name FROM pumps WHERE id = $1 AND plant_id = $2`, *sh.PumpID, plant.ID).Scan(&pumpName)
			}
			if !wasResumed {
				if err := audit.Log(ctx, q, &plant.ID, &userID, "shift.opened", "", map[string]any{"opening_cash_kobo": sh.OpeningCashKobo}); err != nil {
					return err
				}
			}
			fillsCompleted, gramsDispensed, cashHeldKobo, err = shifts.Summary(ctx, q, plant.ID, sh.ID)
			return err
		})
		if err != nil {
			log.Println("terminal pin: start shift failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not start shift")
			return
		}

		sessionToken, err := s.Sessions.Create(r.Context(), session.Data{
			UserID:         userID.String(),
			Role:           session.Role(role),
			Name:           name,
			PlantID:        plant.ID.String(),
			ShiftID:        shift.ID.String(),
			PumpID:         sessionPumpID,
			CanEditPrice:   canEditPrice,
			CanManageStaff: canManageStaff,
			CanManagePumps: canManagePumps,
		}, session.StaffBackstopTTL)
		if err != nil {
			log.Println("terminal pin: create session failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
		session.WriteCookie(w, session.CookieStaff, sessionToken, session.StaffBackstopTTL)
		csrfToken, _ := csrf.Token(sessionToken)

		writeJSON(w, http.StatusOK, map[string]any{
			"shift_id":          shift.ID,
			"user_id":           userID,
			"name":              name,
			"role":              role,
			"opened_at":         shift.OpenedAt,
			"opening_cash_kobo": shift.OpeningCashKobo,
			"pump_id":           sessionPumpID,
			"pump_name":         pumpName,
			"fills_completed":   fillsCompleted,
			"grams_dispensed":   gramsDispensed,
			"cash_held_kobo":    cashHeldKobo,
			"resumed":           resumed,
			"can_edit_price":    canEditPrice,
			"can_manage_staff":  canManageStaff,
			"can_manage_pumps":  canManagePumps,
			"csrf_token":        csrfToken,
		})
	}
}

// ---- Everything below requires middleware.RequireStaffSession ----

// terminalRedeemHandler looks up a ticket by its 4-digit code without
// exposing the customer's name, phone, or reference -- only initials, the
// same privacy boundary the terminal mockup this replaces was explicit
// about.
func terminalRedeemHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if actor.Role == session.RoleOperator {
			writeJSONError(w, http.StatusForbidden, "cashier or manager must start dispensing")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		code := strings.TrimSpace(r.FormValue("code"))
		if len(code) != 4 {
			writeJSONError(w, http.StatusBadRequest, "enter a 4-digit code")
			return
		}

		var redeemed *tickets.RedeemedTicket
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			t, err := tickets.Redeem(ctx, q, plant.ID, code)
			redeemed = t
			return err
		})
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]any{
				"ticket_id": redeemed.ID,
				"initials":  redeemed.Initials,
				"size_kg":   formatKg(int64(redeemed.SizeGrams)),
				"amount":    formatNaira(redeemed.AmountKobo),
				"channel":   humanize(redeemed.Channel, "Payment channel"),
				"pump":      fallbackString(redeemed.PumpName, "Assigned pump"),
			})
		case errors.Is(err, tickets.ErrCodeAlreadyRedeemed):
			writeJSONError(w, http.StatusConflict, "this code has already started dispensing or has been filled")
		case errors.Is(err, tickets.ErrCodeNotFound), errors.Is(err, tickets.ErrCodeNotPayable):
			writeJSONError(w, http.StatusNotFound, "incorrect code")
		default:
			log.Println("terminal redeem: failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong")
		}
	}
}

func terminalFillHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if actor.Role == session.RoleOperator {
			writeJSONError(w, http.StatusForbidden, "cashier or manager must start dispensing")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		ticketID, err := uuid.Parse(r.FormValue("ticket_id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid ticket")
			return
		}

		var notice dispensingNotification
		var reviewURL string
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if actor.Role == session.RoleOperator {
				return errTerminalForbidden
			}
			if err := tickets.StartDispensing(ctx, q, plant.ID, ticketID, actor.UserID); err != nil {
				return err
			}
			var phone, code, pumpName string
			var sizeGrams int
			_ = q.QueryRow(ctx, `
				SELECT COALESCE(t.customer_phone, ''), t.code, t.size_grams, COALESCE(p.name, '')
				FROM tickets t
				LEFT JOIN pumps p ON p.id = t.pump_id AND p.plant_id = t.plant_id
				WHERE t.id = $1 AND t.plant_id = $2
			`, ticketID, plant.ID).Scan(&phone, &code, &sizeGrams, &pumpName)
			notice = dispensingNotification{
				PlantName:      plant.Name,
				TermiiSenderID: ptrString(plant.TermiiSenderID, ""),
				CustomerPhone:  phone,
				Code:           code,
				SizeLabel:      formatKg(int64(sizeGrams)),
				PumpName:       pumpName,
			}
			reviewReq, _, err := reviews.EnsureRequest(ctx, q, plant.ID, ticketID)
			if err != nil {
				return err
			}
			reviewURL = tenantAbsoluteURL(s.AppBaseURL, plant.Slug, "/reviews/"+reviewReq.Token)
			if err := audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.dispensing_started", ticketID.String(), nil); err != nil {
				return err
			}
			return nil
		})
		if errors.Is(err, errTerminalForbidden) {
			writeJSONError(w, http.StatusForbidden, "cashier or manager must start dispensing")
			return
		}
		if err != nil {
			log.Println("terminal fill: failed:", err)
			writeJSONError(w, http.StatusBadRequest, "could not start dispensing")
			return
		}
		if reviewURL != "" {
			log.Println("review link:", reviewURL)
		}
		go notifyDispensingStarted(context.Background(), s, notice)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
		})
	}
}

func terminalCashSaleHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if actor.Role == session.RoleOperator {
			writeJSONError(w, http.StatusForbidden, "cashier or manager must record cash sales")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		amountNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount_naira")), 64)
		if err != nil || amountNaira <= 0 {
			writeJSONError(w, http.StatusBadRequest, "enter the cash amount")
			return
		}
		phone := strings.TrimSpace(r.FormValue("phone"))
		amountKobo := int64(math.Round(amountNaira * 100))

		var (
			ticket       *tickets.Ticket
			sizeGrams    int
			cashHeldKobo int64
			pricePerKg   int64
			pumpName     string
		)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			openStatus, err := hours.StatusNow(ctx, q, plant.ID, plant.Timezone)
			if err != nil {
				return err
			}
			if !openStatus.IsOpen {
				return errPlantClosed
			}
			priceID, currentPrice, err := loadCurrentPrice(ctx, q, plant.ID)
			if err != nil {
				return err
			}
			pricePerKg = currentPrice
			if pricePerKg <= 0 {
				return errors.New("cash sale price is not set")
			}
			sizeGrams = int(math.Round((float64(amountKobo) * 1000) / float64(pricePerKg)))
			if sizeGrams <= 0 {
				return errors.New("cash sale amount is too small")
			}

			// Same tickets.Purchase core the online checkout
			// (buyGasSubmitHandler, cmd/server/tickets.go) goes through --
			// CreatePaidCash is just the 'paid'-immediately,
			// channel='cash' entry point into it.
			t, err := tickets.CreatePaidCash(ctx, q, tickets.PurchaseParams{
				PlantID:       plant.ID,
				PlantSlug:     plant.Slug,
				PriceID:       priceID,
				RatePerKgKobo: pricePerKg,
				AmountKobo:    amountKobo,
				SizeGrams:     sizeGrams,
				CustomerPhone: phone,
			}, actor.UserID, *actor.ShiftID)
			if err != nil {
				return err
			}
			ticket = t
			assignedPump, err := pumps.AssignLeastBusy(ctx, q, plant.ID, t.ID)
			if err != nil {
				return err
			}
			pumpName = assignedPump.Name

			if _, err := q.Exec(ctx, `
				INSERT INTO cash_movements (plant_id, shift_id, kind, amount_kobo, ticket_id, recorded_by, note)
				VALUES ($1, $2, 'sale', $3, $4, $5, 'Cash sale at terminal')
			`, plant.ID, *actor.ShiftID, amountKobo, t.ID, actor.UserID); err != nil {
				return err
			}

			if err := audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.cash_sale", ticket.Reference, map[string]any{
				"amount_kobo": amountKobo,
				"size_grams":  sizeGrams,
				"pump":        assignedPump.Name,
			}); err != nil {
				return err
			}

			cashHeldKobo, err = shiftCashHeld(ctx, q, plant.ID, *actor.ShiftID)
			return err
		})
		if err != nil {
			if errors.Is(err, errPlantClosed) {
				writeJSONError(w, http.StatusForbidden, "Plant is currently closed")
				return
			}
			log.Println("terminal cash sale: failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not record cash sale")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ticket_id":      ticket.ID,
			"code":           ticket.Code,
			"amount":         formatNaira(amountKobo),
			"size_kg":        formatKg(int64(sizeGrams)),
			"rate":           formatNaira(pricePerKg) + "/kg",
			"pump":           pumpName,
			"cash_held_kobo": cashHeldKobo,
		})
	}
}

func terminalPumpHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if actor.Role != session.RoleManager {
			writeJSONError(w, http.StatusForbidden, "you do not have permission to manage pumps")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}

		var id *uuid.UUID
		if rawID := strings.TrimSpace(r.FormValue("pump_id")); rawID != "" {
			parsed, err := uuid.Parse(rawID)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid pump")
				return
			}
			id = &parsed
		}
		name := strings.TrimSpace(r.FormValue("name"))
		status := strings.TrimSpace(r.FormValue("status"))
		sortOrder, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))

		var list []pumps.Pump
		var savedID uuid.UUID
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := terminalManagerCan(ctx, q, plant.ID, actor.UserID, "can_manage_pumps"); err != nil {
				return err
			}
			var err error
			savedID, err = pumps.Upsert(ctx, q, plant.ID, id, name, status, sortOrder)
			if err != nil {
				return err
			}
			if err := audit.Log(ctx, q, &plant.ID, &actor.UserID, "pump.updated", savedID.String(), map[string]any{
				"name":       name,
				"status":     status,
				"sort_order": sortOrder,
				"source":     "terminal",
			}); err != nil {
				return err
			}
			list, err = pumps.List(ctx, q, plant.ID)
			return err
		})
		if errors.Is(err, errTerminalForbidden) {
			writeJSONError(w, http.StatusForbidden, "you do not have permission to manage pumps")
			return
		}
		if err != nil {
			log.Println("terminal pump update:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not save pump")
			return
		}

		items := make([]map[string]any, 0, len(list))
		for _, p := range list {
			items = append(items, map[string]any{
				"id":         p.ID.String(),
				"name":       p.Name,
				"status":     p.Status,
				"sort_order": p.SortOrder,
				"busy_count": p.BusyCount,
				"open_shift": p.OpenShift,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pumps": items})
	}
}

func terminalStaffStatusHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if actor.Role != session.RoleManager {
			writeJSONError(w, http.StatusForbidden, "you do not have permission to manage staff")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		userID, err := uuid.Parse(strings.TrimSpace(r.FormValue("user_id")))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid staff member")
			return
		}
		if userID == actor.UserID {
			writeJSONError(w, http.StatusBadRequest, "you cannot deactivate yourself")
			return
		}
		active := r.FormValue("active") == "true"

		var rows []terminalManageStaff
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := terminalManagerCan(ctx, q, plant.ID, actor.UserID, "can_manage_staff"); err != nil {
				return err
			}
			if err := staffsvc.SetActive(ctx, q, plant.ID, userID, active); err != nil {
				return err
			}
			action := "staff.deactivated"
			if active {
				action = "staff.activated"
			}
			if err := audit.Log(ctx, q, &plant.ID, &actor.UserID, action, userID.String(), map[string]any{"source": "terminal"}); err != nil {
				return err
			}
			var err error
			rows, err = listTerminalManageStaff(ctx, q, plant.ID)
			return err
		})
		if errors.Is(err, errTerminalForbidden) {
			writeJSONError(w, http.StatusForbidden, "you do not have permission to manage staff")
			return
		}
		if err != nil {
			log.Println("terminal staff status:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not update staff")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "manage_staff": rows})
	}
}

func shiftCashHeld(ctx context.Context, q tenantdb.Querier, plantID, shiftID uuid.UUID) (int64, error) {
	_, _, cashHeldKobo, err := shifts.Summary(ctx, q, plantID, shiftID)
	return cashHeldKobo, err
}

func terminalPumpQueueHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.PumpID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in to a pump")
			return
		}
		var rows []tickets.PumpQueueTicket
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			var err error
			rows, err = tickets.ListDispensingForPump(ctx, q, plant.ID, *actor.PumpID)
			return err
		})
		if err != nil {
			log.Println("terminal pump queue:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not load pump receipts")
			return
		}
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]any{
				"ticket_id": row.ID.String(),
				"code":      row.Code,
				"initials":  row.Initials,
				"size_kg":   formatKg(int64(row.SizeGrams)),
				"amount":    formatNaira(row.AmountKobo),
				"channel":   channelLabel(row.Channel),
				"started":   row.StartedAt.Format("15:04"),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"tickets": items})
	}
}

func terminalCloseReceiptHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		ticketID, err := uuid.Parse(r.FormValue("ticket_id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid ticket")
			return
		}
		note := strings.TrimSpace(r.FormValue("note"))

		var fillsCompleted int
		var gramsDispensed int64
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := tickets.CloseFilled(ctx, q, plant.ID, ticketID, *actor.ShiftID, actor.UserID, note); err != nil {
				return err
			}
			if err := audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.filled", ticketID.String(), map[string]any{"note": note}); err != nil {
				return err
			}
			var err error
			fillsCompleted, gramsDispensed, _, err = shifts.Summary(ctx, q, plant.ID, *actor.ShiftID)
			return err
		})
		if err != nil {
			log.Println("terminal close receipt:", err)
			writeJSONError(w, http.StatusBadRequest, "could not close receipt")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fills_completed": fillsCompleted, "grams_dispensed": gramsDispensed})
	}
}

func terminalBulkCloseReceiptsHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil || actor.PumpID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in to a pump")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		note := strings.TrimSpace(r.FormValue("note"))

		var count int64
		var fillsCompleted int
		var gramsDispensed int64
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			var err error
			count, err = tickets.BulkCloseFilled(ctx, q, plant.ID, *actor.PumpID, *actor.ShiftID, actor.UserID, note)
			if err != nil {
				return err
			}
			if err := audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.bulk_filled", actor.PumpID.String(), map[string]any{"count": count, "note": note}); err != nil {
				return err
			}
			fillsCompleted, gramsDispensed, _, err = shifts.Summary(ctx, q, plant.ID, *actor.ShiftID)
			return err
		})
		if err != nil {
			log.Println("terminal bulk close receipts:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not close receipts")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count, "fills_completed": fillsCompleted, "grams_dispensed": gramsDispensed})
	}
}

func terminalSetPriceHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if actor.Role != session.RoleManager {
			writeJSONError(w, http.StatusForbidden, "you do not have permission to set prices")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		priceNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("price_per_kg")), 64)
		if err != nil || priceNaira <= 0 {
			writeJSONError(w, http.StatusBadRequest, "enter a valid price")
			return
		}
		priceKobo := int64(math.Round(priceNaira * 100))

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := terminalManagerCan(ctx, q, plant.ID, actor.UserID, "can_edit_price"); err != nil {
				return err
			}
			priceID, err := prices.Set(ctx, q, plant.ID, priceKobo, actor.UserID)
			if err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "price.set", priceID.String(), map[string]any{
				"price_per_kg_kobo": priceKobo,
				"source":            "terminal",
			})
		})
		if errors.Is(err, errTerminalForbidden) {
			writeJSONError(w, http.StatusForbidden, "you do not have permission to set prices")
			return
		}
		if err != nil {
			log.Println("terminal set price:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not save price")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "price_per_kg_kobo": priceKobo, "price": formatNaira(priceKobo) + "/kg"})
	}
}

func terminalLogoutHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		_ = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "terminal.logged_out", "", map[string]any{
				"shift_id": actor.ShiftID,
			})
		})
		_ = s.Sessions.Delete(r.Context(), actor.Token)
		session.ClearCookie(w, session.CookieStaff)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func terminalShiftEndHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		closingCashNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("closing_cash")), 64)
		if err != nil || closingCashNaira < 0 {
			writeJSONError(w, http.StatusBadRequest, "enter the closing cash amount")
			return
		}
		closingCashKobo := int64(math.Round(closingCashNaira * 100))
		notes := strings.TrimSpace(r.FormValue("notes"))

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := shifts.Close(ctx, q, plant.ID, *actor.ShiftID, actor.UserID, closingCashKobo, notes); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "shift.closed", actor.ShiftID.String(), map[string]any{
				"closing_cash_kobo": closingCashKobo,
			})
		})
		if err != nil {
			log.Println("terminal shift end: failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not close shift")
			return
		}

		// The session is over the instant the shift is -- delete it
		// outright rather than let it linger until StaffBackstopTTL
		// lapses (see session.StaffBackstopTTL's doc comment).
		_ = s.Sessions.Delete(r.Context(), actor.Token)
		session.ClearCookie(w, session.CookieStaff)

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
