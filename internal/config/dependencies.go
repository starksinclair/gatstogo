package config

import (
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dependencies holds application-wide shared resources initialized at startup.
// It is passed down to HTTP handlers and domain services to provide access
// to shared infrastructure like routing and database connection pools.
type Dependencies struct {
	// Router handles HTTP request routing and middleware execution.
	Router *chi.Mux

	// AppDB is the connection pool for standard application traffic,
	// enforcing Row-Level Security (RLS) policies.
	AppDB *pgxpool.Pool

	// AdminDB is a privileged connection pool reserved for platform
	// administration that bypasses Row-Level Security (RLS).
	AdminDB *pgxpool.Pool

	// SessionManager backs staff-terminal login sessions (cookie-based).
	// In-memory store: fine for a single app instance, would need a
	// Postgres-backed store (scs/postgresstore) if this ever runs
	// multi-instance.
	SessionManager *scs.SessionManager
}
