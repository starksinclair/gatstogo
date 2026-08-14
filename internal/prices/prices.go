// Package prices implements price-setting. prices is immutable history
// (migrations/0001_init_schema.up.sql) but, before this build-out,
// nothing anywhere ever inserted a row into it beyond the one seeded at
// setup -- the owner dashboard's "Set new rate" panel was decorative.
package prices

import (
	"context"
	"errors"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
)

var ErrInvalidPrice = errors.New("prices: price per kg must be greater than zero")

// Set inserts a new price row, effective immediately (effective_from
// defaults to now() at the database level). Prices are never updated or
// deleted -- every change is a new row, so a ticket's snapshotted
// price_id always resolves to the rate that was actually in effect when
// it was created, even after the price changes again later.
//
// Scheduling a *future* price change isn't supported here: the owner
// dashboard's existing "Current price" query (loadCurrentPrice,
// cmd/server/tickets.go) just takes the row with the latest
// effective_from with no "effective_from <= now()" filter, so a
// future-dated row would become "current" immediately, not later --
// exposing an effective_from override on this form would be actively
// wrong until that read-side query is also fixed. Every price this
// function writes takes effect immediately.
func Set(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, pricePerKgKobo int64, setBy uuid.UUID) (uuid.UUID, error) {
	if pricePerKgKobo <= 0 {
		return uuid.Nil, ErrInvalidPrice
	}
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		INSERT INTO prices (plant_id, price_per_kg, set_by)
		VALUES ($1, $2, $3)
		RETURNING id
	`, plantID, pricePerKgKobo, setBy).Scan(&id)
	return id, err
}
