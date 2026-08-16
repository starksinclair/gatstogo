package main

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"gatstogo/internal/csrf"
	"gatstogo/internal/middleware"
	"gatstogo/internal/tenantdb"
	"gatstogo/web/templates/pages"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ---- GET /admin/plants/{id} ----

// adminPlantDetailHandler backs the plant detail page -- specifically the
// one place an admin can review the business/regulatory/settlement
// details a self-serve /get-started submission collected
// (internal/plants.CreateParams) before deciding whether to Activate it.
// The Plants table's own summary row (adminConsoleContent) never showed
// those fields; this closes that gap.
func adminPlantDetailHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := middleware.GetActor(r.Context())
		if actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		plantID, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			renderNotFound(w, r)
			return
		}

		data, ok := loadAdminPlantDetail(r.Context(), s.AdminDB, plantID)
		if !ok {
			renderNotFound(w, r)
			return
		}
		data.CSRFToken, _ = csrf.Token(actor.Token)

		if err := pages.AdminPlantDetail(data).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// loadAdminPlantDetail loads everything adminPlantDetailContent shows for
// one plant -- the same fields loadAdminPlants (main.go) already surfaces
// in summary form, plus the business/bank fields that table never had
// room for, plus the owner user's own contact details (a separate query:
// domain.Plant/plants itself carries no owner reference, users.plant_id
// does). Returns ok=false for an id that doesn't match any plant, so the
// handler can 404 instead of rendering a blank page.
func loadAdminPlantDetail(ctx context.Context, db tenantdb.Querier, plantID uuid.UUID) (pages.AdminPlantDetailData, bool) {
	var data pages.AdminPlantDetailData
	var bankAccountNumber string
	var firstTxn, lastTxn sql.NullTime
	var created, updated time.Time
	var volume int64

	err := db.QueryRow(ctx, `
		SELECT p.name, p.slug, COALESCE(p.city, ''), COALESCE(p.address, ''), COALESCE(p.phone, ''),
		       COALESCE(p.state, ''), COALESCE(p.legal_business_name, ''), COALESCE(p.cac_number, ''),
		       COALESCE(p.nmdpra_license_number, ''),
		       COALESCE(p.bank_name, ''), COALESCE(p.bank_account_number, ''), COALESCE(p.bank_account_name, ''),
		       COALESCE(p.paystack_subaccount_code, ''),
		       p.status, COALESCE(p.custom_domain, ''), p.domain_status,
		       (SELECT COUNT(*) FROM users u WHERE u.plant_id = p.id),
		       (SELECT COUNT(*) FROM prices pr WHERE pr.plant_id = p.id),
		       (SELECT COUNT(*) FROM shifts s WHERE s.plant_id = p.id AND s.closed_at IS NULL),
		       (SELECT COUNT(*) FROM tickets t WHERE t.plant_id = p.id AND t.created_at >= now() - interval '7 days'),
		       (SELECT COALESCE(SUM(t.size_grams), 0) FROM tickets t WHERE t.plant_id = p.id AND t.created_at >= now() - interval '30 days' AND t.status <> 'voided'),
		       (SELECT MIN(t.created_at) FROM tickets t WHERE t.plant_id = p.id),
		       (SELECT MAX(t.created_at) FROM tickets t WHERE t.plant_id = p.id),
		       p.created_at, p.updated_at
		FROM plants p
		WHERE p.id = $1
	`, plantID).Scan(
		&data.Name, &data.Slug, &data.City, &data.Address, &data.Phone,
		&data.State, &data.LegalBusinessName, &data.CACNumber, &data.NMDPRALicense,
		&data.BankName, &bankAccountNumber, &data.BankAccountName, &data.SubaccountCode,
		&data.RawStatus, &data.Domain, &data.DomainStatus,
		&data.StaffCount, &data.PriceCount, &data.OpenShifts, &data.Txns7d, &volume,
		&firstTxn, &lastTxn, &created, &updated,
	)
	if err != nil {
		return data, false
	}

	data.ID = plantID.String()
	data.RawStatus = strings.ToLower(fallbackString(data.RawStatus, "provisioning"))
	data.Status = plantStatusLabel(data.RawStatus)
	data.BadgeCSS = badgeForStatus(data.RawStatus)
	if data.Domain == "" {
		data.Domain = data.Slug + ".gatstogo.local"
	}
	data.DomainStatus = strings.ToUpper(fallbackString(data.DomainStatus, "none"))
	data.DomainBadge = badgeForDomain(data.DomainStatus)

	data.City = fallbackString(data.City, "Coming soon")
	data.Address = fallbackString(data.Address, "Address coming soon")
	data.Phone = fallbackString(data.Phone, "Phone coming soon")
	data.State = fallbackString(data.State, "Not yet provided")
	data.LegalBusinessName = fallbackString(data.LegalBusinessName, "Not yet provided")
	data.CACNumber = fallbackString(data.CACNumber, "Not yet provided")
	data.NMDPRALicense = fallbackString(data.NMDPRALicense, "Not yet provided")
	data.BankName = fallbackString(data.BankName, "Not yet provided")
	data.BankAccountName = fallbackString(data.BankAccountName, "Not yet provided")
	data.SubaccountCode = fallbackString(data.SubaccountCode, "Not yet provided")
	if bankAccountNumber == "" {
		data.BankAccount = "Not yet provided"
	} else {
		data.BankAccount = maskAccountNumber(bankAccountNumber)
	}

	data.FirstTxn = "No transaction yet"
	if firstTxn.Valid {
		data.FirstTxn = firstTxn.Time.Format("2 Jan 2006")
	}
	data.LastTxn = "No transaction yet"
	if lastTxn.Valid {
		data.LastTxn = lastTxn.Time.Format("2 Jan, 15:04")
	}
	data.Volume30d = formatKg(volume)
	data.CreatedAt = created.Format("2 Jan 2006")
	data.UpdatedAt = updated.Format("2 Jan 2006")

	var ownerName, ownerPhone, ownerEmail string
	if err := db.QueryRow(ctx, `
		SELECT name, phone, COALESCE(email, '') FROM users
		WHERE plant_id = $1 AND role = 'owner'
		ORDER BY created_at ASC LIMIT 1
	`, plantID).Scan(&ownerName, &ownerPhone, &ownerEmail); err == nil {
		data.OwnerName = fallbackString(ownerName, "Not yet provided")
		data.OwnerPhone = fallbackString(ownerPhone, "Not yet provided")
		data.OwnerEmail = fallbackString(ownerEmail, "Not yet provided")
	} else {
		data.OwnerName = "Not yet provided"
		data.OwnerPhone = "Not yet provided"
		data.OwnerEmail = "Not yet provided"
	}

	data.Activity = loadActivity(ctx, db, plantID, 10)

	return data, true
}
