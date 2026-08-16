package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInitialize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transaction/initialize" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk_test_secret" {
			t.Errorf("unexpected Authorization header: %s", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["reference"] != "sunrise-abc123" {
			t.Errorf("unexpected reference in request body: %v", body["reference"])
		}
		if body["amount"] != float64(143750) {
			t.Errorf("unexpected amount in request body: %v", body["amount"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"message": "Authorization URL created",
			"data": map[string]any{
				"authorization_url": "https://checkout.paystack.com/abc123",
				"access_code":       "abc123",
				"reference":         "sunrise-abc123",
			},
		})
	}))
	defer srv.Close()

	c := NewClient("sk_test_secret")
	c.BaseURL = srv.URL

	result, err := c.Initialize(context.Background(), InitializeParams{
		Email:       "sunrise-abc123@no-reply.gatstogo.local",
		AmountKobo:  143750,
		Reference:   "sunrise-abc123",
		CallbackURL: "http://sunrise.localhost:8080/tickets/sunrise-abc123/callback",
		Channels:    []string{"bank_transfer"},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.AuthorizationURL != "https://checkout.paystack.com/abc123" {
		t.Errorf("unexpected authorization_url: %s", result.AuthorizationURL)
	}
}

func TestInitializeAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  false,
			"message": "Invalid amount",
		})
	}))
	defer srv.Close()

	c := NewClient("sk_test_secret")
	c.BaseURL = srv.URL

	_, err := c.Initialize(context.Background(), InitializeParams{Email: "x@example.com", AmountKobo: -1, Reference: "r"})
	if err == nil {
		t.Fatal("expected an error for a status:false response")
	}
	var apiErr *ErrAPI
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *ErrAPI, got %T: %v", err, err)
	}
	if apiErr.Message != "Invalid amount" {
		t.Errorf("unexpected message: %s", apiErr.Message)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("unexpected status code: %d", apiErr.StatusCode)
	}
}

func TestVerify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transaction/verify/sunrise-abc123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"message": "Verification successful",
			"data": map[string]any{
				"status":    "success",
				"reference": "sunrise-abc123",
				"amount":    143750,
				"channel":   "bank_transfer",
			},
		})
	}))
	defer srv.Close()

	c := NewClient("sk_test_secret")
	c.BaseURL = srv.URL

	result, err := c.Verify(context.Background(), "sunrise-abc123")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.Status != "success" || result.AmountKobo != 143750 {
		t.Errorf("unexpected result: %+v", result)
	}
}

// TestInitializeIncludesSubaccountWhenSet confirms Initialize passes
// subaccount/bearer through to Paystack when InitializeParams.Subaccount
// is set -- the split-payment path a plant with a real Paystack subaccount
// actually uses.
func TestInitializeIncludesSubaccountWhenSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["subaccount"] != "ACCT_sunrise123" {
			t.Errorf("unexpected subaccount: %v", body["subaccount"])
		}
		if body["bearer"] != "subaccount" {
			t.Errorf("unexpected bearer: %v", body["bearer"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true, "message": "ok",
			"data": map[string]any{"authorization_url": "https://checkout.paystack.com/abc123", "access_code": "abc123", "reference": "sunrise-abc123"},
		})
	}))
	defer srv.Close()

	c := NewClient("sk_test_secret")
	c.BaseURL = srv.URL

	_, err := c.Initialize(context.Background(), InitializeParams{
		Email: "x@example.com", AmountKobo: 143750, Reference: "sunrise-abc123",
		Subaccount: "ACCT_sunrise123", Bearer: "subaccount",
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

// TestInitializeOmitsSubaccountWhenUnset confirms a plant with no
// subaccount (every pre-existing dev/test plant seeded before this build-
// out) still behaves exactly as it did before Subaccount/Bearer existed --
// no regression.
func TestInitializeOmitsSubaccountWhenUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if _, present := body["subaccount"]; present {
			t.Errorf("expected no subaccount field in the request body, got %v", body["subaccount"])
		}
		if _, present := body["bearer"]; present {
			t.Errorf("expected no bearer field in the request body, got %v", body["bearer"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true, "message": "ok",
			"data": map[string]any{"authorization_url": "https://checkout.paystack.com/abc123", "access_code": "abc123", "reference": "sunrise-abc123"},
		})
	}))
	defer srv.Close()

	c := NewClient("sk_test_secret")
	c.BaseURL = srv.URL

	_, err := c.Initialize(context.Background(), InitializeParams{Email: "x@example.com", AmountKobo: 143750, Reference: "sunrise-abc123"})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

func TestResolveAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bank/resolve" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.URL.Query().Get("account_number"); got != "0123456789" {
			t.Errorf("unexpected account_number: %s", got)
		}
		if got := r.URL.Query().Get("bank_code"); got != "058" {
			t.Errorf("unexpected bank_code: %s", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true, "message": "Account number resolved",
			"data": map[string]any{"account_number": "0123456789", "account_name": "ADA NWOSU"},
		})
	}))
	defer srv.Close()

	c := NewClient("sk_test_secret")
	c.BaseURL = srv.URL

	result, err := c.ResolveAccount(context.Background(), "0123456789", "058")
	if err != nil {
		t.Fatalf("ResolveAccount: %v", err)
	}
	if result.AccountName != "ADA NWOSU" {
		t.Errorf("unexpected account_name: %s", result.AccountName)
	}
}

