// Package audit writes to activity_log. Every column it needs
// (migrations/0001_init_schema.up.sql) has existed since the first
// migration, and it's read by both the owner dashboard's Activity tab and
// the admin console's Platform activity tab -- but before this build-out,
// nothing anywhere ever inserted a row into it. Every write path landing
// in M8-M11 calls Log as part of the same transaction as its business
// write, so the audit row and the row it's describing commit or roll back
// together.
package audit

import (
	"context"
	"encoding/json"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
)

// Log records one activity_log row. plantID/actorID are pointers because
// both columns are nullable: a payment-gateway-initiated event (e.g. a
// Paystack webhook flipping a ticket to paid) has no human actor, and a
// platform-wide event might have no single plant. detail may be nil --
// it's stored as an empty JSON object rather than SQL NULL, matching the
// column's own NOT NULL DEFAULT '{}'.
//
// q should be the same tenantdb.Querier (transaction) the surrounding
// write used, not a fresh pool handle -- passing s.AppDB directly here
// instead of the tx already in scope would let the audit row commit even
// if the write it's describing rolls back, or vice versa.
func Log(ctx context.Context, q tenantdb.Querier, plantID *uuid.UUID, actorID *uuid.UUID, action, subject string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO activity_log (plant_id, actor_id, action, subject, detail)
		VALUES ($1, $2, $3, $4, $5)
	`, plantID, actorID, action, subject, b)
	return err
}
