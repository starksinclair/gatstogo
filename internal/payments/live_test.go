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

// TestLiveListBanks exercises GET /bank against the real Paystack API.
// Harmless and side-effect-free (a plain read), so this only needs
// PAYSTACK_SECRET_KEY -- unlike TestLiveResolveAndCreateSubaccount below,
// it doesn't need a real bank account to test against.
//
//	PAYSTACK_SECRET_KEY=sk_test_... go test ./internal/payments/... -run TestLiveListBanks -v
func TestLiveListBanks(t *testing.T) {
	secretKey := os.Getenv("PAYSTACK_SECRET_KEY")
	if secretKey == "" {
		t.Skip("PAYSTACK_SECRET_KEY not set; skipping live Paystack API test")
	}

	c := NewClient(secretKey)
	banks, err := c.ListBanks(context.Background())
	if err != nil {
		t.Fatalf("ListBanks against the real Paystack API failed: %v", err)
	}
	if len(banks) == 0 {
		t.Error("expected at least one bank from a real ListBanks call")
	}
	t.Logf("ListBanks OK: %d banks returned", len(banks))
}

// TestLiveResolveAndCreateSubaccount exercises ResolveAccount and
// CreateSubaccount against the real Paystack API -- the two calls this
// build's entire settlement correctness rests on (see
// PlantSettlementPercentage's own doc comment for the specific,
// financially-significant thing a mocked test alone can't fully prove:
// that Paystack's real API actually accepts and echoes back the request
// shape this client assumes).
//
// Needs a real Nigerian bank account to resolve against, on top of
// PAYSTACK_SECRET_KEY -- skipped unless both PAYSTACK_TEST_ACCOUNT_NUMBER
// and PAYSTACK_TEST_BANK_CODE are also set, since there's no safe
// synthetic account number to fabricate here (unlike ticketEmail's
// Gmail "+tag" trick for Initialize's required email). Creates one real,
// harmless test-mode subaccount at Paystack when it runs -- test-mode
// subaccounts move no real money, but this is why it isn't run
// automatically alongside the other live tests.
//
//	PAYSTACK_SECRET_KEY=sk_test_... \
//	PAYSTACK_TEST_ACCOUNT_NUMBER=0123456789 \
//	PAYSTACK_TEST_BANK_CODE=058 \
//	go test ./internal/payments/... -run TestLiveResolveAndCreateSubaccount -v
func TestLiveResolveAndCreateSubaccount(t *testing.T) {
	secretKey := os.Getenv("PAYSTACK_SECRET_KEY")
	accountNumber := os.Getenv("PAYSTACK_TEST_ACCOUNT_NUMBER")
	bankCode := os.Getenv("PAYSTACK_TEST_BANK_CODE")
	if secretKey == "" || accountNumber == "" || bankCode == "" {
		t.Skip("PAYSTACK_SECRET_KEY, PAYSTACK_TEST_ACCOUNT_NUMBER, and PAYSTACK_TEST_BANK_CODE must all be set; skipping live Paystack bank test")
	}

	c := NewClient(secretKey)

	resolved, err := c.ResolveAccount(context.Background(), accountNumber, bankCode)
	if err != nil {
		t.Fatalf("ResolveAccount against the real Paystack API failed: %v", err)
	}
	if resolved.AccountName == "" {
		t.Error("expected a non-empty account_name from a real ResolveAccount call")
	}
	t.Logf("ResolveAccount OK: account_name=%s", resolved.AccountName)

	sub, err := c.CreateSubaccount(context.Background(), CreateSubaccountParams{
		BusinessName:     "GatsToGo Live Test " + resolved.AccountName,
		SettlementBank:   bankCode,
		AccountNumber:    accountNumber,
		PercentageCharge: PlantSettlementPercentage,
	})
	if err != nil {
		t.Fatalf("CreateSubaccount against the real Paystack API failed: %v", err)
	}
	if sub.SubaccountCode == "" {
		t.Error("expected a non-empty subaccount_code from a real CreateSubaccount call")
	}
	t.Logf("CreateSubaccount OK: subaccount_code=%s (percentage_charge=%v)", sub.SubaccountCode, PlantSettlementPercentage)
}