// TestCreateSubaccountSendsPlantShareNotPlatformShare is the one place a
// silent off-by-inversion bug would be financially catastrophic (see
// PlantSettlementPercentage's own doc comment) -- asserts the exact
// percentage_charge value sent is 98.5 (the plant's/subaccount's share),
// not 1.5 (which would be the inverted, wrong reading of the same field).
func TestCreateSubaccountSendsPlantShareNotPlatformShare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subaccount" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["business_name"] != "Sunrise Gas Plant Nigeria Ltd" {
			t.Errorf("unexpected business_name: %v", body["business_name"])
		}
		if body["settlement_bank"] != "058" {
			t.Errorf("unexpected settlement_bank: %v", body["settlement_bank"])
		}
		if body["account_number"] != "0123456789" {
			t.Errorf("unexpected account_number: %v", body["account_number"])
		}
		if body["percentage_charge"] != 98.5 {
			t.Errorf("unexpected percentage_charge: %v (expected 98.5, the SUBACCOUNT's share -- 1.5 here would mean the plant keeps 1.5%% and GatsToGo keeps 98.5%%, the exact inverse of the intended business model)", body["percentage_charge"])
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true, "message": "Subaccount created",
			"data": map[string]any{"subaccount_code": "ACCT_sunrise123"},
		})
	}))
	defer srv.Close()

	c := NewClient("sk_test_secret")
	c.BaseURL = srv.URL

	result, err := c.CreateSubaccount(context.Background(), CreateSubaccountParams{
		BusinessName:     "Sunrise Gas Plant Nigeria Ltd",
		SettlementBank:   "058",
		AccountNumber:    "0123456789",
		PercentageCharge: PlantSettlementPercentage,
	})
	if err != nil {
		t.Fatalf("CreateSubaccount: %v", err)
	}
	if result.SubaccountCode != "ACCT_sunrise123" {
		t.Errorf("unexpected subaccount_code: %s", result.SubaccountCode)
	}
	if PlantSettlementPercentage != 98.5 {
		t.Errorf("PlantSettlementPercentage = %v, want 98.5", PlantSettlementPercentage)
	}
}

func TestListBanks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bank" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true, "message": "Banks retrieved",
			"data": []map[string]any{
				{"name": "Access Bank", "code": "044"},
				{"name": "Guaranty Trust Bank", "code": "058"},
			},
		})
	}))
	defer srv.Close()

	c := NewClient("sk_test_secret")
	c.BaseURL = srv.URL

	banks, err := c.ListBanks(context.Background())
	if err != nil {
		t.Fatalf("ListBanks: %v", err)
	}
	if len(banks) != 2 || banks[1].Code != "058" {
		t.Errorf("unexpected banks: %+v", banks)
	}
}

// TestBanksWithFallbackUsesFallbackOnFailure confirms a client that can
// never reach Paystack (a bad BaseURL, standing in for "no
// PAYSTACK_SECRET_KEY configured yet") still returns a usable, non-empty
// bank list, so local iteration on either onboarding form's Bank dropdown
// never hard-fails just because live keys aren't present.
func TestBanksWithFallbackUsesFallbackOnFailure(t *testing.T) {
	c := NewClient("sk_test_secret")
	c.BaseURL = "http://127.0.0.1:0" // deliberately unreachable

	banks := c.BanksWithFallback(context.Background())
	if len(banks) == 0 {
		t.Fatal("expected a non-empty fallback bank list")
	}
	if name := c.BankName(context.Background(), "058"); name != "Guaranty Trust Bank" {
		t.Errorf("BankName(058) = %q, want the fallback GTBank entry", name)
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	c := NewClient("sk_test_secret")
	body := []byte(`{"event":"charge.success","data":{"reference":"sunrise-abc123","status":"success","amount":143750}}`)

	mac := hmac.New(sha512.New, []byte("sk_test_secret"))
	mac.Write(body)
	validSig := hex.EncodeToString(mac.Sum(nil))

	if !c.VerifyWebhookSignature(body, validSig) {
		t.Error("expected the correctly-signed body to verify")
	}
	if c.VerifyWebhookSignature(body, "") {
		t.Error("expected an empty signature to fail")
	}
	if c.VerifyWebhookSignature(body, "0000") {
		t.Error("expected a wrong signature to fail")
	}
	if c.VerifyWebhookSignature([]byte("a different, tampered body"), validSig) {
		t.Error("expected a signature computed over a different body to fail")
	}

	other := NewClient("a-different-secret-key")
	if other.VerifyWebhookSignature(body, validSig) {
		t.Error("expected a signature computed with a different secret key to fail")
	}
}
