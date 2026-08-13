package csrf

import "testing"

func TestTokenAndVerify(t *testing.T) {
	old := Secret
	Secret = []byte("test-secret-do-not-use-in-prod")
	defer func() { Secret = old }()

	token, err := Token("session-abc")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if !Verify("session-abc", token) {
		t.Error("Verify: expected match for the correct session's token")
	}
	if Verify("session-xyz", token) {
		t.Error("Verify: a token for one session must not verify for another")
	}
	if Verify("session-abc", "") {
		t.Error("Verify: an empty submitted token must never match")
	}
}

func TestTokenNoSecret(t *testing.T) {
	old := Secret
	Secret = nil
	defer func() { Secret = old }()

	if _, err := Token("anything"); err != ErrNoSecret {
		t.Errorf("Token: expected ErrNoSecret, got %v", err)
	}
	if Verify("anything", "some-token") {
		t.Error("Verify: must fail closed when Secret isn't configured")
	}
}

func TestVerifyPublic(t *testing.T) {
	token, err := NewPublicToken()
	if err != nil {
		t.Fatalf("NewPublicToken: %v", err)
	}
	if !VerifyPublic(token, token) {
		t.Error("VerifyPublic: expected match for identical values")
	}
	if VerifyPublic(token, "something-else") {
		t.Error("VerifyPublic: expected no match for a different value")
	}
	if VerifyPublic("", token) {
		t.Error("VerifyPublic: an empty cookie value must never match")
	}
	if VerifyPublic(token, "") {
		t.Error("VerifyPublic: an empty submitted value must never match")
	}
}
