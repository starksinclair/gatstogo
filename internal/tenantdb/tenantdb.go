// Package tenantdb scopes Postgres access to a single tenant (plant) so the
// database's own row-level-security policies -- defined in
// migrations/0001_init_schema.up.sql, keyed off the app.current_plant_id
// session variable -- actually do something. Before this package existed,
// nothing in the application ever set that variable, so RLS was silently
// inert no matter which role a connection used.
package tenantdb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the minimal read/write surface handlers and data-loading
// helpers need. It is satisfied, unmodified, by *pgxpool.Pool, *pgxpool.Conn,
// and pgx.Tx -- so a function written against Querier works whether it's
// handed a whole pool (e.g. the admin console's genuinely cross-tenant
// reads) or a tenant-scoped transaction from WithTenant below.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = (*pgxpool.Conn)(nil)
	_ Querier = (pgx.Tx)(nil)
)

// WithTenant acquires a connection from pool, opens a transaction, scopes
// that transaction to plantID via
//
//	SELECT set_config('app.current_plant_id', $1, true)
//
// (the third argument, is_local, gives SET LOCAL semantics: the setting is
// visible only inside this transaction and is unconditionally cleared the
// instant it commits or rolls back), then runs fn with that transaction.
//
// The is_local scoping is what makes this safe to use with a pooled
// connection: once the transaction ends, the physical connection goes back
// into the pool with no leftover session state for some unrelated later
// request to inherit.
//
// Any error returned by fn -- or by the commit itself -- rolls the whole
// transaction back, so a handler that writes several rows and then fails
// never leaves a partial write behind.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, plantID uuid.UUID, fn func(ctx context.Context, q Querier) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("tenantdb: acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("tenantdb: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx) // no-op if already committed; best-effort otherwise
		}
	}()

	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_plant_id', $1, true)`, plantID.String()); err != nil {
		return fmt.Errorf("tenantdb: set tenant scope: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("tenantdb: commit: %w", err)
	}
	committed = true
	return nil
}
