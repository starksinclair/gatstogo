package pumps

import (
	"context"
	"errors"
	"strings"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Pump struct {
	ID        uuid.UUID
	Name      string
	Status    string
	SortOrder int
	BusyCount int
	OpenShift bool
}

var (
	ErrNoWorkingPump = errors.New("pumps: no working pump is available")
	ErrPumpBusy      = errors.New("pumps: pump already has an open operator shift")
	ErrNotFound      = errors.New("pumps: not found")
)

func EnsureDefault(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		INSERT INTO pumps (plant_id, name, sort_order)
		SELECT $1, 'Pump 1', 1
		WHERE NOT EXISTS (SELECT 1 FROM pumps WHERE plant_id = $1)
	`, plantID)
	return err
}

func List(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID) ([]Pump, error) {
	if err := EnsureDefault(ctx, q, plantID); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT p.id, p.name, p.status, p.sort_order,
		       COUNT(t.id) FILTER (WHERE t.status IN ('paid', 'dispensing')) AS busy_count,
		       EXISTS (
		       	SELECT 1 FROM shifts s
		       	WHERE s.plant_id = p.plant_id AND s.pump_id = p.id AND s.closed_at IS NULL
		       ) AS open_shift
		FROM pumps p
		LEFT JOIN tickets t ON t.pump_id = p.id AND t.plant_id = p.plant_id
		WHERE p.plant_id = $1
		GROUP BY p.id
		ORDER BY p.sort_order, p.created_at
	`, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pump
	for rows.Next() {
		var p Pump
		if err := rows.Scan(&p.ID, &p.Name, &p.Status, &p.SortOrder, &p.BusyCount, &p.OpenShift); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func AssignLeastBusy(ctx context.Context, q tenantdb.Querier, plantID, ticketID uuid.UUID) (Pump, error) {
	if err := EnsureDefault(ctx, q, plantID); err != nil {
		return Pump{}, err
	}
	var p Pump
	err := q.QueryRow(ctx, `
		WITH candidate AS (
			SELECT p.id, p.name, p.status, p.sort_order,
			       COUNT(t.id) FILTER (WHERE t.status IN ('paid', 'dispensing')) AS busy_count
			FROM pumps p
			LEFT JOIN tickets t ON t.pump_id = p.id AND t.plant_id = p.plant_id
			WHERE p.plant_id = $1 AND p.status = 'working'
			GROUP BY p.id
			ORDER BY busy_count ASC, p.sort_order ASC, p.created_at ASC
			LIMIT 1
		), updated AS (
			UPDATE tickets
			SET pump_id = candidate.id
			FROM candidate
			WHERE tickets.id = $2 AND tickets.plant_id = $1 AND tickets.pump_id IS NULL
			RETURNING candidate.id, candidate.name, candidate.status, candidate.sort_order, candidate.busy_count
		)
		SELECT id, name, status, sort_order, busy_count FROM updated
		UNION ALL
		SELECT p.id, p.name, p.status, p.sort_order,
		       COUNT(t.id) FILTER (WHERE t.status IN ('paid', 'dispensing')) AS busy_count
		FROM tickets existing
		JOIN pumps p ON p.id = existing.pump_id AND p.plant_id = existing.plant_id
		LEFT JOIN tickets t ON t.pump_id = p.id AND t.plant_id = p.plant_id
		WHERE existing.id = $2 AND existing.plant_id = $1 AND existing.pump_id IS NOT NULL
		GROUP BY p.id
		LIMIT 1
	`, plantID, ticketID).Scan(&p.ID, &p.Name, &p.Status, &p.SortOrder, &p.BusyCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Pump{}, ErrNoWorkingPump
	}
	return p, err
}

func ValidateShiftPump(ctx context.Context, q tenantdb.Querier, plantID, pumpID uuid.UUID, userID uuid.UUID) error {
	var status string
	if err := q.QueryRow(ctx, `SELECT status FROM pumps WHERE id = $1 AND plant_id = $2`, pumpID, plantID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != "working" {
		return ErrNoWorkingPump
	}
	var busy bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM shifts
			WHERE plant_id = $1 AND pump_id = $2 AND user_id <> $3 AND closed_at IS NULL
		)
	`, plantID, pumpID, userID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrPumpBusy
	}
	return nil
}

func Upsert(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, id *uuid.UUID, name, status string, sortOrder int) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Pump 1"
	}
	if status != "working" && status != "not_working" && status != "maintenance" {
		status = "working"
	}
	if sortOrder <= 0 {
		sortOrder = 1
	}
	if id == nil || *id == uuid.Nil {
		var newID uuid.UUID
		err := q.QueryRow(ctx, `
			INSERT INTO pumps (plant_id, name, status, sort_order)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`, plantID, name, status, sortOrder).Scan(&newID)
		return newID, err
	}
	tag, err := q.Exec(ctx, `
		UPDATE pumps SET name = $3, status = $4, sort_order = $5
		WHERE id = $1 AND plant_id = $2
	`, *id, plantID, name, status, sortOrder)
	if err != nil {
		return uuid.Nil, err
	}
	if tag.RowsAffected() == 0 {
		return uuid.Nil, ErrNotFound
	}
	return *id, nil
}
