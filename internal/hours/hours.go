package hours

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gatstogo/internal/tenantdb"

	"github.com/google/uuid"
)

type Segment struct {
	DayOfWeek int
	Segment   int
	OpensAt   string
	ClosesAt  string
	Closed    bool
}

type Day struct {
	DayOfWeek int
	Label     string
	Segments  []Segment
	Closed    bool
}

type TodayStatus struct {
	IsOpen bool
	Label  string
}

var dayLabels = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func Week(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID) ([]Day, error) {
	rows, err := q.Query(ctx, `
		SELECT day_of_week, segment, COALESCE(opens_at::text, ''), COALESCE(closes_at::text, ''), closed
		FROM plant_hours
		WHERE plant_id = $1
		ORDER BY day_of_week, segment
	`, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	days := defaultWeek()
	seen := map[int]bool{}
	for rows.Next() {
		var s Segment
		if err := rows.Scan(&s.DayOfWeek, &s.Segment, &s.OpensAt, &s.ClosesAt, &s.Closed); err != nil {
			return nil, err
		}
		s.OpensAt = trimSeconds(s.OpensAt)
		s.ClosesAt = trimSeconds(s.ClosesAt)
		days[s.DayOfWeek].Segments = append(days[s.DayOfWeek].Segments, s)
		days[s.DayOfWeek].Closed = s.Closed
		seen[s.DayOfWeek] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range days {
		if !seen[i] {
			days[i].Segments = []Segment{{DayOfWeek: i, Segment: 1, OpensAt: "08:00", ClosesAt: "18:00"}}
		}
	}
	return days, nil
}

func StatusNow(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, timezone string) (TodayStatus, error) {
	loc, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		loc = time.FixedZone("WAT", 60*60)
	}
	now := time.Now().In(loc)
	day := int(now.Weekday())

	rows, err := q.Query(ctx, `
		SELECT COALESCE(opens_at::text, ''), COALESCE(closes_at::text, ''), closed
		FROM plant_hours
		WHERE plant_id = $1 AND day_of_week = $2
		ORDER BY segment
	`, plantID, day)
	if err != nil {
		return TodayStatus{}, err
	}
	defer rows.Close()

	var labels []string
	var anyRows bool
	for rows.Next() {
		anyRows = true
		var opens, closes string
		var closed bool
		if err := rows.Scan(&opens, &closes, &closed); err != nil {
			return TodayStatus{}, err
		}
		if closed {
			continue
		}
		opens = trimSeconds(opens)
		closes = trimSeconds(closes)
		if opens == "" || closes == "" {
			continue
		}
		labels = append(labels, formatRange(opens, closes))
		if timeInRange(now, opens, closes) {
			return TodayStatus{IsOpen: true, Label: "Open today " + strings.Join(labels, ", ")}, nil
		}
	}
	if err := rows.Err(); err != nil {
		return TodayStatus{}, err
	}
	if !anyRows {
		return TodayStatus{IsOpen: true, Label: "Hours not set"}, nil
	}
	if len(labels) == 0 {
		return TodayStatus{IsOpen: false, Label: "Closed today"}, nil
	}
	return TodayStatus{IsOpen: false, Label: "Today " + strings.Join(labels, ", ")}, nil
}

func ReplaceWeek(ctx context.Context, q tenantdb.Querier, plantID uuid.UUID, segments []Segment) error {
	if _, err := q.Exec(ctx, `DELETE FROM plant_hours WHERE plant_id = $1`, plantID); err != nil {
		return err
	}
	for _, s := range segments {
		if s.DayOfWeek < 0 || s.DayOfWeek > 6 || s.Segment < 1 || s.Segment > 3 {
			continue
		}
		if s.Closed {
			if _, err := q.Exec(ctx, `
				INSERT INTO plant_hours (plant_id, day_of_week, segment, closed)
				VALUES ($1, $2, 1, true)
				ON CONFLICT (plant_id, day_of_week, segment)
				DO UPDATE SET closed = true, opens_at = NULL, closes_at = NULL
			`, plantID, s.DayOfWeek); err != nil {
				return err
			}
			continue
		}
		if !validHHMM(s.OpensAt) || !validHHMM(s.ClosesAt) {
			continue
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO plant_hours (plant_id, day_of_week, segment, opens_at, closes_at, closed)
			VALUES ($1, $2, $3, $4::time, $5::time, false)
		`, plantID, s.DayOfWeek, s.Segment, s.OpensAt, s.ClosesAt); err != nil {
			return err
		}
	}
	return nil
}

func ParseWeekForm(value func(string) string) []Segment {
	var out []Segment
	for day := 0; day < 7; day++ {
		if value(fmt.Sprintf("closed_%d", day)) == "true" {
			out = append(out, Segment{DayOfWeek: day, Segment: 1, Closed: true})
			continue
		}
		for seg := 1; seg <= 2; seg++ {
			open := strings.TrimSpace(value(fmt.Sprintf("open_%d_%d", day, seg)))
			close := strings.TrimSpace(value(fmt.Sprintf("close_%d_%d", day, seg)))
			if open == "" && close == "" {
				continue
			}
			out = append(out, Segment{DayOfWeek: day, Segment: seg, OpensAt: open, ClosesAt: close})
		}
	}
	return out
}

func defaultWeek() []Day {
	days := make([]Day, 7)
	for i := range days {
		days[i] = Day{DayOfWeek: i, Label: dayLabels[i]}
	}
	return days
}

func validHHMM(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil
}

func trimSeconds(s string) string {
	if len(s) >= 5 {
		return s[:5]
	}
	return s
}

func timeInRange(now time.Time, opens, closes string) bool {
	openParts := strings.Split(opens, ":")
	closeParts := strings.Split(closes, ":")
	if len(openParts) != 2 || len(closeParts) != 2 {
		return false
	}
	oh, _ := strconv.Atoi(openParts[0])
	om, _ := strconv.Atoi(openParts[1])
	ch, _ := strconv.Atoi(closeParts[0])
	cm, _ := strconv.Atoi(closeParts[1])
	start := time.Date(now.Year(), now.Month(), now.Day(), oh, om, 0, 0, now.Location())
	end := time.Date(now.Year(), now.Month(), now.Day(), ch, cm, 0, 0, now.Location())
	if !end.After(start) {
		end = end.Add(24 * time.Hour)
	}
	return (now.Equal(start) || now.After(start)) && now.Before(end)
}

func formatRange(opens, closes string) string {
	return opens + "-" + closes
}
