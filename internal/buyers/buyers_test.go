package buyers

import (
	"context"
	"errors"
	"testing"
)

// Validation runs before any database access, so a nil pool is safe here.
func TestSignupValidation(t *testing.T) {
	good := SignupParams{Name: "Ada", Phone: "0803 111 2222", State: "Lagos"}
	cases := []struct {
		name string
		mut  func(*SignupParams)
		want error
	}{
		{"missing name", func(p *SignupParams) { p.Name = " " }, ErrMissingField},
		{"short phone", func(p *SignupParams) { p.Phone = "0803" }, ErrInvalidPhone},
		{"bad email", func(p *SignupParams) { p.Email = "nope" }, ErrInvalidEmail},
		{"bad state", func(p *SignupParams) { p.State = "Narnia" }, ErrInvalidState},
		{"empty state", func(p *SignupParams) { p.State = "" }, ErrInvalidState},
	}
	for _, c := range cases {
		p := good
		c.mut(&p)
		if _, err := Signup(context.Background(), nil, p); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}
