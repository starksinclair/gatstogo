// Package buyers stores gas buyers who register interest on /get-started
// (before any plant near them is live) and counts the launch-offer spots.
package buyers

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"gatstogo/internal/plants"
	"gatstogo/internal/tickets"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OfferSpots is how many buyers get the first-order offer.
const OfferSpots = 50

var (
	ErrMissingField = errors.New("buyers: missing field")
	ErrInvalidPhone = errors.New("buyers: invalid phone")
	ErrInvalidEmail = errors.New("buyers: invalid email")
	ErrInvalidState = errors.New("buyers: invalid state")
)

type SignupParams struct {
	Name   string
	Phone  string
	Email  string
	State  string
	Source string
}

// Result reports whether the signup was new. A repeat phone number is not
// an error: the buyer is told they are already on the list.
type Result struct {
	Created   bool
	OfferRank int // 1..OfferSpots when this signup took an offer spot, else 0
}

// Signup validates and stores a buyer. The offer spot is claimed under an
// advisory lock so two simultaneous signups cannot take the same rank.
func Signup(ctx context.Context, db *pgxpool.Pool, p SignupParams) (Result, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 120 {
		return Result{}, ErrMissingField
	}
	key := tickets.NormalizePhone(p.Phone)
	if len(key) != 10 {
		return Result{}, ErrInvalidPhone
	}
	phone := "0" + key
	email := strings.TrimSpace(p.Email)
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil || len(email) > 200 {
			return Result{}, ErrInvalidEmail
		}
	}
	state := strings.TrimSpace(p.State)
	valid := false
	for _, s := range plants.NigerianStates {
		if s == state {
			valid = true
			break
		}
	}
	if !valid {
		return Result{}, ErrInvalidState
	}
	source := strings.TrimSpace(p.Source)
	if len(source) > 40 {
		source = source[:40]
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7710002)`); err != nil {
		return Result{}, err
	}
	var existing int
	err = tx.QueryRow(ctx, `SELECT 1 FROM buyer_signups WHERE phone_key = $1`, key).Scan(&existing)
	if err == nil {
		return Result{Created: false}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}

	var claimed int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM buyer_signups WHERE offer_rank IS NOT NULL`).Scan(&claimed); err != nil {
		return Result{}, err
	}
	var rank *int
	if claimed < OfferSpots {
		r := claimed + 1
		rank = &r
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO buyer_signups (name, phone, phone_key, email, state, source, offer_rank)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7)
	`, name, phone, key, email, state, source, rank); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	res := Result{Created: true}
	if rank != nil {
		res.OfferRank = *rank
	}
	return res, nil
}

// SpotsClaimed is how many of the OfferSpots have been taken.
func SpotsClaimed(ctx context.Context, db *pgxpool.Pool) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT COUNT(*) FROM buyer_signups WHERE offer_rank IS NOT NULL`).Scan(&n)
	return n, err
}
