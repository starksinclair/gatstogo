package attendant

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"gatstogo/internal/config"
	"gatstogo/internal/domain"
	"gatstogo/web/templates/pages"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxAttempts  = 5
	lockDuration = 20 * time.Second

	sessionLoginAttempts     = "login_attempts"
	sessionLoginLockedUntil  = "login_locked_until"
	sessionLoginEmployeeID   = "login_employee_id"
	sessionFillAttempts      = "fill_attempts"
	sessionFillLockedUntil   = "fill_locked_until"
	sessionCashSelectedSize  = "cash_selected_cylinder"
)

var cylinderSizes = []string{"3", "6", "12.5", "25", "50"}

func roleToConfig(role string) string {
	switch role {
	case "cashier":
		return "cashier"
	case "operator":
		return "operator"
	default: // owner, manager, admin
		return "combined"
	}
}

func formatNaira(kobo int64) string {
	naira := float64(kobo) / 100
	return "₦" + formatWholeNumber(int64(math.Round(naira)))
}

func formatKg(grams int64) string {
	return fmt.Sprintf("%.2f kg", float64(grams)/1000)
}

func formatWholeNumber(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	prefix := len(s) % 3
	if prefix == 0 {
		prefix = 3
	}
	out = append(out, s[:prefix]...)
	for i := prefix; i < len(s); i += 3 {
		out = append(out, ',')
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}

// latestPrice returns the plant's current price-per-kg row.
func latestPrice(ctx context.Context, deps *config.Dependencies, plantID uuid.UUID) (priceID uuid.UUID, pricePerKg int64, err error) {
	err = deps.AppDB.QueryRow(ctx, `
		SELECT id, price_per_kg FROM prices
		WHERE plant_id = $1
		ORDER BY effective_from DESC
		LIMIT 1
	`, plantID).Scan(&priceID, &pricePerKg)
	return
}

// generateTicketCode picks a 4-digit code not currently in use (pending/paid)
// for the plant, matching the partial unique index on tickets(plant_id, code).
func generateTicketCode(ctx context.Context, deps *config.Dependencies, plantID uuid.UUID) (string, error) {
	for i := 0; i < 20; i++ {
		code := fmt.Sprintf("%04d", rand.Intn(10000))
		var exists bool
		err := deps.AppDB.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM tickets
				WHERE plant_id = $1 AND code = $2 AND status IN ('pending', 'paid')
			)
		`, plantID, code).Scan(&exists)
		if err != nil {
			return "", err
		}
		if !exists {
			return code, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique ticket code")
}

func cylinderKg(value string) float64 {
	switch value {
	case "3":
		return 3
	case "6":
		return 6
	case "25":
		return 25
	case "50":
		return 50
	default:
		return 12.5
	}
}

func cylinderLabel(value string) string {
	kg := cylinderKg(value)
	if kg == math.Trunc(kg) {
		return fmt.Sprintf("%.0f kg", kg)
	}
	return fmt.Sprintf("%.1f kg", kg)
}

func buildCylinderButtons(selected string) []pages.CylinderButton {
	out := make([]pages.CylinderButton, 0, len(cylinderSizes))
	for _, size := range cylinderSizes {
		class := ""
		if size == selected {
			class = "active"
		}
		out = append(out, pages.CylinderButton{
			Label: cylinderLabel(size),
			Value: size,
			Class: class,
		})
	}
	return out
}

// shiftStats aggregates fills/kg dispensed for the header and end-shift
// summary, computed live from tickets rather than kept as mutable counters.
func shiftStats(ctx context.Context, deps *config.Dependencies, shiftID uuid.UUID) (fillsCompleted int64, kgDispensedGrams int64) {
	_ = deps.AppDB.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(COALESCE(net_grams, size_grams)), 0)
		FROM tickets
		WHERE filled_shift_id = $1 AND status = 'filled'
	`, shiftID).Scan(&fillsCompleted, &kgDispensedGrams)
	return
}

