package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"gatstogo/internal/audit"
	"gatstogo/internal/csrf"
	"gatstogo/internal/domain"
	"gatstogo/internal/middleware"
	"gatstogo/internal/receipts"
	"gatstogo/internal/session"
	"gatstogo/internal/tenantdb"
	"gatstogo/internal/tickets"
	"gatstogo/web/templates/pages"

	"github.com/go-chi/chi/v5"
)

// receiptsErrorMessage maps the short ?error= code RequireReceiptSession
// (session expired) and receiptsConfirmHandler (confirm failed) redirect
// back to /receipts with, to a human-readable banner. An unknown/absent
// code renders no banner -- the same convention buyGasErrorMessage,
// ownerErrorMessage, and adminErrorMessage already use.
func receiptsErrorMessage(code string) string {
	switch code {
	case "session":
		return "Your session expired. Enter your phone number again."
	case "confirm":
		return "Could not confirm that receipt. It may not be ready to confirm yet."
	default:
		return ""
	}
}

// receiptStatusLabel and receiptBadgeCSS turn a tickets.status value (plus
// whether confirmed_at is set) into what the receipts page actually shows
// a customer -- "Awaiting fill"/"Ready to confirm"/"Received" reads far
// better to a customer than the raw status column, and confirmed is its
// own extra bit of state layered on top of 'filled' rather than a status
// of its own (see migrations/0001_init_schema.up.sql's tickets.status
// check constraint, which has no 'confirmed' value).
func receiptStatusLabel(status string, confirmed bool) string {
	switch status {
	case "pending":
		return "Awaiting payment"
	case "paid":
		return "Awaiting fill"
	case "filled":
		if confirmed {
			return "Received"
		}
		return "Ready to confirm"
	case "expired":
		return "Expired"
	case "voided":
		return "Voided"
	default:
		return humanize(status, "Unknown")
	}
}

// displayPhone puts the leading 0 back on a NormalizePhone'd 10-digit
// canonical number for display -- "8030000000" -> "08030000000", the
// standard local format a Nigerian customer actually expects to see. Only
// used for the authenticated list view (cmd/server/receipts.go's
// receiptsPageHandler), where all that's on hand is the canonical form
// from the session; the lookup/verify views instead echo back whatever
// the customer literally typed, which needs no reformatting.
func displayPhone(normalized string) string {
	if len(normalized) == 10 {
		return "0" + normalized
	}
	return normalized
}

func receiptBadgeCSS(status string, confirmed bool) string {
	switch status {
	case "filled":
		if confirmed {
			return "badge-success"
		}
		return "badge-pending"
	case "paid", "pending":
		return "badge-pending"
	case "expired", "voided":
		return "badge-danger"
	default:
		return "badge-pending"
	}
}

// loadReceiptSession is the same check RequireReceiptSession does, run
// directly (not as middleware) because GET /receipts must render
// successfully for anonymous visitors too -- it's simultaneously the
// phone-entry page and, once a valid session cookie exists, the ticket
// list. token is returned alongside phone so the caller can derive the
// same session-based CSRF token RequireReceiptSession's Actor.Token would
// give the confirm form.
func loadReceiptSession(r *http.Request, s *Server, plant *domain.Plant) (phone, token string, ok bool) {
	tok, has := session.ReadCookie(r)
	if !has {
		return "", "", false
	}
	data, err := s.Sessions.Load(r.Context(), tok)
	if err != nil {
		return "", "", false
	}
	if data.Role != session.RoleCustomer || data.PlantID != plant.ID.String() || data.Phone == "" {
		return "", "", false
	}

	// Sliding expiry, same as RequireReceiptSession's own Touch call --
	// without this, simply browsing the receipts list (a GET, so never
	// reaching that middleware) wouldn't count as activity and a customer
	// reading through several old receipts could get logged out mid-visit.
	_ = s.Sessions.Touch(r.Context(), tok, session.ReceiptSessionTTL)

	return data.Phone, tok, true
}

