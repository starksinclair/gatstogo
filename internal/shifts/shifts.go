// Package shifts implements the staff terminal's backend: PIN login rate
// limiting, shift open/resume/close, and the token-issued/returned
// counters the terminal's cashier flow tracks. shifts and cash_movements
// (migrations/0001_init_schema.up.sql) have existed since the first
// migration but, before this build-out, nothing ever wrote to either --
// the whole staff terminal was a client-side JS state machine with no
// backend at all.
package shifts

import (
	"context"
	"errors"
	"time"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// ---- PIN attempt rate limiting (Redis-backed) ----
//
// The terminal mockup this replaces had a "5 wrong attempts locks the
// screen for 20 seconds" UX, but it was pure client-side JS state --
// trivially bypassed by just reloading the page. A 4-digit PIN only has
// 10,000 possibilities, so without server-side enforcement it's brute
// forceable in well under a minute. This makes the same UX real.

const (
	MaxPINAttempts = 5
	LockoutTTL     = 20 * time.Second
	attemptWindow  = 5 * time.Minute
)

// ErrLocked is returned by PINLimiter.Check when a user is currently
// locked out.
var ErrLocked = errors.New("shifts: locked out after too many failed PIN attempts")

// PINLimiter tracks failed PIN attempts per user in Redis.
type PINLimiter struct {
	rdb *redis.Client
}

func NewPINLimiter(rdb *redis.Client) *PINLimiter {
	return &PINLimiter{rdb: rdb}
}

// Locked reports whether userID is currently locked out, and for how much
// longer.
func (l *PINLimiter) Locked(ctx context.Context, userID uuid.UUID) (locked bool, retryAfter time.Duration, err error) {
	ttl, err := l.rdb.TTL(ctx, lockKey(userID)).Result()
	if err != nil {
		return false, 0, err
	}
	if ttl > 0 {
		return true, ttl, nil
	}
	return false, 0, nil
}

// RecordFailure increments the failed-attempt counter for userID and locks
// them out once MaxPINAttempts is reached within attemptWindow.
func (l *PINLimiter) RecordFailure(ctx context.Context, userID uuid.UUID) error {
	key := attemptsKey(userID)
	n, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if n == 1 {
		if err := l.rdb.Expire(ctx, key, attemptWindow).Err(); err != nil {
			return err
		}
	}
	if n >= MaxPINAttempts {
		if err := l.rdb.Set(ctx, lockKey(userID), "1", LockoutTTL).Err(); err != nil {
			return err
		}
		l.rdb.Del(ctx, key)
	}
	return nil
}

// Reset clears the failed-attempt counter after a correct PIN entry.
func (l *PINLimiter) Reset(ctx context.Context, userID uuid.UUID) error {
	return l.rdb.Del(ctx, attemptsKey(userID)).Err()
}

func attemptsKey(userID uuid.UUID) string { return "pin_attempts:" + userID.String() }
func lockKey(userID uuid.UUID) string     { return "pin_locked:" + userID.String() }

// ---- Staff listing (for the terminal's "pick your name" screen) ----

// StaffMember is a terminal-eligible user.
type StaffMember struct {
	ID   uuid.UUID
	Name string
	Role string // manager, cashier, or operator
}

// ListStaff returns active manager/cashier/operator users for a plant.
// owner and admin are deliberately excluded here -- they have their own
// dashboard/console (RequireOwnerSession, RequireAdminSession) and aren't
// meant to clock into the terminal itself.
func ListStaff(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID) ([]StaffMember, error) {
	rows, err := q.Query(ctx, `
		SELECT id, name, role FROM users
		WHERE plant_id = $1 AND active AND role IN ('manager', 'cashier', 'operator')
		ORDER BY name
	`, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StaffMember
	for rows.Next() {
		var m StaffMember
		if err := rows.Scan(&m.ID, &m.Name, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- Shift lifecycle ----

// Shift is the current state of an open (or just-closed) shift.
type Shift struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	OpenedAt        time.Time
	OpeningCashKobo int64
	TokensIssued    int
	TokensReturned  int
}

// findOpen looks up a user's currently-open shift at a plant, if any.
func findOpen(ctx context.Context, q tenantdb.Querier, plantID, userID uuid.UUID) (*Shift, error) {
	var s Shift
	var tokensReturned *int
	err := q.QueryRow(ctx, `
		SELECT id, opened_at, opening_cash_kobo, tokens_issued, tokens_returned
		FROM shifts
		WHERE plant_id = $1 AND user_id = $2 AND closed_at IS NULL
	`, plantID, userID).Scan(&s.ID, &s.OpenedAt, &s.OpeningCashKobo, &s.TokensIssued, &tokensReturned)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.UserID = userID
	if tokensReturned != nil {
		s.TokensReturned = *tokensReturned
	}
	return &s, nil
}

// StartOrResume opens a new shift for userID (recording an opening_float
// cash movement) or, if one is already open -- the terminal reloaded or
// reconnected mid-shift -- returns that existing shift unchanged instead.
// shifts' own schema would reject a second concurrent open shift for the
// same user anyway (idx_shifts_one_open_per_user, a partial unique index
// on (plant_id, user_id) WHERE closed_at IS NULL), but checking first
// gives a clean "resumed" result instead of a constraint-violation error
// on an ordinary reload.
func StartOrResume(ctx context.Context, q tenantdb.Querier, plantID, userID uuid.UUID, openingCashKobo int64, deviceID string) (shift *Shift, resumed bool, err error) {
	existing, err := findOpen(ctx, q, plantID, userID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, true, nil
	}

	var device *string
	if deviceID != "" {
		device = &deviceID
	}

	var id uuid.UUID
	var openedAt time.Time
	if err := q.QueryRow(ctx, `
		INSERT INTO shifts (plant_id, user_id, device_id, opening_cash_kobo)
		VALUES ($1, $2, $3, $4)
		RETURNING id, opened_at
	`, plantID, userID, device, openingCashKobo).Scan(&id, &openedAt); err != nil {
		return nil, false, err
	}

	if _, err := q.Exec(ctx, `
		INSERT INTO cash_movements (plant_id, shift_id, kind, amount_kobo, recorded_by, note)
		VALUES ($1, $2, 'opening_float', $3, $4, 'Shift opening float')
	`, plantID, id, openingCashKobo, userID); err != nil {
		return nil, false, err
	}

	return &Shift{ID: id, UserID: userID, OpenedAt: openedAt, OpeningCashKobo: openingCashKobo}, false, nil
}

// Close ends a shift: sets closed_at/closing_cash_kobo/notes and records a
// closing cash movement. Refuses if the shift is already closed or
// doesn't belong to this plant/user.
func Close(ctx context.Context, q tenantdb.Querier, plantID, shiftID, userID uuid.UUID, closingCashKobo int64, notes string) error {
	tag, err := q.Exec(ctx, `
		UPDATE shifts
		SET closed_at = now(), closing_cash_kobo = $4, notes = $5
		WHERE id = $1 AND plant_id = $2 AND user_id = $3 AND closed_at IS NULL
	`, shiftID, plantID, userID, closingCashKobo, nullIfEmpty(notes))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("shifts: not found, or already closed")
	}

	_, err = q.Exec(ctx, `
		INSERT INTO cash_movements (plant_id, shift_id, kind, amount_kobo, counted_kobo, recorded_by, note)
		VALUES ($1, $2, 'closing', $3, $3, $4, 'Shift closing count')
	`, plantID, shiftID, closingCashKobo, userID)
	return err
}

// IncrementTokensIssued bumps a shift's token counter by one -- issued
// specifically to a cash-paying customer at the cashier station (the
// combined/operator configs don't issue tokens in the mockup this
// replaces). Returns the new count, which doubles as the token "number"
// shown to the cashier to hand the customer (e.g. the 5th token issued
// this shift is "token #5") -- there's no per-token identity table in the
// schema (the mockup's individual token numbers like #42/#43 have no
// backing store beyond this aggregate counter), so a simple per-shift
// sequence is the closest equivalent without a schema change.
func IncrementTokensIssued(ctx context.Context, q tenantdb.Querier, plantID, shiftID uuid.UUID) (newCount int, err error) {
	err = q.QueryRow(ctx, `
		UPDATE shifts SET tokens_issued = tokens_issued + 1
		WHERE id = $1 AND plant_id = $2 AND closed_at IS NULL
		RETURNING tokens_issued
	`, shiftID, plantID).Scan(&newCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errors.New("shifts: not found, or already closed")
	}
	return newCount, err
}

// IncrementTokensReturned bumps a shift's returned-token counter by one
// and returns the new count.
func IncrementTokensReturned(ctx context.Context, q tenantdb.Querier, plantID, shiftID uuid.UUID) (newCount int, err error) {
	err = q.QueryRow(ctx, `
		UPDATE shifts SET tokens_returned = COALESCE(tokens_returned, 0) + 1
		WHERE id = $1 AND plant_id = $2 AND closed_at IS NULL
		RETURNING tokens_returned
	`, shiftID, plantID).Scan(&newCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errors.New("shifts: not found, or already closed")
	}
	return newCount, err
}

// Summary reports a shift's running totals: fills completed and grams
// dispensed (from tickets this shift filled), and cash collected via
// terminal cash sales (from cash_movements). The terminal frontend
// overwrites its local counters from this after every state-changing
// call rather than incrementing them itself, so a reload mid-shift (or a
// request that silently failed) can never leave it showing stale or
// drifted numbers -- the server is always the source of truth. A brand
// new shift naturally has all-zero totals anyway, so this is safe to call
// unconditionally rather than special-casing "was this shift resumed?".
func Summary(ctx context.Context, q tenantdb.Querier, plantID, shiftID uuid.UUID) (fillsCompleted int, gramsDispensed int64, cashHeldKobo int64, err error) {
	if err = q.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size_grams), 0)
		FROM tickets
		WHERE plant_id = $1 AND filled_shift_id = $2 AND status = 'filled'
	`, plantID, shiftID).Scan(&fillsCompleted, &gramsDispensed); err != nil {
		return 0, 0, 0, err
	}
	err = q.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_kobo), 0)
		FROM cash_movements
		WHERE plant_id = $1 AND shift_id = $2 AND kind = 'sale'
	`, plantID, shiftID).Scan(&cashHeldKobo)
	return fillsCompleted, gramsDispensed, cashHeldKobo, err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
