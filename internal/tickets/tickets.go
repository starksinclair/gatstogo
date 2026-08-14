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
	"math"
	"math/big"
	"strings"
	"time"
	"unicode"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// AmountFromGrams and GramsFromAmount are the two directions of the same
// price conversion (kobo per kg <-> grams), shared by every purchase path
// -- the customer's online checkout computes grams from a naira amount
// they typed in, the staff terminal's cash sale computes an amount from a
// cylinder size the cashier picked -- so the rounding rule can't quietly
// drift between the two.
func AmountFromGrams(sizeGrams int, pricePerKgKobo int64) int64 {
	return int64(math.Round(float64(sizeGrams) * float64(pricePerKgKobo) / 1000))
}

func GramsFromAmount(amountKobo int64, pricePerKgKobo int64) int {
	return int(math.Round(float64(amountKobo) * 1000 / float64(pricePerKgKobo)))
}

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

const maxCodeAttempts = 6

// PurchaseParams are every field a ticket can be created with, across
// every way this app sells gas. This is the one shared core both purchase
// paths build on:
//   - the customer's online checkout (CreatePending) -- 'pending', an
//     async gateway confirms payment later (MarkPaid).
//   - the staff terminal's cash sale (CreatePaidCash) -- 'paid'
//     immediately, cash is already in hand.
//
// Before this was factored out, those two had their own near-identical
// copies of the code-collision retry loop and INSERT; a change to one
// (a new column, a fixed bug) had no way to reach the other. Now there's
// exactly one place that allocates a reference+code and inserts a ticket
// row, and the two purchase paths are thin, clearly-named wrappers around
// it that only differ in which columns they actually set.
type PurchaseParams struct {
	PlantID       uuid.UUID
	PlantSlug     string
	PriceID       uuid.UUID
	RatePerKgKobo int64
	AmountKobo    int64
	SizeGrams     int
	// Channel must be one of the tickets.channel check constraint's
	// values: transfer, terminal, ussd, cash.
	Channel       string
	CustomerName  string // optional
	CustomerPhone string // optional
	// Paid, if true, inserts the ticket already in 'paid' status with
	// paid_at set to now -- the terminal cash-sale case. If false (the
	// default), inserts 'pending', for a later MarkPaid to confirm.
	Paid bool
	// SoldBy/ShiftID identify the staff member and shift recording a cash
	// sale. Both nil for an online (Paid: false) purchase -- nobody at
	// the plant "sold" it, the customer bought it themselves.
	SoldBy  *uuid.UUID
	ShiftID *uuid.UUID
}

// CreatePending inserts a new ticket in 'pending' status. A thin wrapper
// over Purchase for the online checkout path -- see PurchaseParams.
func CreatePending(ctx context.Context, q tenantdb.Querier, p PurchaseParams) (*Ticket, error) {
	p.Paid = false
	p.SoldBy, p.ShiftID = nil, nil
	return Purchase(ctx, q, p)
}

// CreatePaidCash inserts a new ticket already in 'paid' status with
// channel='cash', sold_by/shift_id set to the terminal operator recording
// it. A thin wrapper over Purchase for the staff terminal's cash-sale
// path -- see PurchaseParams. soldBy/shiftID are required (not pointers)
// here specifically so a cash sale can't accidentally be recorded without
// them, unlike the shared Purchase entry point where they're optional.
func CreatePaidCash(ctx context.Context, q tenantdb.Querier, p PurchaseParams, soldBy, shiftID uuid.UUID) (*Ticket, error) {
	p.Channel = "cash"
	p.Paid = true
	p.SoldBy, p.ShiftID = &soldBy, &shiftID
	return Purchase(ctx, q, p)
}

