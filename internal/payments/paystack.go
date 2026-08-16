// Package payments is a minimal Paystack API client: just the two calls
// this app needs (Initialize a transaction, Verify one) plus webhook
// signature verification. It talks to Paystack's HTTP API directly with
// net/http rather than depending on an unofficial/unmaintained Go SDK.
//
// Paystack was chosen over Flutterwave for this integration: better
// documentation and reliability track record, HMAC-signed webhooks, and
// its bank_transfer/ussd/card channels map directly onto the payment
// options already present in the customer-facing buy form.
//
// Cross-checked against Paystack's public docs (paystack.com/docs) on top
// of the live-API test in live_test.go, specifically for webhook handling
// (cmd/server/tickets.go's paystackWebhookHandler):
//   - Signature: HMAC-SHA512 of the raw request body, hex-encoded, in the
//     X-Paystack-Signature header (HTTP header names are case-insensitive,
//     so this matches Paystack's own lowercase x-paystack-signature).
//   - Ack window: Paystack expects a response within 30 seconds; slow work
//     should be deferred rather than done inline. This app's webhook
//     handler does a small, fast DB transaction inline (a handful of
//     single-row queries), which comfortably fits -- worth revisiting if
//     that ever stops being true (e.g. under DB contention).
//   - Retries: a non-2xx response is retried every 3 minutes for the
//     first 4 tries, then hourly for 72 hours (live mode); hourly for 72
//     hours in test mode. This is why paystackWebhookHandler deliberately
//     returns 200 for "understood but not actionable" cases (unknown
//     reference, wrong event type, a verify mismatch) but 5xx for genuine
//     processing failures -- retrying fixes the latter, not the former.
//   - Paystack's own guidance is to independently re-confirm a webhook via
//     the Verify API rather than trust the webhook body's own status/
//     amount fields at face value, even after the signature checks out --
//     paystackWebhookHandler does this.
//   - Paystack also supports whitelisting up to 10 known-good source IPs
//     per environment from the dashboard (their documented webhook IPs:
//     52.31.139.75, 52.49.173.169, 52.214.14.220, for both test and live).
//     Deliberately NOT enforced in code here: doing that correctly depends
//     on the production deployment's reverse-proxy/load-balancer topology
//     (get it wrong and a real webhook's client IP silently isn't
//     Paystack's anymore), which isn't known at this layer. Recommended as
//     a deploy-time hardening step on top of the signature check, not a
//     replacement for it.
package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const defaultBaseURL = "https://api.paystack.co"

// Client is a Paystack API client scoped to one secret key.
type Client struct {
	SecretKey  string
	BaseURL    string // defaults to the real Paystack API; overridden in tests
	HTTPClient *http.Client

	// bankList* cache BanksWithFallback's live result -- per-Client rather
	// than a package-level global specifically so two Client instances
	// (e.g. in tests) never see each other's cached list; Client is always
	// used as a pointer (never copied by value) so embedding a mutex here
	// directly is safe.
	bankListMu       sync.Mutex
	bankListCache    []Bank
	bankListCachedAt time.Time
}

