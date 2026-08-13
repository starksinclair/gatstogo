package audit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeQuerier is a minimal tenantdb.Querier that just records the last
// Exec call, so Log's SQL/argument shape can be checked without a real
// Postgres connection.
type fakeQuerier struct {
	execArgs []any
}

func (f *fakeQuerier) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.execArgs = args
	return pgconn.CommandTag{}, nil
}
func (f *fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (f *fakeQuerier) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }

func TestLog(t *testing.T) {
	q := &fakeQuerier{}
	plantID := uuid.New()
	actorID := uuid.New()

	err := Log(context.Background(), q, &plantID, &actorID, "ticket.created", "ref-123", map[string]any{"amount": 1000})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(q.execArgs) != 5 {
		t.Fatalf("expected 5 args (plant_id, actor_id, action, subject, detail), got %d", len(q.execArgs))
	}
	if q.execArgs[0] != &plantID {
		t.Errorf("expected plant_id arg to be the plantID pointer passed in")
	}
	if q.execArgs[2] != "ticket.created" {
		t.Errorf("expected action arg %q, got %v", "ticket.created", q.execArgs[2])
	}
	if q.execArgs[3] != "ref-123" {
		t.Errorf("expected subject arg %q, got %v", "ref-123", q.execArgs[3])
	}

	detailBytes, ok := q.execArgs[4].([]byte)
	if !ok {
		t.Fatalf("expected detail arg to be []byte (marshaled JSON), got %T", q.execArgs[4])
	}
	var detail map[string]any
	if err := json.Unmarshal(detailBytes, &detail); err != nil {
		t.Fatalf("detail did not unmarshal as JSON: %v", err)
	}
	if detail["amount"] != float64(1000) {
		t.Errorf("expected detail.amount == 1000, got %v", detail["amount"])
	}
}

func TestLogNilDetailAndActor(t *testing.T) {
	q := &fakeQuerier{}
	// A payment-gateway-initiated event has no human actor, and this test
	// also exercises passing a nil detail map.
	if err := Log(context.Background(), q, nil, nil, "ticket.paid", "ref-456", nil); err != nil {
		t.Fatalf("Log: %v", err)
	}
	// execArgs[0]/[1] are `any` holding a typed (*uuid.UUID)(nil), not an
	// untyped nil -- comparing directly against nil would always be true
	// regardless of the pointer's value, so assert the concrete type first.
	if p, ok := q.execArgs[0].(*uuid.UUID); !ok || p != nil {
		t.Errorf("expected plant_id arg to be a nil *uuid.UUID, got %#v", q.execArgs[0])
	}
	if p, ok := q.execArgs[1].(*uuid.UUID); !ok || p != nil {
		t.Errorf("expected actor_id arg to be a nil *uuid.UUID, got %#v", q.execArgs[1])
	}
	detailBytes := q.execArgs[4].([]byte)
	if string(detailBytes) != "{}" {
		t.Errorf("expected a nil detail map to marshal as {}, got %s", detailBytes)
	}
}
