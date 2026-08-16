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
	ErrMissingField  = errors.New("plants: plant name, slug, business details, bank details, owner details, and starting price are all required")
	ErrInvalidSlug   = errors.New("plants: slug must be lowercase letters, numbers, and hyphens only, and can't start or end with a hyphen")
	ErrReservedSlug  = errors.New("plants: that slug is reserved and can't be used")
	ErrSlugTaken     = errors.New("plants: that slug is already in use")
	ErrInvalidPrice  = errors.New("plants: starting price per kg must be greater than zero")
	ErrInvalidEmail  = errors.New("plants: owner email is not a valid email address")
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

// emailPattern is a deliberately loose "does this look like an email"
// check, not a full RFC 5322 validator -- the real, authoritative check on
// any address this app actually sends mail to is the mail system itself
// (the same reasoning cmd/server/tickets.go's ticketEmail doc comment
// already lands on for Paystack's own email validation).
var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func validEmail(value string) bool {
	return emailPattern.MatchString(value)
}

// NigerianStates lists Nigeria's 36 states plus the Federal Capital
// Territory, in the fixed order shown on both onboarding forms' State
// dropdown (web/templates/pages/signup.templ, owner_admin.templ). This is
// a hardcoded list rather than anything database-backed because it simply
// doesn't change -- the single source of truth both templates render from
// and validate below reuses.
var NigerianStates = []string{
	"Abia", "Adamawa", "Akwa Ibom", "Anambra", "Bauchi", "Bayelsa", "Benue",
	"Borno", "Cross River", "Delta", "Ebonyi", "Edo", "Ekiti", "Enugu",
	"Gombe", "Imo", "Jigawa", "Kaduna", "Kano", "Katsina", "Kebbi", "Kogi",
	"Kwara", "Lagos", "Nasarawa", "Niger", "Ogun", "Ondo", "Osun", "Oyo",
	"Plateau", "Rivers", "Sokoto", "Taraba", "Yobe", "Zamfara",
	"FCT (Abuja)",
}

var validStateNames = func() map[string]bool {
	m := make(map[string]bool, len(NigerianStates))
	for _, s := range NigerianStates {
		m[s] = true
	}
	return m
}()

// Brand defaults (matching web/templates/pages/customer_home.templ's own
// customerDefault* constants) used when the admin leaves a color field
// blank or enters something that isn't a valid hex color -- not the
// generic blue migrations/0001_init_schema.up.sql's own column defaults
// fall back to, which don't match GatsToGo's actual brand at all.
const (
	defaultPrimaryColor    = "#006B4D"
	defaultButtonColor     = "#006B4D"
	defaultSecondaryColor  = "#10B981"
	defaultButtonTextColor = "#F8FAFC"
)