// toReceiptDetail converts a tickets.Receipt (the DB row) into the view
// model customer_home.templ actually renders -- the same conversion
// loadOwnerTickets already does for the owner dashboard's ticket table,
// just for the customer-facing receipts page instead.
func toReceiptDetail(rc *tickets.Receipt, sessionToken string) *pages.ReceiptDetail {
	confirmed := rc.ConfirmedAt != nil

	netKg := "Awaiting fill"
	if rc.NetGrams != nil && *rc.NetGrams > 0 {
		netKg = formatKg(int64(*rc.NetGrams))
	}

	confirmedAt := ""
	if rc.ConfirmedAt != nil {
		confirmedAt = rc.ConfirmedAt.Format("2 Jan, 15:04")
	}

	csrfToken, _ := csrf.Token(sessionToken)

	return &pages.ReceiptDetail{
		Reference:     rc.Reference,
		Code:          rc.Code,
		StatusLabel:   receiptStatusLabel(rc.Status, confirmed),
		BadgeCSS:      receiptBadgeCSS(rc.Status, confirmed),
		Kg:            formatKg(int64(rc.SizeGrams)),
		NetKg:         netKg,
		Rate:          formatNaira(rc.RatePerKgKobo) + "/kg",
		Amount:        formatNaira(rc.AmountKobo),
		Channel:       channelLabel(rc.Channel),
		When:          rc.CreatedAt.Format("2 Jan, 15:04"),
		ConfirmedAt:   confirmedAt,
		CanConfirm:    rc.Status == "filled" && !confirmed,
		ConfirmAction: "/tickets/" + rc.Reference + "/confirm",
		CSRFToken:     csrfToken,
	}
}

// ---- GET /receipts (public: phone entry, or the ticket list/detail if a receipts session exists) ----

func receiptsPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}

		csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
		errorMsg := receiptsErrorMessage(r.URL.Query().Get("error"))

		phone, sessionToken, ok := loadReceiptSession(r, s, plant)
		if !ok {
			if err := pages.CustomerReceipts(pages.ReceiptsViewData{
				Plant: plant, View: "lookup", CSRFToken: csrfToken, ErrorMsg: errorMsg,
			}).Render(r.Context(), w); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		data := pages.ReceiptsViewData{Plant: plant, View: "list", CSRFToken: csrfToken, ErrorMsg: errorMsg, Phone: displayPhone(phone)}
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			summaries, err := tickets.ListByPhone(ctx, q, plant.ID, phone)
			if err != nil {
				return err
			}

			selectedRef := r.URL.Query().Get("ticket")
			if selectedRef == "" && len(summaries) > 0 {
				selectedRef = summaries[0].Reference
			}

			data.Tickets = make([]pages.ReceiptListItem, 0, len(summaries))
			for _, sum := range summaries {
				confirmed := sum.ConfirmedAt != nil
				data.Tickets = append(data.Tickets, pages.ReceiptListItem{
					Reference:   sum.Reference,
					Href:        "/receipts?ticket=" + sum.Reference,
					Kg:          formatKg(int64(sum.SizeGrams)),
					Amount:      formatNaira(sum.AmountKobo),
					When:        sum.CreatedAt.Format("2 Jan, 15:04"),
					Selected:    sum.Reference == selectedRef,
					StatusLabel: receiptStatusLabel(sum.Status, confirmed),
					BadgeCSS:    receiptBadgeCSS(sum.Status, confirmed),
				})
			}

			if selectedRef == "" {
				return nil
			}
			receipt, err := tickets.GetReceipt(ctx, q, plant.ID, selectedRef, phone)
			if errors.Is(err, tickets.ErrReceiptNotFound) {
				return nil // e.g. a stale/tampered ?ticket= -- just show the list with nothing selected
			}
			if err != nil {
				return err
			}
			data.Selected = toReceiptDetail(receipt, sessionToken)
			return nil
		})
		if err != nil {
			log.Println("receipts page: query failed:", err)
			http.Error(w, "Could not load receipts", http.StatusInternalServerError)
			return
		}

		if err := pages.CustomerReceipts(data).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// ---- POST /receipts/lookup (public: request a one-time code) ----

func receiptsLookupHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			renderReceiptsLookup(w, r, plant, "Could not read that submission. Try again.")
			return
		}

		rawPhone := strings.TrimSpace(r.FormValue("phone"))
		normalized := tickets.NormalizePhone(rawPhone)
		if len(normalized) != 10 {
			renderReceiptsLookup(w, r, plant, "Enter a valid phone number.")
			return
		}

		code, err := s.Receipts.RequestCode(r.Context(), plant.ID, normalized)
		switch {
		case errors.Is(err, receipts.ErrCooldown):
			// A code was already sent recently -- go straight back to the
			// verify step rather than erroring outright; the customer most
			// likely just double-tapped the button or is asking to resend.
			renderReceiptsVerify(w, r, plant, rawPhone, "A code was already sent -- check your messages, or wait a moment and try resending.")
			return
		case err != nil:
			log.Println("receipts lookup: request code failed:", err)
			renderReceiptsLookup(w, r, plant, "Something went wrong. Try again.")
			return
		}

		// RequestCode never checks whether this phone number actually has
		// any tickets -- deliberately: a lookup form that behaved
		// differently for "no purchases on file" vs. "here's your code"
		// would let anyone probe which phone numbers have bought gas here.
		message := fmt.Sprintf("Your %s receipts code is %s. It expires in 10 minutes.", plant.Name, code)
		if err := s.SMS.Send(r.Context(), rawPhone, message); err != nil {
			// Logged, not fatal: LoggingSender (the only Sender wired up so
			// far) never actually fails, but a future real provider might --
			// the code is already valid in Redis regardless, so still let
			// the customer try entering it.
			log.Println("receipts lookup: sms send failed:", err)
		}

		renderReceiptsVerify(w, r, plant, rawPhone, "")
	}
}

func renderReceiptsLookup(w http.ResponseWriter, r *http.Request, plant *domain.Plant, errorMsg string) {
	csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
	if err := pages.CustomerReceipts(pages.ReceiptsViewData{
		Plant: plant, View: "lookup", CSRFToken: csrfToken, ErrorMsg: errorMsg,
	}).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func renderReceiptsVerify(w http.ResponseWriter, r *http.Request, plant *domain.Plant, phone, errorMsg string) {
	csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
	if err := pages.CustomerReceipts(pages.ReceiptsViewData{
		Plant: plant, View: "verify", CSRFToken: csrfToken, ErrorMsg: errorMsg, Phone: phone,
	}).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---- POST /receipts/verify (public: submit the one-time code) ----

func receiptsVerifyHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			renderReceiptsLookup(w, r, plant, "Could not read that submission. Try again.")
			return
		}

		rawPhone := strings.TrimSpace(r.FormValue("phone"))
		code := strings.TrimSpace(r.FormValue("code"))
		if rawPhone == "" {
			renderReceiptsLookup(w, r, plant, "Enter your phone number.")
			return
		}
		normalized := tickets.NormalizePhone(rawPhone)

		ok, err := s.Receipts.VerifyCode(r.Context(), plant.ID, normalized, code)
		switch {
		case errors.Is(err, receipts.ErrLocked):
			renderReceiptsVerify(w, r, plant, rawPhone, "Too many wrong attempts. Wait a few minutes, then request a new code.")
			return
		case err != nil:
			log.Println("receipts verify: query failed:", err)
			renderReceiptsVerify(w, r, plant, rawPhone, "Something went wrong. Try again.")
			return
		case !ok:
			renderReceiptsVerify(w, r, plant, rawPhone, "That code is incorrect or has expired.")
			return
		}

		token, err := s.Sessions.Create(r.Context(), session.Data{
			Role:    session.RoleCustomer,
			PlantID: plant.ID.String(),
			Phone:   normalized,
		}, session.ReceiptSessionTTL)
		if err != nil {
			log.Println("receipts verify: create session failed:", err)
			renderReceiptsVerify(w, r, plant, rawPhone, "Something went wrong. Try again.")
			return
		}

		session.WriteCookie(w, token, session.ReceiptSessionTTL)
		http.Redirect(w, r, "/receipts", http.StatusSeeOther)
	}
}

// ---- POST /tickets/{reference}/confirm (requires a verified receipts session) ----

func receiptsConfirmHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		reference := chi.URLParam(r, "reference")

		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := tickets.Confirm(ctx, q, plant.ID, reference, actor.Phone); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, nil, "ticket.confirmed", reference, nil)
		})
		if err != nil {
			log.Println("receipts confirm: failed:", err)
			http.Redirect(w, r, "/receipts?ticket="+reference+"&error=confirm", http.StatusSeeOther)
			return
		}

		http.Redirect(w, r, "/receipts?ticket="+reference, http.StatusSeeOther)
	}
}
