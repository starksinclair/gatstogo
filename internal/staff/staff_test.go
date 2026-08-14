package staff

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestCreateValidation(t *testing.T) {
	ctx := context.Background()
	plantID := uuid.New()

	cases := []struct {
		name    string
		params  CreateParams
		wantErr error
	}{
		{"missing name", CreateParams{Phone: "0803", Role: "cashier", PIN: "1234"}, ErrMissingField},
		{"missing phone", CreateParams{Name: "Ada", Role: "cashier", PIN: "1234"}, ErrMissingField},
		{"missing PIN", CreateParams{Name: "Ada", Phone: "0803", Role: "cashier"}, ErrMissingField},
		{"invalid role", CreateParams{Name: "Ada", Phone: "0803", Role: "owner", PIN: "1234"}, ErrInvalidRole},
		{"admin role rejected", CreateParams{Name: "Ada", Phone: "0803", Role: "admin", PIN: "1234"}, ErrInvalidRole},
		{"PIN too short", CreateParams{Name: "Ada", Phone: "0803", Role: "cashier", PIN: "123"}, ErrInvalidPIN},
		{"PIN too long", CreateParams{Name: "Ada", Phone: "0803", Role: "cashier", PIN: "12345"}, ErrInvalidPIN},
		{"PIN not numeric", CreateParams{Name: "Ada", Phone: "0803", Role: "cashier", PIN: "abcd"}, ErrInvalidPIN},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// None of these should reach the database -- validation fails
			// first, so a nil Querier is safe here.
			if _, err := Create(ctx, nil, plantID, c.params); err != c.wantErr {
				t.Errorf("Create(%+v): expected %v, got %v", c.params, c.wantErr, err)
			}
		})
	}
}

func TestValidPIN(t *testing.T) {
	valid := []string{"0000", "1234", "9999"}
	invalid := []string{"", "123", "12345", "12a4", " 234", "abcd"}
	for _, p := range valid {
		if !validPIN(p) {
			t.Errorf("validPIN(%q) = false, want true", p)
		}
	}
	for _, p := range invalid {
		if validPIN(p) {
			t.Errorf("validPIN(%q) = true, want false", p)
		}
	}
}
