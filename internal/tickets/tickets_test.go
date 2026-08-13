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