// CreateParams are the fields both onboarding entry points collect: the
// admin console's "Create plant" panel and the public self-serve
// /get-started form (cmd/server/marketing.go's signupSubmitHandler). This
// deliberately covers plant identity, the business's real-world
// legal/regulatory identity (CAC, NMDPRA), where it actually gets paid
// (bank/Paystack subaccount), and the owner's first login -- consolidated
// into a single Create call rather than separate endpoints, so a newly
// provisioned plant is immediately usable (a real owner can log in, a real
// price exists to sell at, real settlement is already wired up) instead of
// left half-set-up.
//
// BankName, BankAccountName, and PaystackSubaccountCode are deliberately
// NOT something either form's visitor types directly -- they're derived
// server-side (cmd/server's provisionPlant) from a real Paystack call
// before Create is ever reached, the same "don't trust the client for
// what the server can derive authoritatively" reasoning BankAccountName's
// own field doc below explains. Create still requires all three to be
// present (see the second validation block below): a plant row should
// never exist without a real, working settlement path attached to it.
type CreateParams struct {
	Name         string
	Slug         string
	City         string
	Phone        string
	Address      string
	State        string
	PrimaryColor string
	ButtonColor  string

	// SecondaryColor and ButtonTextColor round out the brand kit
	// PrimaryColor/ButtonColor started -- both already read and applied
	// by the customer-facing page (web/templates/pages/customer_home.templ's
	// customerTheme) since before this build-out, just never settable by
	// either onboarding form until now. Optional, same fallback-on-blank-
	// or-invalid treatment as PrimaryColor/ButtonColor: never gates
	// submission.
	SecondaryColor  string
	ButtonTextColor string

	// LogoPath is the public URL a logo was saved to (cmd/server's
	// provisionPlant, via internal/uploads.SaveLogo) -- optional, and,
	// like BankName/BankAccountName below, not something Create trusts a
	// caller to have typed: it's either empty (no logo uploaded) or a
	// path SaveLogo itself just returned. plants.logo_path has existed in
	// the schema since the first migration and customerTheme already
	// renders it if set; this is what finally lets it ever be set.
	LogoPath string

	// LegalBusinessName, CACNumber, and NMDPRALicenseNumber are the
	// business's real-world registered identity -- LegalBusinessName may
	// differ from Name (a customer-facing trade name), CACNumber is the
	// business's CAC registration number, and NMDPRALicenseNumber is its
	// NMDPRA LPG retail operating license number. Nigerian LPG retail
	// requires an NMDPRA license; there was previously nowhere in this
	// schema to record one at all.
	LegalBusinessName   string
	CACNumber           string
	NMDPRALicenseNumber string

	// BankCode and BankAccountNumber are submitted by the visitor (a bank
	// chosen from a live-populated dropdown, an account number typed in).
	// BankName and BankAccountName are resolved server-side -- see the
	// type doc comment above.
	BankCode          string
	BankName          string
	BankAccountNumber string
	BankAccountName   string

	// PaystackSubaccountCode is returned by Paystack once the subaccount
	// is created (cmd/server's provisionPlant, before Create is called) --
	// this is what gets attached to every ticket this plant sells
	// (cmd/server/tickets.go's buyGasSubmitHandler), so customer payments
	// settle straight to the plant's own bank account instead of
	// GatsToGo's, with GatsToGo's commission deducted automatically per
	// transaction by Paystack's own split payment mechanics (see
	// internal/payments/paystack.go's package doc for the full mechanism
	// and PlantSettlementPercentage for the actual number).
	PaystackSubaccountCode string

	OwnerName     string
	OwnerPhone    string
	OwnerEmail    string
	OwnerPassword string

	StartingPriceKobo int64

	// Status is the plant's initial lifecycle status. Empty means
	// "active" -- the historical behavior, and still correct for the
	// admin console's own onboarding panel (a platform admin creating a
	// plant directly is already the review step). The public self-serve
	// signup flow (cmd/server/marketing.go) passes "provisioning"
	// explicitly instead: middleware.Tenant's own lookup already filters
	// WHERE status = 'active', so an unreviewed plant's subdomain simply
	// won't resolve until an admin approves it via the existing
	// POST /admin/plants/{id}/status (SetStatus, below) -- no separate
	// enforcement needed.
	Status string
}

// Plant is what a caller needs back after creating one.
type Plant struct {
	ID   uuid.UUID
	Slug string
}

// validated is the trimmed/normalized form of a CreateParams that passed
// validate below -- exactly the set of values Create needs for its
// INSERTs, computed once instead of re-trimming the same strings twice.
type validated struct {
	name, slug                                                 string
	state, legalBusinessName, cacNumber, nmdpraLicenseNumber   string
	bankCode, bankAccountNumber                                string
	ownerName, ownerPhone, ownerEmail                          string
	status                                                     string
	primaryColor, buttonColor, secondaryColor, buttonTextColor string
}

// validate normalizes and checks every field a visitor actually fills in
// on either onboarding form. Deliberately does NOT check BankName,
// BankAccountName, or PaystackSubaccountCode -- those are only ever
// derived after a real Paystack call the caller hasn't necessarily made
// yet (see ValidateParams's own doc comment for why this split exists).
// Create (below) checks those three separately, immediately before it
// touches the database, so a plant row can still never be written without
// them.
func validate(p CreateParams) (validated, error) {
	var v validated
	v.name = strings.TrimSpace(p.Name)
	v.slug = strings.ToLower(strings.TrimSpace(p.Slug))
	v.state = strings.TrimSpace(p.State)
	v.legalBusinessName = strings.TrimSpace(p.LegalBusinessName)
	v.cacNumber = strings.TrimSpace(p.CACNumber)
	v.nmdpraLicenseNumber = strings.TrimSpace(p.NMDPRALicenseNumber)
	v.bankCode = strings.TrimSpace(p.BankCode)
	v.bankAccountNumber = strings.TrimSpace(p.BankAccountNumber)
	v.ownerName = strings.TrimSpace(p.OwnerName)
	v.ownerPhone = strings.TrimSpace(p.OwnerPhone)
	v.ownerEmail = strings.TrimSpace(p.OwnerEmail)
	v.primaryColor = validHexColorOr(p.PrimaryColor, defaultPrimaryColor)
	v.buttonColor = validHexColorOr(p.ButtonColor, defaultButtonColor)
	v.secondaryColor = validHexColorOr(p.SecondaryColor, defaultSecondaryColor)
	v.buttonTextColor = validHexColorOr(p.ButtonTextColor, defaultButtonTextColor)

	if v.name == "" || v.slug == "" || v.state == "" || v.legalBusinessName == "" ||
		v.cacNumber == "" || v.nmdpraLicenseNumber == "" || v.bankCode == "" ||
		v.bankAccountNumber == "" || v.ownerName == "" || v.ownerPhone == "" ||
		v.ownerEmail == "" || p.OwnerPassword == "" || p.StartingPriceKobo == 0 {
		return v, ErrMissingField
	}
	if !validStateNames[v.state] {
		return v, ErrMissingField
	}
	if !slugPattern.MatchString(v.slug) {
		return v, ErrInvalidSlug
	}
	if !validEmail(v.ownerEmail) {
		return v, ErrInvalidEmail
	}
	if p.StartingPriceKobo <= 0 {
		return v, ErrInvalidPrice
	}

	v.status = strings.TrimSpace(p.Status)
	if v.status == "" {
		v.status = "active"
	}
	if !validStatuses[v.status] {
		return v, ErrInvalidStatus
	}
	return v, nil
}

