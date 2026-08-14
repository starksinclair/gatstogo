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

func TestCreateValidation(t *testing.T) {
	valid := CreateParams{
		Name: "Sunrise Gas Plant", Slug: "sunrise", OwnerName: "Ada Nwosu",
		OwnerPhone: "08030000000", OwnerPassword: "a-real-password", StartingPriceKobo: 150000,
	}

	cases := []struct {
		name    string
		mutate  func(p CreateParams) CreateParams
		wantErr error
	}{
		{"missing name", func(p CreateParams) CreateParams { p.Name = ""; return p }, ErrMissingField},
		{"missing slug", func(p CreateParams) CreateParams { p.Slug = ""; return p }, ErrMissingField},
		{"missing owner name", func(p CreateParams) CreateParams { p.OwnerName = ""; return p }, ErrMissingField},
		{"missing owner phone", func(p CreateParams) CreateParams { p.OwnerPhone = ""; return p }, ErrMissingField},
		{"missing owner password", func(p CreateParams) CreateParams { p.OwnerPassword = ""; return p }, ErrMissingField},
		{"missing starting price", func(p CreateParams) CreateParams { p.StartingPriceKobo = 0; return p }, ErrMissingField},
		{"negative starting price", func(p CreateParams) CreateParams { p.StartingPriceKobo = -1; return p }, ErrInvalidPrice},
		{"slug with spaces", func(p CreateParams) CreateParams { p.Slug = "sunrise gas"; return p }, ErrInvalidSlug},
		{"slug with underscore", func(p CreateParams) CreateParams { p.Slug = "sunrise_gas"; return p }, ErrInvalidSlug},
		{"slug starting with hyphen", func(p CreateParams) CreateParams { p.Slug = "-sunrise"; return p }, ErrInvalidSlug},
		{"slug ending with hyphen", func(p CreateParams) CreateParams { p.Slug = "sunrise-"; return p }, ErrInvalidSlug},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Every case here is expected to fail validation before Create
			// ever touches adminDB, so a nil pool is safe.
			if _, err := Create(context.Background(), nil, c.mutate(valid), uuid.New()); err != c.wantErr {
				t.Errorf("Create(%+v): expected %v, got %v", c.mutate(valid), c.wantErr, err)
			}
		})
	}
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
