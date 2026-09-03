// Command gtg is the operator CLI for GatsToGo database tasks.
//
// Local Docker already runs migrations via `docker compose up`. Use this
// binary when applying the same migrations against Neon (or any hosted
// Postgres), and for status / seed:
//
//	gtg migrate              # schema + role passwords (no demo seed)
//	gtg migrate --seed       # also apply 0002_seed_data (dev/staging only)
//	gtg migrate status
//	gtg db:seed              # seed only
//
// Connection (first match wins):
//
//	MIGRATE_DATABASE_URL   Neon *direct* owner URL (preferred for migrate)
//	DATABASE_URL
//	PGHOST + PGUSER + PGPASSWORD + PGDATABASE
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gatstogo/internal/migrate"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	loadEnvFiles()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch os.Args[1] {
	case "migrate":
		if len(os.Args) >= 3 && os.Args[2] == "status" {
			if err := cmdStatus(ctx); err != nil {
				fatal(err)
			}
			return
		}
		seed := flagSet(os.Args[2:], "--seed")
		if err := cmdMigrate(ctx, seed); err != nil {
			fatal(err)
		}
	case "db:seed":
		force := flagSet(os.Args[2:], "--force")
		if err := cmdSeed(ctx, force); err != nil {
			fatal(err)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func cmdMigrate(ctx context.Context, seed bool) error {
	conn, cfg, err := open(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	cfg.SeedDevData = seed || envTruthy("SEED_DEV_DATA")
	cfg.RefuseSeedInProdEnv = true

	res, err := migrate.Up(ctx, conn, cfg)
	if err != nil {
		return err
	}
	printResult(res)
	fmt.Println("migrations complete")
	return nil
}

func cmdSeed(ctx context.Context, force bool) error {
	conn, cfg, err := open(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	cfg.RefuseSeedInProdEnv = true
	cfg.AllowProdSeed = force

	res, err := migrate.Seed(ctx, conn, cfg)
	if err != nil {
		return err
	}
	printResult(res)
	fmt.Println("seed complete")
	return nil
}

func cmdStatus(ctx context.Context) error {
	conn, cfg, err := open(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	rows, err := migrate.Status(ctx, conn, cfg)
	if err != nil {
		return err
	}
	for _, row := range rows {
		mark := "pending"
		if row.Applied {
			mark = "applied"
		}
		extra := ""
		if row.Seed {
			extra = " [seed]"
		}
		fmt.Printf("%-8s  %s%s\n", mark, row.Version, extra)
	}
	return nil
}

func open(ctx context.Context) (*pgx.Conn, migrate.Config, error) {
	dsn, err := migrateDSN()
	if err != nil {
		return nil, migrate.Config{}, err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, migrate.Config{}, fmt.Errorf("connect: %w\n(use Neon *direct* host for migrate, not the -pooler URL)", err)
	}
	dir := os.Getenv("MIGRATIONS_DIR")
	if dir == "" {
		dir = defaultMigrationsDir()
	}
	cfg := migrate.Config{
		Dir:               dir,
		AppRolePassword:   os.Getenv("GATSTOGO_APP_ROLE_PASSWORD"),
		AdminRolePassword: os.Getenv("GATSTOGO_ADMIN_ROLE_PASSWORD"),
	}
	return conn, cfg, nil
}

func migrateDSN() (string, error) {
	if u := firstNonEmpty(os.Getenv("MIGRATE_DATABASE_URL"), os.Getenv("DATABASE_URL")); u != "" {
		return u, nil
	}
	host := getenv("PGHOST", "localhost")
	user := getenv("PGUSER", "gatstogo")
	pass := os.Getenv("PGPASSWORD")
	if pass == "" {
		return "", fmt.Errorf("set MIGRATE_DATABASE_URL (Neon direct owner URL) or PGPASSWORD / DATABASE_URL")
	}
	db := getenv("PGDATABASE", "gatstogo")
	ssl := getenv("PGSSLMODE", "require")
	port := getenv("PGPORT", "5432")
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     host + ":" + port,
		Path:     "/" + db,
		RawQuery: "sslmode=" + url.QueryEscape(ssl),
	}
	return u.String(), nil
}

func defaultMigrationsDir() string {
	candidates := []string{
		"migrations",
		"/migrations",
		filepath.Join("..", "..", "migrations"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return "migrations"
}

func loadEnvFiles() {
	_ = godotenv.Load(".env")
	_ = godotenv.Load(".env.local")
	if f := os.Getenv("GTG_ENV_FILE"); f != "" {
		_ = godotenv.Overload(f)
	}
}

func printResult(res migrate.Result) {
	for _, s := range res.Skipped {
		fmt.Println("skip", s)
	}
	for _, a := range res.Applied {
		fmt.Println("ok  ", a)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `gtg — GatsToGo database CLI

Usage:
  gtg migrate              Apply pending schema migrations (no demo seed)
  gtg migrate --seed       Also apply *_seed_data.up.sql (dev/staging only)
  gtg migrate status       Show applied vs pending
  gtg db:seed              Apply seed data only
  gtg db:seed --force      Allow seed even when APP_ENV=production

Env (see docs/DATABASE.md):
  MIGRATE_DATABASE_URL     Neon direct owner URL (preferred for migrate)
  GATSTOGO_APP_ROLE_PASSWORD
  GATSTOGO_ADMIN_ROLE_PASSWORD
  SEED_DEV_DATA=true       Same as --seed for migrate
  MIGRATIONS_DIR           Default: ./migrations

Docker teammates: you usually do not need this CLI —
  cp .env.example .env && docker compose up --build
already migrates + seeds the local Postgres.
`)
}

func flagSet(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func envTruthy(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
