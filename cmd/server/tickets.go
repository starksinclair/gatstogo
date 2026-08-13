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
// "POS terminal" doesn't correspond to an online Paystack channel -- a POS
// terminal is a physical card machine, a different product from the
// Checkout/Initialize API this app calls. The closest available online
// equivalent is a card charge, so that customer-facing option is
// deliberately wired to Paystack's "card" channel while still storing the
// schema's existing 'terminal' value; the label in the UI is unchanged.
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

// ticketEmail synthesizes a placeholder address: Paystack's Initialize API
// requires an email, but this app's customer flow only ever collects a
// name and phone number -- a standard workaround for phone-first markets.
func ticketEmail(reference string) string {
	return reference + "@no-reply.gatstogo.invalid"
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
			http.Error(w, "Plant not found", http.StatusNotFound)
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
			sizeGrams := int(math.Round(float64(amountKobo) * 1000 / float64(pricePerKg)))
			if sizeGrams <= 0 {
				return fmt.Errorf("amount too small to buy any gas at the current price")
			}

			created, err := tickets.CreatePending(ctx, q, tickets.CreatePendingParams{
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

		callbackURL := requestScheme(r) + "://" + r.Host + "/tickets/" + ticket.Reference + "/callback"
		init, err := s.Paystack.Initialize(r.Context(), payments.InitializeParams{
			Email:       ticketEmail(ticket.Reference),
			AmountKobo:  amountKobo,
			Reference:   ticket.Reference,
			CallbackURL: callbackURL,
			Channels:    paystackChannels,
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
			http.Error(w, "Plant not found", http.StatusNotFound)
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

			if verifyErr != nil || verify.Status != "success" || verify.AmountKobo != amountKobo {
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

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plantID, func(ctx context.Context, q tenantdb.Querier) error {
			var amountKobo int64
			if err := q.QueryRow(ctx, `SELECT amount FROM tickets WHERE id = $1 AND plant_id = $2`, ticketID, plantID).Scan(&amountKobo); err != nil {
				return err
			}
			if event.Data.Amount != amountKobo {
				log.Printf("paystack webhook: amount mismatch for %s: webhook=%d ticket=%d", event.Data.Reference, event.Data.Amount, amountKobo)
				return nil // don't mark paid, but don't ask Paystack to retry a mismatch either
			}

			ok, err := tickets.MarkPaid(ctx, q, plantID, ticketID)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			return audit.Log(ctx, q, &plantID, nil, "ticket.paid", event.Data.Reference, map[string]any{"via": "webhook"})
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
