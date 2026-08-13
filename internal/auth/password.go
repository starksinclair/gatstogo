// Package auth provides password/PIN hashing. users.password_hash (owner/
// manager/admin) and users.pin_hash (cashier/operator) both exist in the
// schema (migrations/0001_init_schema.up.sql) but, before this build-out,
// nothing anywhere ever read or wrote either column -- there was no login
// of any kind. Both use the same bcrypt-based hashing here; a PIN is just a
// short secret, not a cryptographically different kind of thing.
package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword bcrypt-hashes a plaintext password (or PIN) for storage in
// users.password_hash / users.pin_hash.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword reports whether plain matches the given bcrypt hash. A
// malformed or mismatched hash returns false, never an error -- callers
// shouldn't be able to distinguish "wrong password" from "corrupt hash" by
// branching on an error value.
func VerifyPassword(hash, plain string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
