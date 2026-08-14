package tickets

import (
	"context"
	"strings"
	"testing"
	"time"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeRow/fakeQuerier let Purchase's INSERT be exercised (and its exact
// argument list inspected) without a real Postgres connection -- the same
// pattern internal/audit's tests use.
type fakeRow struct {
	id uuid.UUID
}

func (r fakeRow) Scan(dest ...any) error {
	if len(dest) > 0 {
		if idPtr, ok := dest[0].(*uuid.UUID); ok {
			*idPtr = r.id
		}
	}
	return nil
}

type fakeQuerier struct {
	lastArgs []any
	returnID uuid.UUID
}

func (f *fakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (f *fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (f *fakeQuerier) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	f.lastArgs = args
	return fakeRow{id: f.returnID}
}

var _ tenantdb.Querier = (*fakeQuerier)(nil)

// tickets INSERT column order (see Purchase): plant_id, reference, code,
// customer_name, customer_phone, size_grams, rate_per_kg, price_id,
// amount, channel, status, paid_at, sold_by, shift_id.
const (
	argStatus  = 10
	argPaidAt  = 11
	argSoldBy  = 12
	argShiftID = 13
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

func TestCreatePendingGoesThroughSharedPurchaseCore(t *testing.T) {
	q := &fakeQuerier{returnID: uuid.New()}
	ticket, err := CreatePending(context.Background(), q, PurchaseParams{
		PlantID: uuid.New(), PlantSlug: "sunrise", PriceID: uuid.New(),
		RatePerKgKobo: 150000, AmountKobo: 1875000, SizeGrams: 12500,
		Channel: "transfer", CustomerName: "Ada Nwosu", CustomerPhone: "08030000000",
	})
	if err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	if ticket.ID != q.returnID {
		t.Errorf("expected the ticket ID scanned from the INSERT, got %v want %v", ticket.ID, q.returnID)
	}
	if len(q.lastArgs) != 14 {
		t.Fatalf("expected 14 INSERT args, got %d", len(q.lastArgs))
	}
	if status := q.lastArgs[argStatus]; status != "pending" {
		t.Errorf("expected status 'pending' for an online purchase, got %v", status)
	}
	if paidAt := q.lastArgs[argPaidAt]; paidAt != (*time.Time)(nil) {
		t.Errorf("expected a nil paid_at for a pending purchase, got %v", paidAt)
	}
	if soldBy := q.lastArgs[argSoldBy]; soldBy != (*uuid.UUID)(nil) {
		t.Errorf("expected a nil sold_by for an online purchase (nobody at the plant sold it), got %v", soldBy)
	}
	if shiftID := q.lastArgs[argShiftID]; shiftID != (*uuid.UUID)(nil) {
		t.Errorf("expected a nil shift_id for an online purchase, got %v", shiftID)
	}
}

func TestCreatePaidCashGoesThroughSharedPurchaseCore(t *testing.T) {
	q := &fakeQuerier{returnID: uuid.New()}
	soldBy, shiftID := uuid.New(), uuid.New()
	ticket, err := CreatePaidCash(context.Background(), q, PurchaseParams{
		PlantID: uuid.New(), PlantSlug: "sunrise", PriceID: uuid.New(),
		RatePerKgKobo: 150000, AmountKobo: 1875000, SizeGrams: 12500,
		CustomerPhone: "08030000000",
		// Channel/Paid/SoldBy/ShiftID are deliberately left unset here --
		// CreatePaidCash must override them regardless of what's passed,
		// which is exactly what this test checks.
	}, soldBy, shiftID)
	if err != nil {
		t.Fatalf("CreatePaidCash: %v", err)
	}
	if ticket.ID != q.returnID {
		t.Errorf("expected the ticket ID scanned from the INSERT, got %v want %v", ticket.ID, q.returnID)
	}
	if len(q.lastArgs) != 14 {
		t.Fatalf("expected 14 INSERT args, got %d", len(q.lastArgs))
	}
	if channel := q.lastArgs[9]; channel != "cash" {
		t.Errorf("expected channel forced to 'cash', got %v", channel)
	}
	if status := q.lastArgs[argStatus]; status != "paid" {
		t.Errorf("expected status 'paid' immediately for a cash sale, got %v", status)
	}
	paidAt, ok := q.lastArgs[argPaidAt].(*time.Time)
	if !ok || paidAt == nil {
		t.Fatalf("expected a non-nil paid_at for a cash sale, got %v", q.lastArgs[argPaidAt])
	}
	if time.Since(*paidAt) > time.Minute {
		t.Errorf("expected paid_at to be ~now, got %v", paidAt)
	}
	gotSoldBy, ok := q.lastArgs[argSoldBy].(*uuid.UUID)
	if !ok || gotSoldBy == nil || *gotSoldBy != soldBy {
		t.Errorf("expected sold_by = %v, got %v", soldBy, q.lastArgs[argSoldBy])
	}
	gotShiftID, ok := q.lastArgs[argShiftID].(*uuid.UUID)
	if !ok || gotShiftID == nil || *gotShiftID != shiftID {
		t.Errorf("expected shift_id = %v, got %v", shiftID, q.lastArgs[argShiftID])
	}
}

func TestAmountFromGramsAndGramsFromAmountAgree(t *testing.T) {
	const pricePerKgKobo = int64(150000) // ₦1,500/kg, matching the seed data

	amount := AmountFromGrams(12500, pricePerKgKobo) // 12.5kg
	if want := int64(1875000); amount != want {       // ₦18,750
		t.Errorf("AmountFromGrams(12500, %d) = %d, want %d", pricePerKgKobo, amount, want)
	}

	// The online path derives grams from an amount; the terminal cash
	// path derives an amount from grams. Round-tripping through both
	// directions should land back where it started -- if it doesn't, the
	// two purchase paths could show a customer and a cashier two
	// different weights for what should be the same money.
	grams := GramsFromAmount(amount, pricePerKgKobo)
	if grams != 12500 {
		t.Errorf("GramsFromAmount(%d, %d) = %d, want 12500 (round trip)", amount, pricePerKgKobo, grams)
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
