package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"

	"gatstogo/internal/auth"

	"github.com/jackc/pgx/v5"
	"golang.org/x/term"
)

const (
	minAdminPasswordLength = 14
	devAdminPhone          = "08000000000"
	platformPlantSlug      = "gatstogo-platform"
)

// validateAdminInput checks the values create-admin accepts. The password is
// never echoed back in an error message.
func validateAdminInput(name, phone, email, password string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("--name is required")
	}
	digits := phone
	if len(digits) != 11 || strings.Trim(digits, "0123456789") != "" || !strings.HasPrefix(digits, "0") {
		return errors.New("--phone must be an 11-digit Nigerian number such as 08031234567")
	}
	if phone == devAdminPhone {
		return errors.New("--phone is the demo admin's number; use your own")
	}
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			return errors.New("--email is not a valid email address")
		}
	}
	if len([]rune(password)) < minAdminPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minAdminPasswordLength)
	}
	lower := strings.ToLower(password)
	if strings.Contains(lower, "platform-admin-dev") || strings.Contains(lower, "password") {
		return errors.New("password is too guessable")
	}
	if strings.Contains(password, phone) || strings.Contains(password, strings.TrimPrefix(phone, "0")) {
		return errors.New("password must not contain the phone number")
	}
	if allSameRune(password) {
		return errors.New("password is too guessable")
	}
	return nil
}

func allSameRune(s string) bool {
	r := []rune(s)
	for _, c := range r {
		if c != r[0] {
			return false
		}
	}
	return len(r) > 0
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimSpace(strings.TrimPrefix(a, name+"="))
		}
	}
	return ""
}

func isProduction() bool {
	return strings.EqualFold(os.Getenv("APP_ENV"), "production")
}

func readPasswordTwice() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("create-admin must run in an interactive terminal so the password can be typed hidden")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("passwords do not match")
	}
	return string(a), nil
}

func confirm(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt+" [y/N]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

func cmdCreateAdmin(ctx context.Context, args []string) error {
	name := flagValue(args, "--name")
	phone := flagValue(args, "--phone")
	email := flagValue(args, "--email")
	if err := validateAdminInput(name, phone, email, strings.Repeat("x", minAdminPasswordLength)); err != nil {
		if !strings.Contains(err.Error(), "password") {
			return err
		}
	}

	password, err := readPasswordTwice()
	if err != nil {
		return err
	}
	if err := validateAdminInput(name, phone, email, password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	conn, _, err := open(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var existing int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND phone = $1`, phone).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return fmt.Errorf("an admin with phone %s already exists", phone)
	}

	var devAdmins int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND phone = $1`, devAdminPhone).Scan(&devAdmins); err != nil {
		return err
	}
	if devAdmins > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: the demo admin %s (password in the README) exists in this database.\nRun `gtg delete-admin --phone %s` once your own admin works.\n", devAdminPhone, devAdminPhone)
	}

	if !flagSet(args, "--yes") && !confirm(fmt.Sprintf("Create platform admin %q (%s) in this database?", name, phone)) {
		return errors.New("cancelled")
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// users.plant_id is NOT NULL, so admins hang off an internal, closed
	// plant. Admin access is platform-wide regardless (see session.Data).
	var plantID string
	err = tx.QueryRow(ctx, `SELECT id FROM plants WHERE slug = $1`, platformPlantSlug).Scan(&plantID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO plants (slug, name, status) VALUES ($1, 'GatsToGo Platform', 'closed') RETURNING id
		`, platformPlantSlug).Scan(&plantID)
	}
	if err != nil {
		return err
	}

	var userID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (plant_id, name, phone, email, role, password_hash, active)
		VALUES ($1, $2, $3, NULLIF($4, ''), 'admin', $5, true) RETURNING id
	`, plantID, name, phone, email, hash).Scan(&userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO activity_log (plant_id, actor_id, action, subject, detail)
		VALUES (NULL, NULL, 'admin.created', $1, '{"via":"gtg create-admin"}')
	`, phone); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("created platform admin %s (%s)\n", name, phone)
	return nil
}

func cmdDeleteAdmin(ctx context.Context, args []string) error {
	phone := flagValue(args, "--phone")
	if phone == "" {
		return errors.New("--phone is required")
	}
	conn, _, err := open(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var total, matching int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE phone = $1) FROM users WHERE role = 'admin' AND active`, phone).Scan(&total, &matching); err != nil {
		return err
	}
	if matching == 0 {
		return fmt.Errorf("no admin with phone %s", phone)
	}
	if total-matching < 1 {
		return errors.New("refusing to delete the only platform admin; create another first")
	}
	if !flagSet(args, "--yes") && !confirm(fmt.Sprintf("Delete platform admin %s?", phone)) {
		return errors.New("cancelled")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Keep history: activity_log.actor_id references users, so deactivate
	// and blank the credentials instead of deleting the row.
	if _, err := tx.Exec(ctx, `
		UPDATE users SET active = false, password_hash = NULL, phone = 'deleted-' || id::text
		WHERE role = 'admin' AND active AND phone = $1
	`, phone); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO activity_log (plant_id, actor_id, action, subject, detail)
		VALUES (NULL, NULL, 'admin.deleted', $1, '{"via":"gtg delete-admin"}')
	`, phone); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("removed platform admin %s\n", phone)
	return nil
}
