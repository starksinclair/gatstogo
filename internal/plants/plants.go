// Package plants implements admin-side plant onboarding: creating a new
// tenant and changing its lifecycle status. plants has existed since the
// first migration but, before this build-out, the only row ever
// inserted was the one seeded at setup -- the admin console's "Create
// plant" panel was decorative, and reserved_slugs (also seeded, and
// displayed read-only in the admin console) was never actually consulted
// by anything.
package plants

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"gatstogo/internal/audit"
	"gatstogo/internal/auth"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrMissingField  = errors.New("plants: plant name, slug, owner name, owner phone, owner password, and starting price are all required")
	ErrInvalidSlug   = errors.New("plants: slug must be lowercase letters, numbers, and hyphens only, and can't start or end with a hyphen")
	ErrReservedSlug  = errors.New("plants: that slug is reserved and can't be used")
	ErrSlugTaken     = errors.New("plants: that slug is already in use")
	ErrInvalidPrice  = errors.New("plants: starting price per kg must be greater than zero")
	ErrNotFound      = errors.New("plants: not found")
	ErrInvalidStatus = errors.New("plants: status must be provisioning, active, suspended, or closed")
)

// slugPattern mirrors a standard DNS label: 1-63 chars, lowercase
// alphanumeric, hyphens allowed only in the middle. plants.slug feeds
// directly into subdomain routing (middleware.Tenant's extractSubdomain
// compares it against the request Host header), so this is stricter than
// Paystack's reference character set (internal/tickets.
// sanitizeReferenceSegment) needs to be -- a slug has to be a valid
// hostname label, not just URL-safe.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Brand defaults (matching web/templates/pages/customer_home.templ's own
// customerDefault* constants) used when the admin leaves a color field
// blank or enters something that isn't a valid hex color -- not the
// generic blue migrations/0001_init_schema.up.sql's own column defaults
// fall back to, which don't match GatsToGo's actual brand at all.
const (
	defaultPrimaryColor = "#006B4D"
	defaultButtonColor  = "#006B4D"
)

// CreateParams are the fields the admin console's "Create plant" form
// collects. This deliberately covers the first three steps of the
// console's own "Provisioning checklist" (reserve slug + create tenant
// row, add owner user with a password, set a starting price) in one
// submission -- consolidated into a single Create call rather than
// separate plant/owner endpoints, so a newly provisioned plant is
// immediately usable (a real owner can log in, a real price exists for
// the customer page to sell at) instead of left half-set-up. The
// checklist's fourth step (test the pages) is inherently manual.
type CreateParams struct {
	Name              string
	Slug              string
	City              string
	Phone             string
	Address           string
	PrimaryColor      string
	ButtonColor       string
	OwnerName         string
	OwnerPhone        string
	OwnerPassword     string
	StartingPriceKobo int64
}

// Plant is what a caller needs back after creating one.
type Plant struct {
	ID   uuid.UUID
	Slug string
}

