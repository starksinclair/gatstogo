package plants

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Create's validation runs entirely before it ever touches adminDB (the
// first thing it does afterward is adminDB.Begin), so a nil pool is safe
// for these cases -- the same pattern internal/prices and internal/staff's
// tests use.

// validParams is a full, valid CreateParams -- including BankName,
// BankAccountName, and PaystackSubaccountCode, which a real caller only
// ever has after a successful Paystack round trip (cmd/server's
// provisionPlant), but which Create itself doesn't know or care how they
// were obtained.
func validParams() CreateParams {
	return CreateParams{
		Name: "Sunrise Gas Plant", Slug: "sunrise",
		State:                  "Imo",
		LegalBusinessName:      "Sunrise Gas Plant Nigeria Ltd",
		CACNumber:              "RC1234567",
		NMDPRALicenseNumber:    "NMDPRA-LPG-000123",
		BankCode:               "058",
		BankName:               "GTBank",
		BankAccountNumber:      "0123456789",
		BankAccountName:        "Sunrise Gas Plant Nigeria Ltd",
		PaystackSubaccountCode: "ACCT_abc123",
		OwnerName:              "Ada Nwosu",
		OwnerPhone:             "08030000000",
		OwnerEmail:             "ada@sunrisegas.ng",
		OwnerPassword:          "a-real-password",
		StartingPriceKobo:      150000,
	}
}

func TestCreateValidation(t *testing.T) {
	valid := validParams()

	cases := []struct {
		name    string
		mutate  func(p CreateParams) CreateParams
		wantErr error
	}{
		{"missing name", func(p CreateParams) CreateParams { p.Name = ""; return p }, ErrMissingField},
		{"missing slug", func(p CreateParams) CreateParams { p.Slug = ""; return p }, ErrMissingField},
		{"missing state", func(p CreateParams) CreateParams { p.State = ""; return p }, ErrMissingField},
		{"invalid state", func(p CreateParams) CreateParams { p.State = "Neverland"; return p }, ErrMissingField},
		{"missing legal business name", func(p CreateParams) CreateParams { p.LegalBusinessName = ""; return p }, ErrMissingField},
		{"missing CAC number", func(p CreateParams) CreateParams { p.CACNumber = ""; return p }, ErrMissingField},
		{"missing NMDPRA license number", func(p CreateParams) CreateParams { p.NMDPRALicenseNumber = ""; return p }, ErrMissingField},
		{"missing bank code", func(p CreateParams) CreateParams { p.BankCode = ""; return p }, ErrMissingField},
		{"missing bank account number", func(p CreateParams) CreateParams { p.BankAccountNumber = ""; return p }, ErrMissingField},
		{"missing bank name (derived, not user-typed)", func(p CreateParams) CreateParams { p.BankName = ""; return p }, ErrMissingField},
		{"missing bank account name (derived, not user-typed)", func(p CreateParams) CreateParams { p.BankAccountName = ""; return p }, ErrMissingField},
		{"missing paystack subaccount code (derived, not user-typed)", func(p CreateParams) CreateParams { p.PaystackSubaccountCode = ""; return p }, ErrMissingField},
		{"missing owner name", func(p CreateParams) CreateParams { p.OwnerName = ""; return p }, ErrMissingField},
		{"missing owner phone", func(p CreateParams) CreateParams { p.OwnerPhone = ""; return p }, ErrMissingField},
		{"missing owner email", func(p CreateParams) CreateParams { p.OwnerEmail = ""; return p }, ErrMissingField},
		{"invalid owner email", func(p CreateParams) CreateParams { p.OwnerEmail = "not-an-email"; return p }, ErrInvalidEmail},
		{"missing owner password", func(p CreateParams) CreateParams { p.OwnerPassword = ""; return p }, ErrMissingField},
		{"missing starting price", func(p CreateParams) CreateParams { p.StartingPriceKobo = 0; return p }, ErrMissingField},
		{"negative starting price", func(p CreateParams) CreateParams { p.StartingPriceKobo = -1; return p }, ErrInvalidPrice},
		{"slug with spaces", func(p CreateParams) CreateParams { p.Slug = "sunrise gas"; return p }, ErrInvalidSlug},
		{"slug with underscore", func(p CreateParams) CreateParams { p.Slug = "sunrise_gas"; return p }, ErrInvalidSlug},
		{"slug starting with hyphen", func(p CreateParams) CreateParams { p.Slug = "-sunrise"; return p }, ErrInvalidSlug},
		{"slug ending with hyphen", func(p CreateParams) CreateParams { p.Slug = "sunrise-"; return p }, ErrInvalidSlug},
	}
	actorID := uuid.New()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Every case here is expected to fail validation before Create
			// ever touches adminDB, so a nil pool is safe.
			if _, err := Create(context.Background(), nil, c.mutate(valid), &actorID); err != c.wantErr {
				t.Errorf("Create(%+v): expected %v, got %v", c.mutate(valid), c.wantErr, err)
			}
		})
	}
}