func NewClient(secretKey string) *Client {
	return &Client{
		SecretKey:  secretKey,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// InitializeParams are the fields this app sets on a
// transaction/initialize call.
type InitializeParams struct {
	Email       string
	AmountKobo  int64
	Reference   string
	CallbackURL string
	Channels    []string

	// Subaccount is the selling plant's Paystack subaccount code
	// (plants.paystack_subaccount_code), set once a plant has completed
	// real onboarding (internal/plants.CreateParams.PaystackSubaccountCode)
	// -- when present, this transaction settles as a split payment: the
	// plant's own bank account gets its share directly from Paystack, no
	// manual payout ever needed. Left empty for a plant with no subaccount
	// (pre-existing dev/test plants seeded before this build-out), in
	// which case Initialize omits both this and Bearer entirely and the
	// whole transaction settles to GatsToGo's own account exactly as it
	// always has -- no regression for those plants.
	Subaccount string
	// Bearer controls who absorbs Paystack's own processing fee on a split
	// transaction ("account" | "subaccount" | "all") -- this app always
	// passes "subaccount" (the plant absorbs it from its share), matching
	// the business decision recorded in internal/plants.CreateParams's own
	// doc comment. Only meaningful when Subaccount is set.
	Bearer string
}

// InitializeResult is what a caller needs back: where to send the
// customer to actually pay.
type InitializeResult struct {
	AuthorizationURL string `json:"authorization_url"`
	AccessCode       string `json:"access_code"`
	Reference        string `json:"reference"`
}

// Initialize starts a Paystack transaction and returns the URL to redirect
// the customer's browser to.
func (c *Client) Initialize(ctx context.Context, p InitializeParams) (*InitializeResult, error) {
	fields := map[string]any{
		"email":        p.Email,
		"amount":       p.AmountKobo,
		"reference":    p.Reference,
		"callback_url": p.CallbackURL,
		"channels":     p.Channels,
	}
	// Only included when a subaccount is actually set -- see
	// InitializeParams.Subaccount's own doc comment for why an empty value
	// here must mean "behave exactly as before this field existed", not
	// "split to a subaccount with an empty code".
	if p.Subaccount != "" {
		fields["subaccount"] = p.Subaccount
		if p.Bearer != "" {
			fields["bearer"] = p.Bearer
		}
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}

	var out apiEnvelope[InitializeResult]
	if err := c.do(ctx, http.MethodPost, "/transaction/initialize", body, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// VerifyResult is the subset of Paystack's verify response this app acts
// on. Field names/shape confirmed directly against Paystack's real API via
// internal/payments/live_test.go, and cross-checked against their public
// docs (paystack.com/docs/api/transaction/).
type VerifyResult struct {
	Status          string `json:"status"` // "success", "failed", "abandoned", ...
	Reference       string `json:"reference"`
	AmountKobo      int64  `json:"amount"`
	Channel         string `json:"channel"`
	Currency        string `json:"currency"`
	GatewayResponse string `json:"gateway_response"` // human-readable reason, useful in logs for a non-"success" status
}

// Verify checks a transaction's actual status directly with Paystack. Used
// as the fallback confirmation path when the customer's browser lands back
// on the callback URL -- the webhook (VerifyWebhookSignature below) is the
// primary confirmation path, but the two are expected to race, which is
// why ticket status transitions (internal/tickets.MarkPaid) are idempotent.
func (c *Client) Verify(ctx context.Context, reference string) (*VerifyResult, error) {
	var out apiEnvelope[VerifyResult]
	path := "/transaction/verify/" + url.PathEscape(reference)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// PlantSettlementPercentage is the share of every transaction a plant's
// Paystack subaccount keeps -- 98.5%, leaving GatsToGo a 1.5% commission.
// A single, fixed, platform-wide constant this round, not per-plant
// configurable.
//
// This is deliberately NOT 1.5: Paystack's percentage_charge on a
// subaccount is the SUBACCOUNT's own share, not the platform's cut -- the
// opposite of what the field name might suggest at a glance. Confirmed
// against a worked example in Paystack's own split-payment documentation
// before use here, given how costly getting this backwards would be (it
// would hand a plant 1.5% of every sale and GatsToGo the other 98.5%, the
// exact inverse of the intended business model). See CreateSubaccountParams
// below for where this is actually sent.
const PlantSettlementPercentage = 98.5

// ResolveAccountResult is Paystack's response to GET /bank/resolve --
// AccountName is the real, bank-verified name on the account, not
// anything the caller supplied.
type ResolveAccountResult struct {
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
}

// ResolveAccount confirms a bank account number is real and returns the
// name on it -- a free Paystack call, used both for the live "does this
// account exist" preview both onboarding forms' JS shows before
// submission (GET /accounts/resolve, cmd/server/accounts.go) and,
// authoritatively, server-side by cmd/server's provisionPlant immediately
// before CreateSubaccount: the resolved AccountName is what actually gets
// written to plants.bank_account_name, never anything typed by a visitor.
func (c *Client) ResolveAccount(ctx context.Context, accountNumber, bankCode string) (*ResolveAccountResult, error) {
	path := "/bank/resolve?account_number=" + url.QueryEscape(accountNumber) + "&bank_code=" + url.QueryEscape(bankCode)
	var out apiEnvelope[ResolveAccountResult]
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// CreateSubaccountParams are the fields this app sets on a
// POST /subaccount call. PercentageCharge should always be
// PlantSettlementPercentage -- passed explicitly (not hardcoded inside
// CreateSubaccount) so a test can assert on the exact value actually sent,
// the one place a silent off-by-inversion bug here would be financially
// catastrophic and worth double-checking mechanically, not just by
// reasoning about the code.
type CreateSubaccountParams struct {
	BusinessName     string
	SettlementBank   string // Paystack's own bank code (see Bank.Code), not a NIBSS institution code
	AccountNumber    string
	PercentageCharge float64
}

// CreateSubaccountResult is the subset of Paystack's subaccount response
// this app needs back.
type CreateSubaccountResult struct {
	SubaccountCode string `json:"subaccount_code"`
}

// CreateSubaccount registers a plant's bank account with Paystack as a
// split-payment destination. Every ticket this plant sells afterward
// (cmd/server/tickets.go's buyGasSubmitHandler) settles straight to this
// subaccount, minus PlantSettlementPercentage's complement, with no manual
// payout step ever involved.
func (c *Client) CreateSubaccount(ctx context.Context, p CreateSubaccountParams) (*CreateSubaccountResult, error) {
	body, err := json.Marshal(map[string]any{
		"business_name":     p.BusinessName,
		"settlement_bank":   p.SettlementBank,
		"account_number":    p.AccountNumber,
		"percentage_charge": p.PercentageCharge,
	})
	if err != nil {
		return nil, err
	}

	var out apiEnvelope[CreateSubaccountResult]
	if err := c.do(ctx, http.MethodPost, "/subaccount", body, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Bank is one entry from Paystack's supported bank list -- Code is
// Paystack's own bank code (e.g. GTBank = "058"), a different, incompatible
// numbering from the 6-digit NIBSS institution-code system some other
// sources document (e.g. GTBank = "000013") -- cross-checked against a
// source specifically documenting Paystack's own bank list before use
// here, and the NIBSS-style list rejected.
type Bank struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

// ListBanks fetches Paystack's live supported-bank list for Nigeria (NGN).
func (c *Client) ListBanks(ctx context.Context) ([]Bank, error) {
	var out apiEnvelope[[]Bank]
	if err := c.do(ctx, http.MethodGet, "/bank?currency=NGN", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// bankListTTL is how long BanksWithFallback caches a live bank list before
// fetching again -- Paystack's supported-bank list changes rarely, and
// this is fetched on every render of a form with a Bank dropdown
// otherwise.
const bankListTTL = 1 * time.Hour

// BanksWithFallback returns Paystack's live bank list, cached in-memory
// for bankListTTL. Falls back to fallbackBanks (below) when the live call
// fails or returns nothing -- e.g. no PAYSTACK_SECRET_KEY configured yet
// in a local/dev environment -- so local iteration on either onboarding
// form's Bank dropdown never hard-fails just because live keys aren't
// present. Live data always wins when it's actually available.
func (c *Client) BanksWithFallback(ctx context.Context) []Bank {
	c.bankListMu.Lock()
	if len(c.bankListCache) > 0 && time.Since(c.bankListCachedAt) < bankListTTL {
		cached := c.bankListCache
		c.bankListMu.Unlock()
		return cached
	}
	c.bankListMu.Unlock()

	banks, err := c.ListBanks(ctx)
	if err != nil || len(banks) == 0 {
		return fallbackBanks
	}

	c.bankListMu.Lock()
	c.bankListCache = banks
	c.bankListCachedAt = time.Now()
	c.bankListMu.Unlock()
	return banks
}

// BankName looks up a bank's display name from its Paystack code within
// BanksWithFallback's result -- used server-side (cmd/server's
// provisionPlant) to derive plants.bank_name from the submitted bank_code,
// rather than trusting a client-supplied name field, the same "don't trust
// the client for what the server can derive authoritatively" principle
// bank_account_name already follows via ResolveAccount. Returns "" if code
// isn't found in either the live or fallback list.
func (c *Client) BankName(ctx context.Context, code string) string {
	for _, b := range c.BanksWithFallback(ctx) {
		if b.Code == code {
			return b.Name
		}
	}
	return ""
}

// fallbackBanks seeds ~20 major Nigerian banks for when a live GET /bank
// call isn't possible (see BanksWithFallback). Codes are Paystack's own
// (see Bank.Code's doc comment on the NIBSS mix-up this specifically
// avoids), cross-checked against a source documenting Paystack's bank list
// directly.
var fallbackBanks = []Bank{
	{Name: "Access Bank", Code: "044"},
	{Name: "Citibank Nigeria", Code: "023"},
	{Name: "Ecobank Nigeria", Code: "050"},
	{Name: "Fidelity Bank", Code: "070"},
	{Name: "First Bank of Nigeria", Code: "011"},
	{Name: "First City Monument Bank", Code: "214"},
	{Name: "Guaranty Trust Bank", Code: "058"},
	{Name: "Heritage Bank", Code: "030"},
	{Name: "Jaiz Bank", Code: "301"},
	{Name: "Keystone Bank", Code: "082"},
	{Name: "Kuda Bank", Code: "50211"},
	{Name: "PalmPay", Code: "999991"},
	{Name: "Polaris Bank", Code: "076"},
	{Name: "Providus Bank", Code: "101"},
	{Name: "Stanbic IBTC Bank", Code: "221"},
	{Name: "Standard Chartered Bank", Code: "068"},
	{Name: "Sterling Bank", Code: "232"},
	{Name: "SunTrust Bank", Code: "100"},
	{Name: "Union Bank of Nigeria", Code: "032"},
	{Name: "United Bank for Africa", Code: "033"},
	{Name: "Unity Bank", Code: "215"},
	{Name: "Wema Bank", Code: "035"},
	{Name: "Zenith Bank", Code: "057"},
}

// VerifyWebhookSignature reports whether signatureHex (the value of the
// X-Paystack-Signature header) is the correct HMAC-SHA512 of body, hex
// encoded, using this client's secret key. body must be the exact raw
// request bytes -- Paystack signs over what it actually sent, not a
// re-marshaled version of it, so this must be called before any
// json.Unmarshal of the request body.
func (c *Client) VerifyWebhookSignature(body []byte, signatureHex string) bool {
	if signatureHex == "" {
		return false
	}
	mac := hmac.New(sha512.New, []byte(c.SecretKey))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signatureHex)) == 1
}

// WebhookEvent is the subset of a Paystack webhook payload this app reads.
type WebhookEvent struct {
	Event string `json:"event"`
	Data  struct {
		Reference string `json:"reference"`
		Status    string `json:"status"`
		Amount    int64  `json:"amount"`
	} `json:"data"`
}

type apiEnvelope[T any] struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// ErrAPI wraps a non-2xx or status:false Paystack response with its
// message, so callers and logs get something actionable instead of a bare
// "unexpected status code".
type ErrAPI struct {
	StatusCode int
	Message    string
}

func (e *ErrAPI) Error() string {
	return fmt.Sprintf("paystack: %s (http %d)", e.Message, e.StatusCode)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.SecretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	var envelope struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(respBody, &envelope)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Status {
		msg := envelope.Message
		if msg == "" {
			msg = "unexpected response from Paystack"
		}
		return &ErrAPI{StatusCode: resp.StatusCode, Message: msg}
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("paystack: decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