// Create provisions a new tenant: the plants row, its first owner user,
// and a starting price, all in one transaction.
//
// Runs directly against adminDB (the RLS-bypassing pool), not through
// tenantdb.WithTenant: WithTenant needs a plantID to scope the
// transaction's app.current_plant_id to before it starts, but there's no
// plant id yet -- that's the whole point of this call. Even setting that
// aside, plants' own RLS policy (USING (id = current_plant_id())) applies
// to INSERTs too by default (no separate WITH CHECK was defined in 0001),
// so a new row -- whose id is only assigned by gen_random_uuid() at
// insert time -- could never satisfy "id = <a plant id decided in
// advance>" anyway. This is exactly the kind of pre-tenant, directory-
// level write AdminDB exists for, the same reasoning as why
// middleware.Tenant's own slug lookup runs on it.
func Create(ctx context.Context, adminDB *pgxpool.Pool, p CreateParams, actorID uuid.UUID) (*Plant, error) {
	name := strings.TrimSpace(p.Name)
	slug := strings.ToLower(strings.TrimSpace(p.Slug))
	ownerName := strings.TrimSpace(p.OwnerName)
	ownerPhone := strings.TrimSpace(p.OwnerPhone)

	if name == "" || slug == "" || ownerName == "" || ownerPhone == "" || p.OwnerPassword == "" || p.StartingPriceKobo == 0 {
		return nil, ErrMissingField
	}
	if !slugPattern.MatchString(slug) {
		return nil, ErrInvalidSlug
	}
	if p.StartingPriceKobo <= 0 {
		return nil, ErrInvalidPrice
	}

	primaryColor := validHexColorOr(p.PrimaryColor, defaultPrimaryColor)
	buttonColor := validHexColorOr(p.ButtonColor, defaultButtonColor)

	passwordHash, err := auth.HashPassword(p.OwnerPassword)
	if err != nil {
		return nil, err
	}

	tx, err := adminDB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	var reserved bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reserved_slugs WHERE slug = $1)`, slug).Scan(&reserved); err != nil {
		return nil, err
	}
	if reserved {
		return nil, ErrReservedSlug
	}

	var plantID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO plants (name, slug, city, address, phone, primary_color, button_color)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, name, slug, nullIfEmpty(p.City), nullIfEmpty(p.Address), nullIfEmpty(p.Phone), primaryColor, buttonColor).Scan(&plantID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSlugTaken
		}
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO users (plant_id, name, phone, role, password_hash, active)
		VALUES ($1, $2, $3, 'owner', $4, true)
	`, plantID, ownerName, ownerPhone, passwordHash); err != nil {
		return nil, err
	}

	// set_by left NULL: the platform admin provisioning this plant has no
	// user row scoped to it (every users row belongs to exactly one
	// plant_id), so there's no meaningful "who" to attribute a
	// system-provisioned starting price to.
	if _, err := tx.Exec(ctx, `
		INSERT INTO prices (plant_id, price_per_kg) VALUES ($1, $2)
	`, plantID, p.StartingPriceKobo); err != nil {
		return nil, err
	}

	// Logged inside this same transaction, not by the caller afterward --
	// every other write path in this codebase (tickets, shifts, staff,
	// prices) keeps its audit.Log call atomic with the business write it
	// describes, and provisioning a new tenant with real login credentials
	// is not the place to be the one exception to that.
	if err := audit.Log(ctx, tx, &plantID, &actorID, "plant.created", slug, nil); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	return &Plant{ID: plantID, Slug: slug}, nil
}

var validStatuses = map[string]bool{"provisioning": true, "active": true, "suspended": true, "closed": true}

// SetStatus changes a plant's lifecycle status and logs it, atomically --
// takes *pgxpool.Pool directly (AdminDB) rather than the general
// tenantdb.Querier interface specifically so it can open its own
// transaction and keep the audit entry atomic with the update, the same
// as every other write path in this codebase. There's no per-request
// tenant scoping concern here either way -- this is a platform-level
// change keyed by plant id, not a tenant-scoped one.
func SetStatus(ctx context.Context, adminDB *pgxpool.Pool, plantID uuid.UUID, status string, actorID uuid.UUID) error {
	if !validStatuses[status] {
		return ErrInvalidStatus
	}

	tx, err := adminDB.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	tag, err := tx.Exec(ctx, `UPDATE plants SET status = $2 WHERE id = $1`, plantID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	if err := audit.Log(ctx, tx, &plantID, &actorID, "plant.status_changed", status, nil); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func validHexColorOr(value, fallback string) string {
	value = strings.TrimSpace(value)
	if validHexColor(value) {
		return value
	}
	return fallback
}

// validHexColor mirrors customer_home.templ's own check of the same name
// -- duplicated rather than shared, the same call made for the small
// formatting helpers duplicated between cmd/server and web/templates/pages
// elsewhere in this build-out (different packages, ~10 lines, not worth a
// shared internal/color package for).
func validHexColor(value string) bool {
	if len(value) != 4 && len(value) != 7 {
		return false
	}
	if !strings.HasPrefix(value, "#") {
		return false
	}
	for _, r := range value[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
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
