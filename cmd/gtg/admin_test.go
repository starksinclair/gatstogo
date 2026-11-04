package main

import "testing"

func TestValidateAdminInput(t *testing.T) {
	good := "correct-horse-battery-9"
	cases := []struct {
		name, phone, email, pw string
		ok                     bool
	}{
		{"Ada", "08031234567", "", good, true},
		{"Ada", "08031234567", "ada@example.com", good, true},
		{"", "08031234567", "", good, false},
		{"Ada", "8031234567", "", good, false},
		{"Ada", "0803123456", "", good, false},
		{"Ada", "08000000000", "", good, false},
		{"Ada", "08031234567", "not-an-email", good, false},
		{"Ada", "08031234567", "", "short-pass", false},
		{"Ada", "08031234567", "", "platform-admin-dev-extra", false},
		{"Ada", "08031234567", "", "my-password-is-long", false},
		{"Ada", "08031234567", "", "xxxxxxxxxxxxxxxx", false},
		{"Ada", "08031234567", "", "call-08031234567-now", false},
	}
	for _, c := range cases {
		err := validateAdminInput(c.name, c.phone, c.email, c.pw)
		if (err == nil) != c.ok {
			t.Errorf("validateAdminInput(%q,%q,%q,%q) err=%v, want ok=%v", c.name, c.phone, c.email, c.pw, err, c.ok)
		}
	}
}