// ValidateParams reports whether p has every field a visitor is
// responsible for filling in correctly, without touching the database or
// Paystack. Exported so the handler layer (cmd/server's provisionPlant)
// can validate a submission fully before calling Paystack's
// CreateSubaccount, which has a real side effect (a live subaccount
// entity created at Paystack) that's wasted, orphaned work for a
// submission that was going to fail validation anyway. Create runs this
// exact same check internally too, so nothing about calling Create
// directly changes.
func ValidateParams(p CreateParams) error {
	_, err := validate(p)
	return err
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
//
// actorID is nilable: the admin console's onboarding panel passes the
// authenticated admin's own id, but the public self-serve signup flow
// (cmd/server/marketing.go) has no actor yet and passes nil -- the same
// "no specific actor" convention audit.Log's other callers already use
// (e.g. cmd/server/receipts.go's ticket.confirmed entry).
func Create(ctx context.Context, adminDB *pgxpool.Pool, p CreateParams, actorID *uuid.UUID) (*Plant, error) {
	v, err := validate(p)
	if err != nil {
		return nil, err
	}

	// BankName, BankAccountName, and PaystackSubaccountCode are derived,
	// not user-typed (see CreateParams's own doc comment) -- checked here,
	// separately from validate above, as the final integrity gate: a
	// plant row must never be written without a real, resolved settlement
	// path already attached to it.
	bankName := strings.TrimSpace(p.BankName)
	bankAccountName := strings.TrimSpace(p.BankAccountName)
	subaccountCode := strings.TrimSpace(p.PaystackSubaccountCode)
	if bankName == "" || bankAccountName == "" || subaccountCode == "" {
		return nil, ErrMissingField
	}

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
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reserved_slugs WHERE slug = $1)`, v.slug).Scan(&reserved); err != nil {
		return nil, err
	}
	if reserved {
		return nil, ErrReservedSlug
	}

	var plantID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO plants (
			name, slug, city, address, phone, state,
			legal_business_name, cac_number, nmdpra_license_number,
			bank_code, bank_name, bank_account_number, bank_account_name,
			paystack_subaccount_code,
			primary_color, button_color, secondary_color, button_text_color, logo_path, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		RETURNING id
	`,
		v.name, v.slug, nullIfEmpty(p.City), nullIfEmpty(p.Address), nullIfEmpty(p.Phone), v.state,
		v.legalBusinessName, v.cacNumber, v.nmdpraLicenseNumber,
		v.bankCode, bankName, v.bankAccountNumber, bankAccountName,
		subaccountCode,
		v.primaryColor, v.buttonColor, v.secondaryColor, v.buttonTextColor, nullIfEmpty(p.LogoPath), v.status,
	).Scan(&plantID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSlugTaken
		}
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO users (plant_id, name, phone, email, role, password_hash, active)
		VALUES ($1, $2, $3, $4, 'owner', $5, true)
	`, plantID, v.ownerName, v.ownerPhone, v.ownerEmail, passwordHash); err != nil {
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
	// and a real settlement path is not the place to be the one exception
	// to that.
	if err := audit.Log(ctx, tx, &plantID, actorID, "plant.created", v.slug, nil); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	return &Plant{ID: plantID, Slug: v.slug}, nil
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
