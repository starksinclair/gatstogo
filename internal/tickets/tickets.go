// Package tickets implements the ticket write paths: creation, marking
// paid, and voiding. tickets is the schema's central transaction table
// (migrations/0001_init_schema.up.sql) but, before this build-out, nothing
// anywhere ever inserted or updated a row in it -- every owner/admin view
// of ticket data was read-only.
package tickets

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// NewReference mints a ticket reference that's unique across the whole
// platform, not just within one plant. tickets.reference is only
// UNIQUE(plant_id, reference) in the schema, but the Paystack webhook has
// no other way to know which tenant a payment belongs to (see
// LookupPlantByReference below) -- so it needs to be globally unique in
// practice, which a per-plant-unique value alone wouldn't guarantee.
// Prefixing with the plant's slug keeps it human-readable and still
// trivially satisfies the schema's actual constraint.
func NewReference(plantSlug string) (string, error) {
	suffix, err := randomHex(6)
	if err != nil {
		return "", err
	}
	return sanitizeReferenceSegment(plantSlug) + "-" + suffix, nil
}

// sanitizeReferenceSegment strips everything except what Paystack's API
// actually allows in a transaction reference: alphanumeric characters,
// '-', '.', and '=' (confirmed against Paystack's docs). Nothing in the
// schema constrains plants.slug's character set, so this guards against a
// slug containing anything else -- a space, an underscore, non-ASCII --
// turning into a reference Paystack's Initialize API would reject
// outright. Slugs themselves should also be validated at creation time
// (plant onboarding, M11) since they're used in subdomain routing too
// (which is more restrictive than this), but this is a cheap, independent
// backstop specifically for what this package sends to Paystack.
func sanitizeReferenceSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '=':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "plant" // slug sanitized down to nothing -- fall back rather than produce an empty segment
	}
	return b.String()
}

// NewCode generates a 4-digit numeric redemption code -- the thing a
// customer reads aloud to a staff terminal to authorize a fill.
func NewCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", n.Int64()), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// Ticket is the subset of a tickets row a caller needs back after
// creating one.
type Ticket struct {
	ID        uuid.UUID
	Reference string
	Code      string
}

// CreatePendingParams are the fields needed to insert a new pending
// ticket. RatePerKgKobo/PriceID are a snapshot of the price in effect at
// creation time -- prices are immutable history, so a later price change
// must never retroactively change what an in-flight ticket charges.
type CreatePendingParams struct {
	PlantID       uuid.UUID
	PlantSlug     string
	PriceID       uuid.UUID
	RatePerKgKobo int64
	AmountKobo    int64
	SizeGrams     int
	// Channel must be one of the tickets.channel check constraint's
	// values: transfer, terminal, ussd, cash.
	Channel       string
	CustomerName  string
	CustomerPhone string
}

const maxCodeAttempts = 6

// CreatePending inserts a new ticket in 'pending' status, retrying with a
// freshly generated 4-digit code on the rare event of a collision against
// another currently-live ticket at this plant. idx_tickets_open_code (0001)
// is a partial unique index on (plant_id, code) WHERE status IN ('pending',
// 'paid') -- a code only has to be unique among tickets that are still
// redeemable, not for all time, so a collision is expected to be rare but
// not impossible.
func CreatePending(ctx context.Context, q tenantdb.Querier, p CreatePendingParams) (*Ticket, error) {
	reference, err := NewReference(p.PlantSlug)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt < maxCodeAttempts; attempt++ {
		code, err := NewCode()
		if err != nil {
			return nil, err
		}

		row := q.QueryRow(ctx, `
			INSERT INTO tickets (
				plant_id, reference, code, customer_name, customer_phone,
				size_grams, rate_per_kg, price_id, amount, channel, status
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'pending')
			RETURNING id
		`, p.PlantID, reference, code, nullIfEmpty(p.CustomerName), nullIfEmpty(p.CustomerPhone),
			p.SizeGrams, p.RatePerKgKobo, p.PriceID, p.AmountKobo, p.Channel)

		var id uuid.UUID
		switch scanErr := row.Scan(&id); {
		case scanErr == nil:
			return &Ticket{ID: id, Reference: reference, Code: code}, nil
		case isUniqueViolation(scanErr):
			continue // code collision against another live ticket -- try another code
		default:
			return nil, scanErr
		}
	}
	return nil, errors.New("tickets: could not allocate a unique redemption code after several attempts")
}

// MarkPaid transitions a ticket from pending to paid. Idempotent by
// design: the Paystack webhook and the customer's browser landing on the
// callback URL both race to confirm the same payment (that's expected --
// see cmd/server/tickets.go), so a second call for an already-paid (or
// already-filled) ticket is a no-op that still reports paid=true. A
// ticket in any other state (voided, expired) refuses and reports
// paid=false rather than overwriting a terminal state.
func MarkPaid(ctx context.Context, q tenantdb.Querier, plantID, ticketID uuid.UUID) (paid bool, err error) {
	var status string
	if err := q.QueryRow(ctx, `SELECT status FROM tickets WHERE id = $1 AND plant_id = $2`, ticketID, plantID).Scan(&status); err != nil {
		return false, err
	}
	switch status {
	case "paid", "filled":
		return true, nil
	case "pending":
		if _, err := q.Exec(ctx, `UPDATE tickets SET status = 'paid', paid_at = now() WHERE id = $1 AND plant_id = $2`, ticketID, plantID); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

// Void marks a pending or paid ticket voided. Returns an error if the
// ticket doesn't exist or is already in a terminal state (filled,
// expired, already voided) that shouldn't be overwritten.
func Void(ctx context.Context, q tenantdb.Querier, plantID, ticketID, voidedBy uuid.UUID, reason string) error {
	tag, err := q.Exec(ctx, `
		UPDATE tickets
		SET status = 'voided', void_reason = $3, voided_by = $4, voided_at = now()
		WHERE id = $1 AND plant_id = $2 AND status IN ('pending', 'paid')
	`, ticketID, plantID, reason, voidedBy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("tickets: not found, or not in a voidable state")
	}
	return nil
}

// LookupPlantByReference finds which plant a ticket reference belongs to.
// Must run on a connection that can see across every plant (AdminDB) --
// there's no plant_id yet to scope a tenantdb.WithTenant transaction to,
// since discovering the plant_id is the whole point of this call. This
// mirrors why middleware.Tenant's own slug->plant lookup runs on AdminDB
// rather than the restricted pool.
func LookupPlantByReference(ctx context.Context, q tenantdb.Querier, reference string) (plantID uuid.UUID, ticketID uuid.UUID, err error) {
	err = q.QueryRow(ctx, `SELECT plant_id, id FROM tickets WHERE reference = $1`, reference).Scan(&plantID, &ticketID)
	return plantID, ticketID, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func nullIfEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
