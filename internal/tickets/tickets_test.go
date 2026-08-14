package tickets

import (
	"strings"
	"testing"
)

func TestNewReference(t *testing.T) {
	ref, err := NewReference("Sunrise")
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	if !strings.HasPrefix(ref, "sunrise-") {
		t.Errorf("expected the slug to be lowercased and prefixed, got %q", ref)
	}
	if len(ref) != len("sunrise-")+12 {
		t.Errorf("unexpected reference length: %q", ref)
	}

	other, err := NewReference("Sunrise")
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	if ref == other {
		t.Error("expected two references for the same plant to differ")
	}
}

func TestNewReferenceSanitizesSlug(t *testing.T) {
	// Nothing in the schema constrains plants.slug's character set, but
	// Paystack's transaction reference only allows alphanumerics, -, ., =.
	ref, err := NewReference("Sunrise Gas Plant #1 (Owerri)")
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '=':
			// allowed
		default:
			t.Fatalf("reference %q contains a character Paystack would reject: %q", ref, r)
		}
	}

	// A slug that sanitizes down to nothing shouldn't produce a malformed
	// (empty-prefix) reference.
	ref, err = NewReference("   ")
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	if !strings.HasPrefix(ref, "plant-") {
		t.Errorf("expected a fallback prefix for an all-whitespace slug, got %q", ref)
	}
}

func TestNewCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := NewCode()
		if err != nil {
			t.Fatalf("NewCode: %v", err)
		}
		if len(code) != 4 {
			t.Fatalf("expected a 4-digit code, got %q", code)
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Fatalf("expected only digits, got %q", code)
			}
		}
		seen[code] = true
	}
	// Not a strict uniqueness requirement (codes only need to be unique
	// among *live* tickets at one plant, enforced at the DB layer) --
	// just a sanity check that draws aren't degenerate.
	if len(seen) < 100 {
		t.Errorf("expected a reasonable spread across 200 draws, only saw %d distinct codes", len(seen))
	}
}

func TestInitialsFromName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Ada Nwosu", "A.N."},
		{"  Ifeoma  Chukwu  ", "I.C."},
		{"Cher", "C."},
		{"Tunde Bakare Extra Name", "T.B."}, // only the first two words count
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		name := c.name
		if got := initialsFromName(&name); got != c.want {
			t.Errorf("initialsFromName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	if got := initialsFromName(nil); got != "" {
		t.Errorf("initialsFromName(nil) = %q, want empty (no customer name on file, e.g. a cash-sale ticket)", got)
	}
}

func TestNullIfEmpty(t *testing.T) {
	if got := nullIfEmpty("  "); got != nil {
		t.Errorf("expected whitespace-only input to become nil, got %q", *got)
	}
	if got := nullIfEmpty(""); got != nil {
		t.Errorf("expected empty input to become nil, got %q", *got)
	}
	got := nullIfEmpty("  Ada Obi  ")
	if got == nil || *got != "Ada Obi" {
		t.Errorf("expected trimmed value %q, got %v", "Ada Obi", got)
	}
}