// cashHeld sums the cash this shift has taken in (sales + opening float),
// net of any deposits/payouts recorded against the shift.
func cashHeld(ctx context.Context, deps *config.Dependencies, shiftID uuid.UUID) int64 {
	var total int64
	_ = deps.AppDB.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_kobo), 0)
		FROM cash_movements
		WHERE shift_id = $1 AND kind IN ('opening_float', 'sale', 'deposit', 'payout')
	`, shiftID).Scan(&total)
	return total
}

func cashState(held int64) (class, caption string) {
	const amberLimit = 150000_00 // ₦150,000 in kobo
	const blockLimit = 300000_00 // ₦300,000 in kobo
	switch {
	case held >= blockLimit:
		return "danger", "Deposit required"
	case held >= amberLimit:
		return "warn", "Deposit soon"
	default:
		return "", "Normal"
	}
}

// buildShellData assembles everything the dashboard shell needs to render
// the given active tab for the authenticated user's open shift.
func buildShellData(ctx context.Context, deps *config.Dependencies, plant *domain.Plant, user *domain.User, shift *domain.Shift, activeTab string) pages.DashboardShellData {
	fills, kgGrams := shiftStats(ctx, deps, shift.ID)
	config := roleToConfig(user.Role)

	data := pages.DashboardShellData{
		Plant:          plant,
		Config:         config,
		FillsCompleted: fills,
		KgDispensedStr: formatKg(kgGrams),
		ActiveTab:      activeTab,
		ShowCashTab:    config != "operator",
		Fill:           pages.FillPanelData{},
		End: pages.EndPanelData{
			FillsCompleted: fills,
			KgDispensedStr: formatKg(kgGrams),
			CashHeldStr:    formatNaira(cashHeld(ctx, deps, shift.ID)),
			OpeningCashStr: formatNaira(shift.OpeningCashKobo),
		},
	}
	return data
}

func buildFillPanel(locked bool, lockSeconds int, errorMsg, errorClass string) pages.FillPanelData {
	return pages.FillPanelData{
		Locked:          locked,
		LockSecondsLeft: lockSeconds,
		ErrorMsg:        errorMsg,
		ErrorClass:      errorClass,
	}
}

// renderShell builds and writes a full dashboard-shell response for the
// given tab, optionally overriding the Fill/Cash panel data (pass nil to
// fall back to the tab's default render).
func renderShell(w http.ResponseWriter, r *http.Request, deps *config.Dependencies, plant *domain.Plant, user *domain.User, shift *domain.Shift, activeTab string, fill *pages.FillPanelData, cash *pages.CashPanelData) {
	data := buildShellData(r.Context(), deps, plant, user, shift, activeTab)
	if fill != nil {
		data.Fill = *fill
	}
	if cash != nil {
		data.Cash = *cash
	} else {
		data.Cash = buildCashPanel(r.Context(), deps, shift, "", "", "")
	}
	if err := pages.DashboardShell(data).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func buildCashPanel(ctx context.Context, deps *config.Dependencies, shift *domain.Shift, selected, phone, confirmMsg string) pages.CashPanelData {
	if selected == "" {
		selected = "12.5"
	}
	_, pricePerKg, _ := latestPrice(ctx, deps, shift.PlantID)
	grams := int64(cylinderKg(selected) * 1000)
	amount := (grams * pricePerKg) / 1000
	held := cashHeld(ctx, deps, shift.ID)
	class, caption := cashState(held)

	return pages.CashPanelData{
		Cylinders:        buildCylinderButtons(selected),
		SelectedCylinder: selected,
		Phone:            phone,
		AmountStr:        formatNaira(amount),
		Blocked:          class == "danger",
		ButtonLabel:      "Record cash and open valve",
		CashHeldStr:      formatNaira(held),
		CashClass:        class,
		CashCaption:      caption,
		ConfirmMsg:       confirmMsg,
	}
}

func normalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isNoRows(err error) bool {
	return err == pgx.ErrNoRows
}