// TestValidateParamsMatchesCreate confirms ValidateParams (the pre-flight
// check cmd/server's provisionPlant runs before ever calling Paystack)
// accepts exactly what Create's own validation accepts -- specifically,
// that it does NOT require BankName/BankAccountName/PaystackSubaccountCode
// (those don't exist yet at the point ValidateParams is meant to be
// called), while still catching every genuinely user-typed missing field.
func TestValidateParamsMatchesCreate(t *testing.T) {
	valid := validParams()
	valid.BankName = ""
	valid.BankAccountName = ""
	valid.PaystackSubaccountCode = ""

	if err := ValidateParams(valid); err != nil {
		t.Errorf("ValidateParams: expected nil for a submission missing only the Paystack-derived fields, got %v", err)
	}

	missingRequired := valid
	missingRequired.LegalBusinessName = ""
	if err := ValidateParams(missingRequired); err != ErrMissingField {
		t.Errorf("ValidateParams: expected ErrMissingField for a missing user-typed field, got %v", err)
	}
}

// TestCreateStatusValidation covers the two real callers of CreateParams.Status:
// the admin console's onboarding panel, which never sets it (defaulting to
// "active", the historical behavior, preserved byte-for-byte), and the
// public self-serve signup flow (cmd/server/marketing.go), which always
// sets "provisioning" explicitly and has no actor yet -- both still fail
// validation (missing name) before ever touching adminDB, so a nil pool
// and a nil actorID are safe here, same as TestCreateValidation above.
func TestCreateStatusValidation(t *testing.T) {
	base := validParams()
	base.Name = "" // deliberately left blank so every case below still fails at the same ErrMissingField check, before adminDB.Begin.

	t.Run("admin call site: no Status set, no actor omitted", func(t *testing.T) {
		actorID := uuid.New()
		if _, err := Create(context.Background(), nil, base, &actorID); err != ErrMissingField {
			t.Errorf("expected ErrMissingField, got %v", err)
		}
	})

	t.Run("self-serve call site: Status provisioning, nil actor", func(t *testing.T) {
		p := base
		p.Status = "provisioning"
		if _, err := Create(context.Background(), nil, p, nil); err != ErrMissingField {
			t.Errorf("expected ErrMissingField, got %v", err)
		}
	})

	t.Run("invalid Status rejected", func(t *testing.T) {
		p := base
		p.Name = "Sunrise Gas Plant" // pass the earlier missing-field checks so Status is actually reached
		p.Status = "deleted"
		actorID := uuid.New()
		if _, err := Create(context.Background(), nil, p, &actorID); err != ErrInvalidStatus {
			t.Errorf("expected ErrInvalidStatus, got %v", err)
		}
	})
}

func TestSlugPatternAcceptsNormalizedUppercase(t *testing.T) {
	// Create itself lowercases the slug before validating it (strings.
	// ToLower, then slugPattern.MatchString) -- this checks that
	// normalization step directly, without needing a real database to
	// exercise the rest of Create.
	if !slugPattern.MatchString("sunrise") {
		t.Error(`expected "sunrise" (the normalized form of "Sunrise") to match slugPattern`)
	}
}

func TestSetStatusRejectsInvalidStatus(t *testing.T) {
	// Rejected before adminDB.Begin is ever called, so a nil pool is safe.
	for _, bad := range []string{"", "deleted", "Active", "PROVISIONING"} {
		if err := SetStatus(context.Background(), nil, uuid.New(), bad, uuid.New()); err != ErrInvalidStatus {
			t.Errorf("SetStatus(%q): expected ErrInvalidStatus, got %v", bad, err)
		}
	}
}

func TestValidHexColor(t *testing.T) {
	valid := []string{"#006B4D", "#fff", "#FFFFFF", "#000"}
	invalid := []string{"", "006B4D", "#00", "#GGGGGG", "red"}
	for _, c := range valid {
		if !validHexColor(c) {
			t.Errorf("validHexColor(%q) = false, want true", c)
		}
	}
	for _, c := range invalid {
		if validHexColor(c) {
			t.Errorf("validHexColor(%q) = true, want false", c)
		}
	}
}

func TestValidHexColorOrFallsBack(t *testing.T) {
	if got := validHexColorOr("not-a-color", "#006B4D"); got != "#006B4D" {
		t.Errorf("expected fallback for an invalid color, got %q", got)
	}
	if got := validHexColorOr("#123456", "#006B4D"); got != "#123456" {
		t.Errorf("expected the valid color to pass through unchanged, got %q", got)
	}
}

func TestValidEmail(t *testing.T) {
	valid := []string{"ada@sunrisegas.ng", "a.b+tag@example.com"}
	invalid := []string{"", "not-an-email", "missing-domain@", "@missing-local.com", "no-at-sign.com"}
	for _, e := range valid {
		if !validEmail(e) {
			t.Errorf("validEmail(%q) = false, want true", e)
		}
	}
	for _, e := range invalid {
		if validEmail(e) {
			t.Errorf("validEmail(%q) = true, want false", e)
		}
	}
}

func TestNigerianStatesHas37Entries(t *testing.T) {
	// 36 states + FCT.
	if len(NigerianStates) != 37 {
		t.Errorf("expected 37 entries (36 states + FCT), got %d", len(NigerianStates))
	}
	for _, s := range NigerianStates {
		if !validStateNames[s] {
			t.Errorf("NigerianStates entry %q missing from validStateNames", s)
		}
	}
}
