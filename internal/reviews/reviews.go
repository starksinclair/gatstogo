package reviews

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound         = errors.New("reviews: not found")
	ErrAlreadySubmitted = errors.New("reviews: already submitted")
	ErrInvalidRating    = errors.New("reviews: rating must be between 1 and 5")
)

type Request struct {
	ID       uuid.UUID
	Token    string
	TicketID uuid.UUID
}

type Target struct {
	Token           string
	TicketID        uuid.UUID
	TicketReference string
	Rating          *int
	Note            string
	SubmittedAt     *time.Time
}

type Row struct {
	ID              uuid.UUID
	TicketReference string
	CustomerPhone   string
	Rating          int
	Note            string
	SubmittedAt     time.Time
}

type Summary struct {
	Count   int64
	Average float64
}

func EnsureRequest(ctx context.Context, q tenantdb.Querier, plantID, ticketID uuid.UUID) (*Request, bool, error) {
	existing := &Request{TicketID: ticketID}
	err := q.QueryRow(ctx, `
		SELECT id, token
		FROM plant_reviews
		WHERE plant_id = $1 AND ticket_id = $2
	`, plantID, ticketID).Scan(&existing.ID, &existing.Token)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	for attempts := 0; attempts < 5; attempts++ {
		token, err := newToken()
		if err != nil {
			return nil, false, err
		}
		req := &Request{TicketID: ticketID, Token: token}
		err = q.QueryRow(ctx, `
			INSERT INTO plant_reviews (plant_id, ticket_id, token)
			VALUES ($1, $2, $3)
			ON CONFLICT (plant_id, ticket_id) DO NOTHING
			RETURNING id
		`, plantID, ticketID, token).Scan(&req.ID)
		switch {
		case err == nil:
			return req, true, nil
		case errors.Is(err, pgx.ErrNoRows):
			err = q.QueryRow(ctx, `
				SELECT id, token
				FROM plant_reviews
				WHERE plant_id = $1 AND ticket_id = $2
			`, plantID, ticketID).Scan(&existing.ID, &existing.Token)
			if err != nil {
				return nil, false, err
			}
			return existing, false, nil
		case isUniqueViolation(err):
			continue
		default:
			return nil, false, err
		}
	}
	return nil, false, errors.New("reviews: could not allocate unique token")
}

func GetTarget(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, token string) (*Target, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrNotFound
	}
	var t Target
	var rating *int
	var submittedAt *time.Time
	err := q.QueryRow(ctx, `
		SELECT pr.token, pr.ticket_id, t.reference, pr.rating, COALESCE(pr.note, ''), pr.submitted_at
		FROM plant_reviews pr
		JOIN tickets t ON t.id = pr.ticket_id AND t.plant_id = pr.plant_id
		WHERE pr.plant_id = $1 AND pr.token = $2
	`, plantID, token).Scan(&t.Token, &t.TicketID, &t.TicketReference, &rating, &t.Note, &submittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Rating = rating
	t.SubmittedAt = submittedAt
	return &t, nil
}

func Submit(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, token string, rating int, note string) error {
	if rating < 1 || rating > 5 {
		return ErrInvalidRating
	}
	note = strings.TrimSpace(note)
	tag, err := q.Exec(ctx, `
		UPDATE plant_reviews
		SET rating = $3,
		    note = NULLIF($4, ''),
		    submitted_at = now()
		WHERE plant_id = $1
		  AND token = $2
		  AND submitted_at IS NULL
	`, plantID, strings.TrimSpace(token), rating, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		target, loadErr := GetTarget(ctx, q, plantID, token)
		if loadErr != nil {
			return loadErr
		}
		if target.SubmittedAt != nil {
			return ErrAlreadySubmitted
		}
		return ErrNotFound
	}
	return nil
}

func List(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, limit int) ([]Row, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := q.Query(ctx, `
		SELECT pr.id, t.reference, COALESCE(t.customer_phone, ''), pr.rating, COALESCE(pr.note, ''), pr.submitted_at
		FROM plant_reviews pr
		JOIN tickets t ON t.id = pr.ticket_id AND t.plant_id = pr.plant_id
		WHERE pr.plant_id = $1
		  AND pr.submitted_at IS NOT NULL
		ORDER BY pr.submitted_at DESC
		LIMIT $2
	`, plantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Row{}
	for rows.Next() {
		var row Row
		if err := rows.Scan(&row.ID, &row.TicketReference, &row.CustomerPhone, &row.Rating, &row.Note, &row.SubmittedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func Stats(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID) (Summary, error) {
	var s Summary
	err := q.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(AVG(rating), 0)
		FROM plant_reviews
		WHERE plant_id = $1
		  AND submitted_at IS NOT NULL
	`, plantID).Scan(&s.Count, &s.Average)
	return s, err
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
