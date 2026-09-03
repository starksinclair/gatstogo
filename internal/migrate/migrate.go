// Package migrate applies the SQL files in migrations/, tracking versions
// in schema_migrations -- the same contract migrations/migrate.sh used,
// so docker compose and `gtg migrate` (Neon / host) share one code path.
package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Config controls which migrations run and how role passwords are set.
type Config struct {
	Dir                 string
	SeedDevData         bool
	AppRolePassword     string
	AdminRolePassword   string
	AllowProdSeed       bool // only for explicit gtg db:seed --force
	RefuseSeedInProdEnv bool // when true, refuse seed if APP_ENV=production
}

// Result summarises one Up / Seed run.
type Result struct {
	Applied []string
	Skipped []string
}

// StatusRow is one migrations/*.up.sql file vs schema_migrations.
type StatusRow struct {
	Version string
	Applied bool
	Seed    bool
}

// Up applies every pending *.up.sql (seed files only when SeedDevData).
func Up(ctx context.Context, conn *pgx.Conn, cfg Config) (Result, error) {
	if err := ensureTable(ctx, conn); err != nil {
		return Result{}, err
	}
	files, err := listUpFiles(cfg.Dir)
	if err != nil {
		return Result{}, err
	}
	var out Result
	for _, file := range files {
		version := filepath.Base(file)
		seed := isSeedFile(version)
		if seed && !cfg.SeedDevData {
			out.Skipped = append(out.Skipped, version+" (seed disabled; pass --seed or SEED_DEV_DATA=true)")
			continue
		}
		if seed {
			if err := guardSeed(cfg); err != nil {
				return out, err
			}
		}
		ok, err := alreadyApplied(ctx, conn, version)
		if err != nil {
			return out, err
		}
		if ok {
			out.Skipped = append(out.Skipped, version+" (already applied)")
			continue
		}
		if err := applyOne(ctx, conn, file, version, cfg); err != nil {
			return out, err
		}
		out.Applied = append(out.Applied, version)
	}
	return out, nil
}

// Seed applies only *_seed_data.up.sql files that are not yet recorded.
func Seed(ctx context.Context, conn *pgx.Conn, cfg Config) (Result, error) {
	cfg.SeedDevData = true
	if err := guardSeed(cfg); err != nil {
		return Result{}, err
	}
	if err := ensureTable(ctx, conn); err != nil {
		return Result{}, err
	}
	files, err := listUpFiles(cfg.Dir)
	if err != nil {
		return Result{}, err
	}
	var out Result
	for _, file := range files {
		version := filepath.Base(file)
		if !isSeedFile(version) {
			continue
		}
		ok, err := alreadyApplied(ctx, conn, version)
		if err != nil {
			return out, err
		}
		if ok {
			out.Skipped = append(out.Skipped, version+" (already applied)")
			continue
		}
		if err := applyOne(ctx, conn, file, version, cfg); err != nil {
			return out, err
		}
		out.Applied = append(out.Applied, version)
	}
	if len(out.Applied) == 0 && len(out.Skipped) == 0 {
		return out, fmt.Errorf("no *_seed_data.up.sql files found in %s", cfg.Dir)
	}
	return out, nil
}

// Status lists every *.up.sql and whether it has been applied.
func Status(ctx context.Context, conn *pgx.Conn, cfg Config) ([]StatusRow, error) {
	if err := ensureTable(ctx, conn); err != nil {
		return nil, err
	}
	files, err := listUpFiles(cfg.Dir)
	if err != nil {
		return nil, err
	}
	rows := make([]StatusRow, 0, len(files))
	for _, file := range files {
		version := filepath.Base(file)
		ok, err := alreadyApplied(ctx, conn, version)
		if err != nil {
			return nil, err
		}
		rows = append(rows, StatusRow{Version: version, Applied: ok, Seed: isSeedFile(version)})
	}
	return rows, nil
}

func guardSeed(cfg Config) error {
	if !cfg.RefuseSeedInProdEnv {
		return nil
	}
	if strings.EqualFold(os.Getenv("APP_ENV"), "production") && !cfg.AllowProdSeed {
		return fmt.Errorf("refusing to seed while APP_ENV=production (known demo passwords); use a staging Neon branch, or pass --force if you really mean it")
	}
	return nil
}

func ensureTable(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     text PRIMARY KEY,
			applied_at  timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func alreadyApplied(ctx context.Context, conn *pgx.Conn, version string) (bool, error) {
	var one int
	err := conn.QueryRow(ctx, `SELECT 1 FROM schema_migrations WHERE version = $1`, version).Scan(&one)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, path, version string, cfg Config) error {
	fmt.Printf("applying %s\n", version)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if isRoleCredentialsFile(version) {
		// ALTER ROLE ... PASSWORD does not accept extended-query parameters
		// ($1); Postgres requires a string literal. Dollar-quote like psql's
		// :'var' path in migrations/migrate.sh so quotes in the password
		// cannot break the statement.
		if cfg.AppRolePassword == "" || cfg.AdminRolePassword == "" {
			return fmt.Errorf("%s requires GATSTOGO_APP_ROLE_PASSWORD and GATSTOGO_ADMIN_ROLE_PASSWORD", version)
		}
		appSQL := "ALTER ROLE gatstogo_app WITH PASSWORD " + dollarQuote(cfg.AppRolePassword)
		if _, err := tx.Exec(ctx, appSQL); err != nil {
			return fmt.Errorf("%s: alter gatstogo_app: %w", version, err)
		}
		adminSQL := "ALTER ROLE gatstogo_admin WITH PASSWORD " + dollarQuote(cfg.AdminRolePassword)
		if _, err := tx.Exec(ctx, adminSQL); err != nil {
			return fmt.Errorf("%s: alter gatstogo_admin: %w", version, err)
		}
	} else {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("%s: %w", version, err)
		}
	}

	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return fmt.Errorf("record %s: %w", version, err)
	}
	return tx.Commit(ctx)
}

func listUpFiles(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no *.up.sql files in %s", dir)
	}
	sort.Strings(matches)
	return matches, nil
}

func isSeedFile(name string) bool {
	return strings.Contains(name, "_seed_data.up.sql")
}

func isRoleCredentialsFile(name string) bool {
	return strings.Contains(name, "_role_credentials.up.sql")
}

// dollarQuote returns a Postgres dollar-quoted string literal for s.
func dollarQuote(s string) string {
	tag := "gtg"
	for strings.Contains(s, "$"+tag+"$") {
		tag += "x"
	}
	return "$" + tag + "$" + s + "$" + tag + "$"
}
