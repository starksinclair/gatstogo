// Package session implements server-side login sessions backed by Redis.
// Redis was chosen (over a Postgres table or stateless signed cookies)
// specifically so app servers can be horizontally scaled behind a shared,
// fast session store, and so a session can be revoked immediately (force
// logout, end-of-shift) without waiting for a client-held token to expire.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Role mirrors the users.role check constraint in
// migrations/0001_init_schema.up.sql.
type Role string

const (
	RoleOwner    Role = "owner"
	RoleManager  Role = "manager"
	RoleCashier  Role = "cashier"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

// Data is what's stored in Redis for a session, keyed by its token.
type Data struct {
	UserID  string `json:"user_id"`
	Role    Role   `json:"role"`
	Name    string `json:"name"`
	// PlantID is deliberately "" for admin sessions. Even though every
	// users row (including role='admin' ones) carries a NOT NULL plant_id
	// in the schema, admin access is platform-wide -- this empty string is
	// the actual mechanism that keeps an admin session from being treated
	// as scoped to whichever plant that admin user's row happens to live
	// under, matching /admin's existing placement outside the tenant
	// middleware group.
	PlantID string `json:"plant_id"`
	// ShiftID is set only for staff terminal sessions (cashier/operator),
	// once a shift has been opened. Empty until then.
	ShiftID   string    `json:"shift_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrNotFound is returned by Load/Touch when a token has no session --
// either it never existed, or it already expired/was deleted.
var ErrNotFound = errors.New("session: not found")

const keyPrefix = "session:"

const (
	// OwnerTTL is the sliding idle timeout for owner/manager/admin
	// (password-login) sessions. Reset on every authenticated request via
	// Touch, so an active user is never logged out mid-session.
	OwnerTTL = 12 * time.Hour

	// StaffBackstopTTL is a leak-prevention ceiling for terminal
	// (PIN-login) sessions -- a device left on, a crashed tab. It is
	// deliberately NOT the primary expiry mechanism: those sessions are
	// meant to live exactly as long as the shift they're tied to, and are
	// deleted explicitly the instant shift-close succeeds (internal/shifts).
	// This is just a floor under that, in case a shift is never closed.
	StaffBackstopTTL = 24 * time.Hour
)

// Store is a thin wrapper over a Redis client for session CRUD.
type Store struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Store {
	return &Store{rdb: rdb}
}

// Create mints a new random session token, stores data under it with the
// given TTL, and returns the token -- the raw value that goes in the
// session cookie (see cookie.go). 256 bits of crypto/rand entropy, so the
// token itself is unguessable; nothing else needs to be secret about it.
func (s *Store) Create(ctx context.Context, data Data, ttl time.Duration) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	data.CreatedAt = time.Now().UTC()

	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, keyPrefix+token, b, ttl).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// Load fetches a session's data. Returns ErrNotFound if the token is
// missing or expired.
func (s *Store) Load(ctx context.Context, token string) (Data, error) {
	b, err := s.rdb.Get(ctx, keyPrefix+token).Bytes()
	if errors.Is(err, redis.Nil) {
		return Data{}, ErrNotFound
	}
	if err != nil {
		return Data{}, err
	}
	var data Data
	if err := json.Unmarshal(b, &data); err != nil {
		return Data{}, err
	}
	return data, nil
}

// Touch resets a session's TTL (sliding-window expiry) without changing its
// stored data. Returns ErrNotFound if the token doesn't exist.
func (s *Store) Touch(ctx context.Context, token string, ttl time.Duration) error {
	ok, err := s.rdb.Expire(ctx, keyPrefix+token, ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// SetShift attaches a shift id to an existing staff session (called once a
// PIN login successfully opens a shift) and applies the backstop TTL.
func (s *Store) SetShift(ctx context.Context, token, shiftID string) error {
	data, err := s.Load(ctx, token)
	if err != nil {
		return err
	}
	data.ShiftID = shiftID
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, keyPrefix+token, b, StaffBackstopTTL).Err()
}

// Delete ends a session immediately (logout, or shift-close for staff
// sessions). Deleting a token that doesn't exist is not an error.
func (s *Store) Delete(ctx context.Context, token string) error {
	return s.rdb.Del(ctx, keyPrefix+token).Err()
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
