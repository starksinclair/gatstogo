package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Error("VerifyPassword: expected match for the correct password")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Error("VerifyPassword: expected no match for a wrong password")
	}
}

func TestVerifyPasswordEmptyHash(t *testing.T) {
	// A user row with no password_hash set yet (e.g. a cashier who only
	// has a PIN) must never verify against any input.
	if VerifyPassword("", "anything") {
		t.Error("VerifyPassword: expected false for an empty hash")
	}
}

func TestVerifyPasswordMalformedHash(t *testing.T) {
	// bcrypt.CompareHashAndPassword returns an error (not a panic) for a
	// malformed hash -- VerifyPassword must fold that into false rather
	// than letting a caller branch on "wrong password" vs "corrupt data".
	if VerifyPassword("not-a-bcrypt-hash", "anything") {
		t.Error("VerifyPassword: expected false for a malformed hash")
	}
}
