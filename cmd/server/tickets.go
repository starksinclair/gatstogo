package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"gatstogo/internal/audit"
	"gatstogo/internal/middleware"
	"gatstogo/internal/payments"
	"gatstogo/internal/tenantdb"
	"gatstogo/internal/tickets"
	"gatstogo/web/templates/pages"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// loadCurrentPrice returns the plant's latest price row -- the same query
// the owner dashboard already uses for its "Current price" tile, now also
// the one source of truth the customer buy form and ticket creation read
// from, instead of the hardcoded ₦1,150/kg the buy form used to show
// regardless of what was actually in the prices table.
func loadCurrentPrice(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID) (priceID uuid.UUID, pricePerKgKobo int64, err error) {
	err = q.QueryRow(ctx, `
		SELECT id, price_per_kg FROM prices WHERE plant_id = $1 ORDER BY effective_from DESC LIMIT 1
	`, plantID).Scan(&priceID, &pricePerKgKobo)
	return priceID, pricePerKgKobo, err
}

// mapCustomerChannel translates the customer-facing payment option into
// (a) the tickets.channel value to store, which must be one of the
// schema's check-constraint values (transfer/terminal/ussd/cash), and
// (b) the Paystack channel(s) to request for that option.
//
// A physical POS terminal isn't something an online Initialize Transaction
// call can trigger, so the closest available equivalent is a card charge:
// that customer-facing option is wired to Paystack's "card" channel while
// still storing the schema's existing 'terminal' value. The UI label for
// this option is "Card" (see channelLabel), matching what actually happens
// rather than the schema's internal column value.
func mapCustomerChannel(input string) (dbChannel string, paystackChannels []string, ok bool) {
	switch input {
	case "transfer":
		return "transfer", []string{"bank_transfer"}, true
	case "terminal":
		return "terminal", []string{"card"}, true
	case "ussd":
		return "ussd", []string{"ussd"}, true
	default:
		return "", nil, false
	}
}

// channelLabel turns a tickets.channel DB value into the human label shown
// to customers (receipt detail) and owners (admin ticket list). This is
// deliberately not the generic humanize() helper: humanize's naive
// underscore-strip-and-title-case would render 'terminal' as "Terminal"
// (not what actually happens -- see mapCustomerChannel) and 'ussd' as
// "Ussd" (not the acronym). Any value outside the schema's check
// constraint falls back to humanize for forward-compatibility rather than
// silently showing nothing.
func channelLabel(dbChannel string) string {
	switch dbChannel {
	case "transfer":
		return "Bank transfer"
	case "terminal":
		return "Card"
	case "ussd":
		return "USSD"
	case "cash":
		return "Cash"
	default:
		return humanize(dbChannel, "Payment channel")
	}
}

// ticketEmail builds a per-ticket email address for Paystack's Initialize
// API, which requires one even though this app's customer flow only ever
// collects a name and phone number. baseEmail is GatsToGo's own real inbox
// (Server.NotificationEmail, from TICKET_NOTIFICATION_EMAIL) -- ticketEmail
// tags it with the ticket reference using Gmail-style "+tag" addressing
// (localpart+tag@domain), so every transaction is distinguishable if
// Paystack ever sends a receipt there, while mail still lands in that one
// real, controlled inbox.
//
// This replaced a synthetic reference@example.com placeholder: a live test
// against the real Paystack API (internal/payments/live_test.go) caught
// that Paystack's own email validation rejects obviously-fake domains
// outright -- specifically .invalid, with "Invalid Email Address Passed"
// (HTTP 400) -- something a mocked test could never catch, since the mock
// never validated the email format at all. Every real ticket purchase
// would have failed at the Initialize step with that domain. Using a real,
// deliverable address sidesteps the question of which placeholder domains
// happen to pass Paystack's validator.
func ticketEmail(baseEmail, reference string) string {
	at := strings.Index(baseEmail, "@")
	if at < 0 {
		return baseEmail // malformed base -- fall back to it verbatim rather than producing something worse
	}
	return baseEmail[:at] + "+" + reference + baseEmail[at:]
}

// requestScheme reports "https" if this request arrived over TLS or a
// trusted reverse proxy says it did, "http" otherwise. Used only to build
// the Paystack callback URL against the tenant subdomain the request
// actually came in on (APP_BASE_URL isn't usable here -- it's one fixed
// value and can't represent every plant's subdomain).
func requestScheme(r *http.Request) string {
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return "https"
	}
	return "http"
}

// ---- POST /tickets (public: customer buy-gas form) ----

func buyGasSubmitHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/?error=form", http.StatusSeeOther)
			return
		}

		amountNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
		if err != nil || amountNaira <= 0 {
			http.Redirect(w, r, "/?error=amount", http.StatusSeeOther)
			return
		}
		amountKobo := int64(math.Round(amountNaira * 100))

		name := strings.TrimSpace(r.FormValue("name"))
		phone := strings.TrimSpace(r.FormValue("phone"))
		if name == "" || phone == "" {
			http.Redirect(w, r, "/?error=details", http.StatusSeeOther)
			return
		}

		dbChannel, paystackChannels, ok := mapCustomerChannel(r.FormValue("channel"))
		if !ok {
			http.Redirect(w, r, "/?error=channel", http.StatusSeeOther)
			return
		}

		var ticket *tickets.Ticket
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			priceID, pricePerKg, err := loadCurrentPrice(ctx, q, plant.ID)
			if err != nil {
				return fmt.Errorf("load current price: %w", err)
			}
			if pricePerKg <= 0 {
				return fmt.Errorf("invalid current price for plant")
			}
			sizeGrams := tickets.GramsFromAmount(amountKobo, pricePerKg)
			if sizeGrams <= 0 {
				return fmt.Errorf("amount too small to buy any gas at the current price")
			}

			created, err := tickets.CreatePending(ctx, q, tickets.PurchaseParams{
				PlantID:       plant.ID,
				PlantSlug:     plant.Slug,
				PriceID:       priceID,
				RatePerKgKobo: pricePerKg,
				AmountKobo:    amountKobo,
				SizeGrams:     sizeGrams,
				Channel:       dbChannel,
				CustomerName:  name,
				CustomerPhone: phone,
			})
			if err != nil {
				return err
			}
			ticket = created

			return audit.Log(ctx, q, &plant.ID, nil, "ticket.created", ticket.Reference, map[string]any{
				"amount_kobo": amountKobo,
				"size_grams":  sizeGrams,
				"channel":     dbChannel,
			})
		})
		if err != nil {
			log.Println("buy gas: create ticket failed:", err)
			http.Redirect(w, r, "/?error=server", http.StatusSeeOther)
			return
		}

		// Subaccount is left empty (no split payment) for a plant with no
		// paystack_subaccount_code yet -- every dev/test plant seeded
		// before real settlement existed. Initialize itself only includes
		// subaccount/bearer in the request when Subaccount is actually
		// set, so this is a no-op for those plants, not a broken request.
		var subaccount string
		if plant.PaystackSubaccountCode != nil {
			subaccount = *plant.PaystackSubaccountCode
		}

		callbackURL := requestScheme(r) + "://" + r.Host + "/tickets/" + ticket.Reference + "/callback"
		init, err := s.Paystack.Initialize(r.Context(), payments.InitializeParams{
			Email:       ticketEmail(s.NotificationEmail, ticket.Reference),
			AmountKobo:  amountKobo,
			Reference:   ticket.Reference,
			CallbackURL: callbackURL,
			Channels:    paystackChannels,
			Subaccount:  subaccount,
			// "subaccount" -- the plant absorbs Paystack's own processing
			// fee from its share, per the business decision recorded in
			// internal/plants.CreateParams's own doc comment. Only takes
			// effect when Subaccount is actually set (see Initialize).
			Bearer: "subaccount",
		})
		if err != nil {
			// The ticket row already exists (status 'pending') even though
			// checkout couldn't be started -- it just sits unpaid and is
			// never redeemable, no cleanup required.
			log.Println("buy gas: paystack initialize failed:", err)
			http.Redirect(w, r, "/?error=payment", http.StatusSeeOther)
			return
		}

		http.Redirect(w, r, init.AuthorizationURL, http.StatusSeeOther)
	}
}

// ---- GET /tickets/{reference}/callback (public: browser returns from Paystack) ----

func ticketCallbackHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		reference := chi.URLParam(r, "reference")

		verify, verifyErr := s.Paystack.Verify(r.Context(), reference)

		var (
			code, amountLabel, sizeLabel string
			paid                         bool
		)
		txErr := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			var (
				ticketID   uuid.UUID
				amountKobo int64
				sizeGrams  int
			)
			if err := q.QueryRow(ctx, `
				SELECT id, code, amount, size_grams FROM tickets WHERE plant_id = $1 AND reference = $2
			`, plant.ID, reference).Scan(&ticketID, &code, &amountKobo, &sizeGrams); err != nil {
				return err
			}
			amountLabel = formatNaira(amountKobo)
			sizeLabel = formatKg(int64(sizeGrams))

			if verifyErr != nil || verify.Status != "success" || verify.AmountKobo != amountKobo || (verify.Currency != "" && verify.Currency != "NGN") {
				// Not confirmed from here -- leave it pending. The webhook
				// (cmd/server: paystackWebhookHandler) is the primary
				// confirmation path and may simply not have arrived yet.
				return nil
			}

			ok, err := tickets.MarkPaid(ctx, q, plant.ID, ticketID)
			if err != nil {
				return err
			}
			paid = ok
			if !ok {
				return nil
			}
			return audit.Log(ctx, q, &plant.ID, nil, "ticket.paid", reference, map[string]any{"via": "callback"})
		})
		if txErr != nil {
			log.Println("ticket callback: failed:", txErr)
			http.Error(w, "Something went wrong confirming your payment.", http.StatusInternalServerError)
			return
		}

		if err := pages.TicketConfirmation(plant, paid, code, amountLabel, sizeLabel).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// ---- POST /webhooks/paystack (server-to-server, signature-verified) ----

func paystackWebhookHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Signature check must run over the exact raw bytes, before any
		// JSON parsing -- that's what Paystack actually signed.
		if !s.Paystack.VerifyWebhookSignature(body, r.Header.Get("X-Paystack-Signature")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var event payments.WebhookEvent
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if event.Event != "charge.success" {
			// Nothing to do for other event types -- 200 so Paystack
			// doesn't retry a webhook there was never anything to act on.
			w.WriteHeader(http.StatusOK)
			return
		}

		plantID, ticketID, err := tickets.LookupPlantByReference(r.Context(), s.AdminDB, event.Data.Reference)
		if err != nil {
			log.Println("paystack webhook: unknown reference:", event.Data.Reference, err)
			w.WriteHeader(http.StatusOK) // not a ticket of ours -- nothing to retry
			return
		}

		// The signature check above proves this request genuinely came
		// from Paystack and wasn't tampered with in transit, but Paystack's
		// own guidance is to still independently re-confirm via the Verify
		// API before granting value, rather than trust the webhook body's
		// own status/amount fields at face value. A network error here
		// returns 5xx (below) so Paystack retries -- this is the "genuine
		// processing failure" case, not a data mismatch retrying wouldn't
		// fix.
		verify, err := s.Paystack.Verify(r.Context(), event.Data.Reference)
		if err != nil {
			log.Println("paystack webhook: verify call failed:", err)
			http.Error(w, "could not confirm transaction", http.StatusInternalServerError)
			return
		}

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plantID, func(ctx context.Context, q tenantdb.Querier) error {
			var amountKobo int64
			if err := q.QueryRow(ctx, `SELECT amount FROM tickets WHERE id = $1 AND plant_id = $2`, ticketID, plantID).Scan(&amountKobo); err != nil {
				return err
			}
			if verify.Status != "success" || verify.AmountKobo != amountKobo || (verify.Currency != "" && verify.Currency != "NGN") {
				log.Printf("paystack webhook: verify mismatch for %s: status=%s verify_amount=%d ticket_amount=%d currency=%s",
					event.Data.Reference, verify.Status, verify.AmountKobo, amountKobo, verify.Currency)
				return nil // don't mark paid, but don't ask Paystack to retry a mismatch either
			}

			ok, err := tickets.MarkPaid(ctx, q, plantID, ticketID)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			return audit.Log(ctx, q, &plantID, nil, "ticket.paid", event.Data.Reference, map[string]any{
				"via":              "webhook",
				"gateway_response": verify.GatewayResponse,
			})
		})
		if err != nil {
			log.Println("paystack webhook: processing failed:", err)
			// Non-2xx here (unlike the branches above) is deliberate: this
			// is a transient/unexpected failure (DB error, etc.), and
			// Paystack retrying the webhook with backoff is the desired
			// behavior, not something to suppress.
			http.Error(w, "processing failed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

// ---- POST /tickets/{reference}/void (owner/manager session) ----

func ticketVoidHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner", http.StatusSeeOther)
			return
		}
		reference := chi.URLParam(r, "reference")
		reason := strings.TrimSpace(r.FormValue("reason"))
		if reason == "" {
			reason = "Voided by owner"
		}

		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			var ticketID uuid.UUID
			if err := q.QueryRow(ctx, `SELECT id FROM tickets WHERE plant_id = $1 AND reference = $2`, plant.ID, reference).Scan(&ticketID); err != nil {
				return err
			}
			if err := tickets.Void(ctx, q, plant.ID, ticketID, actor.UserID, reason); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.voided", reference, map[string]any{"reason": reason})
		})
		if err != nil {
			log.Println("void ticket: failed:", err)
		}
		http.Redirect(w, r, "/owner", http.StatusSeeOther)
	}
}
