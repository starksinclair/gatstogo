// Package staff implements the owner dashboard's staff management: adding
// a cashier/operator/manager and activating/deactivating one. users has
// existed since the first migration but, before this build-out, was only
// ever populated by the seed SQL -- the owner dashboard's "Add staff"
// panel was decorative, and there was no way to deactivate anyone.
package staff

import (
	"context"
	"errors"
	"strings"

	"gatstogo/internal/auth"
	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrMissingField = errors.New("staff: name, phone, and PIN are required")
	ErrInvalidRole  = errors.New("staff: role must be manager, cashier, or operator")
	ErrInvalidPIN   = errors.New("staff: PIN must be exactly 4 digits")
	ErrPhoneTaken   = errors.New("staff: a staff member with that phone number already exists at this plant")
	ErrNotFound     = errors.New("staff: not found")
)

// CreateParams are the fields the owner dashboard's "Add staff" form
// collects. This creates terminal-capable staff only (manager, cashier,
// operator, matching the form's single "PIN" field) -- it sets pin_hash,
// never password_hash. A manager created here can clock into the staff
// terminal with this PIN; granting them password-based owner-dashboard
// access (RequireOwnerSession, which checks password_hash) is a
// separate, not-yet-built provisioning step, not something this form
// does implicitly.
type CreateParams struct {
	Name  string
	Phone string
	Role  string // manager, cashier, operator
	PIN   string
}

// Create adds a new active staff member. Returns ErrPhoneTaken if the
// phone number collides with the schema's UNIQUE(plant_id, phone)
// constraint -- phones only need to be unique within one plant, not
// platform-wide.
func Create(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, p CreateParams) (uuid.UUID, error) {
	name := strings.TrimSpace(p.Name)
	phone := strings.TrimSpace(p.Phone)
	pin := strings.TrimSpace(p.PIN)

	if name == "" || phone == "" || pin == "" {
		return uuid.Nil, ErrMissingField
	}
	if p.Role != "manager" && p.Role != "cashier" && p.Role != "operator" {
		return uuid.Nil, ErrInvalidRole
	}
	if !validPIN(pin) {
		return uuid.Nil, ErrInvalidPIN
	}

	pinHash, err := auth.HashPassword(pin)
	if err != nil {
		return uuid.Nil, err
	}

	var id uuid.UUID
	err = q.QueryRow(ctx, `
		INSERT INTO users (plant_id, name, phone, role, pin_hash, active)
		VALUES ($1, $2, $3, $4, $5, true)
		RETURNING id
	`, plantID, name, phone, p.Role, pinHash).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return uuid.Nil, ErrPhoneTaken
		}
		return uuid.Nil, err
	}
	return id, nil
}

// SetActive activates or deactivates a staff member. The terminal's PIN
// login (WHERE ... AND active, cmd/server/terminal.go) already refuses an
// inactive user, so deactivating here takes effect immediately without
// deleting their historical tickets/shifts. Restricted to
// manager/cashier/operator rows -- this isn't a general-purpose "disable
// any user" endpoint; an owner can't deactivate themselves or another
// owner through it.
func SetActive(ctx context.Context, q tenantdb.Querier, plantID, userID uuid.UUID, active bool) error {
	tag, err := q.Exec(ctx, `
		UPDATE users SET active = $3
		WHERE id = $1 AND plant_id = $2 AND role IN ('manager', 'cashier', 'operator')
	`, userID, plantID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// validPIN requires exactly 4 digits -- the terminal's PIN pad
// (web/templates/pages/terminal.templ) physically only collects 4 digits,
// so a PIN created outside that shape could never actually be entered.
func validPIN(pin string) bool {
	if len(pin) != 4 {
		return false
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