// Purchase inserts a new ticket, retrying with a freshly generated 4-digit
// code on the rare event of a collision against another currently-live
// ticket at this plant. idx_tickets_open_code (0001) is a partial unique
// index on (plant_id, code) WHERE status IN ('pending', 'paid') -- a code
// only has to be unique among tickets that are still redeemable, not for
// all time, so a collision is expected to be rare but not impossible.
// RatePerKgKobo/PriceID should be a snapshot of the price in effect at
// creation time -- prices are immutable history, so a later price change
// must never retroactively change what an in-flight ticket charges.
func Purchase(ctx context.Context, q tenantdb.Querier, p PurchaseParams) (*Ticket, error) {
	reference, err := NewReference(p.PlantSlug)
	if err != nil {
		return nil, err
	}

	status := "pending"
	var paidAt *time.Time
	if p.Paid {
		status = "paid"
		now := time.Now()
		paidAt = &now
	}

	for attempt := 0; attempt < maxCodeAttempts; attempt++ {
		code, err := NewCode()
		if err != nil {
			return nil, err
		}

		row := q.QueryRow(ctx, `
			INSERT INTO tickets (
				plant_id, reference, code, customer_name, customer_phone,
				size_grams, rate_per_kg, price_id, amount, channel, status,
				paid_at, sold_by, shift_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING id
		`, p.PlantID, reference, code, nullIfEmpty(p.CustomerName), nullIfEmpty(p.CustomerPhone),
			p.SizeGrams, p.RatePerKgKobo, p.PriceID, p.AmountKobo, p.Channel, status,
			paidAt, p.SoldBy, p.ShiftID)

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

// RedeemedTicket is what the staff terminal's Fill tab is allowed to see
// after a code is entered. Deliberately excludes customer_name,
// customer_phone, and reference -- the terminal mockup this replaces was
// explicit that "no customer name, phone number, or ticket reference
// appears on this terminal", and Initials (derived server-side, never the
// raw name) is what it showed instead.
type RedeemedTicket struct {
	ID         uuid.UUID
	Initials   string
	SizeGrams  int
	AmountKobo int64
	Channel    string
}

// Redeem-specific errors -- distinguished (unlike the client-side-only
// mockup this replaces, where every failure just showed "incorrect code")
// so the terminal can show the right message: a code that plainly doesn't
// exist vs. one that's already been used vs. one that hasn't been paid
// for yet.
var (
	ErrCodeNotFound        = errors.New("tickets: no ticket with that code")
	ErrCodeAlreadyRedeemed = errors.New("tickets: this ticket has already been filled")
	ErrCodeNotPayable      = errors.New("tickets: this ticket has not been paid for yet")
)

// Redeem looks up a ticket by its 4-digit code. Codes are only unique
// among currently-live tickets (idx_tickets_open_code, a partial unique
// index scoped to status IN ('pending','paid')) -- once a ticket leaves
// that range (filled, voided, expired), its code number can be reused by
// a later ticket. So this looks up the most recently created ticket with
// that code, not just any match, and returns ErrCodeAlreadyRedeemed
// specifically for a code whose most recent ticket is already filled,
// rather than lumping it in with "never existed".
func Redeem(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, code string) (*RedeemedTicket, error) {
	var (
		id           uuid.UUID
		customerName *string
		sizeGrams    int
		amountKobo   int64
		channel      string
		status       string
	)
	err := q.QueryRow(ctx, `
		SELECT id, customer_name, size_grams, amount, channel, status
		FROM tickets
		WHERE plant_id = $1 AND code = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, plantID, code).Scan(&id, &customerName, &sizeGrams, &amountKobo, &channel, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCodeNotFound
	}
	if err != nil {
		return nil, err
	}

	switch status {
	case "paid":
		// redeemable
	case "filled":
		return nil, ErrCodeAlreadyRedeemed
	default: // pending, voided, expired
		return nil, ErrCodeNotPayable
	}

	return &RedeemedTicket{
		ID:         id,
		Initials:   initialsFromName(customerName),
		SizeGrams:  sizeGrams,
		AmountKobo: amountKobo,
		Channel:    channel,
	}, nil
}

// Fill transitions a paid ticket to filled. Unlike MarkPaid, this is not
// idempotent-by-design -- there's no race between two independent
// confirmation paths here, just one deliberate staff action, so filling an
// already-filled (or otherwise non-paid) ticket is treated as an error
// rather than silently accepted.
//
// net_grams/empty_weight_grams/filled_weight_grams are deliberately left
// unset: the terminal has no weighing-scale input step (matching the
// mockup this replaces, which only ever showed the entitled kg, not an
// actual dispensed weight) -- capturing real dispensed weight would need a
// scale integration this build-out doesn't include. Until that exists,
// filled tickets show "Awaiting fill" for net weight on the owner
// dashboard (cmd/server/main.go's loadOwnerTickets already handles a NULL
// net_grams this way).
func Fill(ctx context.Context, q tenantdb.Querier, plantID, ticketID, shiftID uuid.UUID) error {
	tag, err := q.Exec(ctx, `
		UPDATE tickets
		SET status = 'filled', filled_at = now(), filled_shift_id = $3
		WHERE id = $1 AND plant_id = $2 AND status = 'paid'
	`, ticketID, plantID, shiftID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("tickets: not found, or not currently paid")
	}
	return nil
}

func initialsFromName(name *string) string {
	if name == nil {
		return ""
	}
	fields := strings.Fields(*name)
	var b strings.Builder
	for i, f := range fields {
		if i >= 2 {
			break
		}
		r := []rune(f)
		if len(r) > 0 {
			b.WriteRune(unicode.ToUpper(r[0]))
			b.WriteByte('.')
		}
	}
	return b.String()
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
