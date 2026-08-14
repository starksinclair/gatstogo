package payments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
)

// TestLiveInitializeAndVerify exercises Initialize and Verify against the
// real Paystack API, unlike every other test in this package (which mocks
// Paystack with an httptest server). It only runs when PAYSTACK_SECRET_KEY
// is actually set in the environment -- skipped otherwise, so `go test
// ./...` stays hermetic and doesn't need network access or real
// credentials by default (CI, a fresh clone, this sandbox before a key
// existed, etc.).
//
// Uses a Paystack *test* key: this creates a real but harmless test-mode
// transaction (never completed, no card details submitted, no money
// moves) purely to confirm the secret key is accepted and the
// request/response shapes this client assumes actually match what
// Paystack's live API returns -- something a mocked test can't catch on
// its own.
//
// Run explicitly with:
//
//	PAYSTACK_SECRET_KEY=sk_test_... go test ./internal/payments/... -run TestLiveInitializeAndVerify -v
func TestLiveInitializeAndVerify(t *testing.T) {
	secretKey := os.Getenv("PAYSTACK_SECRET_KEY")
	if secretKey == "" {
		t.Skip("PAYSTACK_SECRET_KEY not set; skipping live Paystack API test")
	}

	c := NewClient(secretKey)

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate reference suffix: %v", err)
	}
	reference := "livetest-" + hex.EncodeToString(suffix)

	result, err := c.Initialize(context.Background(), InitializeParams{
		// A real, deliverable address (Gmail "+tag" addressing, same
		// pattern as cmd/server/tickets.go's ticketEmail), not a synthetic
		// placeholder domain -- this test is what discovered Paystack
		// rejects obviously-fake domains like .invalid outright.
		Email:       "gatstogofficial+" + reference + "@gmail.com",
		AmountKobo:  50000, // ₦500 -- never actually charged, this transaction is never completed
		Reference:   reference,
		CallbackURL: "https://example.com/callback",
		Channels:    []string{"bank_transfer"},
	})
	if err != nil {
		t.Fatalf("Initialize against the real Paystack API failed: %v", err)
	}
	if result.AuthorizationURL == "" {
		t.Error("expected a non-empty authorization_url from a real Initialize call")
	}
	if result.Reference != reference {
		t.Errorf("expected Paystack to echo back reference %q, got %q", reference, result.Reference)
	}
	t.Logf("Initialize OK: reference=%s authorization_url=%s", result.Reference, result.AuthorizationURL)

	verify, err := c.Verify(context.Background(), reference)
	if err != nil {
		t.Fatalf("Verify against the real Paystack API failed: %v", err)
	}
	// Never completed on purpose -- this just confirms Verify's response
	// shape parses correctly and reports a real (non-success) status back,
	// not that AmountKobo/Reference round-trip through an actual payment.
	if verify.Status == "" {
		t.Error("expected a non-empty status from a real Verify call")
	}
	if verify.Status == "success" {
		t.Error("this transaction was never completed -- a \"success\" status here would mean the reference collided with something real")
	}
	t.Logf("Verify OK: status=%s (expected non-success -- nothing was ever paid)", verify.Status)
}
