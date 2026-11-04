package main

import (
	"encoding/csv"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// adminBuyersCSVHandler exports the gas buyers who registered on
// /get-started, newest first. Admin session only (it is mounted inside the
// authenticated admin group in main.go).
func adminBuyersCSVHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.AdminDB.Query(r.Context(), `
			SELECT name, phone, COALESCE(email, ''), state, COALESCE(source, ''), COALESCE(offer_rank, 0), created_at
			FROM buyer_signups ORDER BY created_at DESC
		`)
		if err != nil {
			log.Println("admin buyers csv:", err)
			http.Error(w, "Could not load buyers", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="buyers.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"name", "phone", "email", "state", "source", "offer_spot", "signed_up"})
		for rows.Next() {
			var name, phone, email, state, source string
			var rank int
			var created time.Time
			if err := rows.Scan(&name, &phone, &email, &state, &source, &rank, &created); err != nil {
				log.Println("admin buyers csv: scan:", err)
				return
			}
			spot := ""
			if rank > 0 {
				spot = strconv.Itoa(rank)
			}
			_ = cw.Write([]string{csvSafe(name), phone, csvSafe(email), state, csvSafe(source), spot, created.Format("2006-01-02 15:04")})
		}
		cw.Flush()
	}
}

// csvSafe stops a spreadsheet from running a typed value as a formula.
func csvSafe(v string) string {
	if v != "" && strings.ContainsAny(v[:1], "=+-@\t\r") {
		return "'" + v
	}
	return v
}
