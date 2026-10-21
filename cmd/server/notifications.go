package main

import (
	"context"
	"log"
	"net"
	"net/url"
	"strings"
	"time"

	"gatstogo/internal/sms"

	"github.com/google/uuid"
)

type plantApprovalNotification struct {
	PlantName      string
	Slug           string
	PreviousStatus string
	OwnerName      string
	OwnerPhone     string
	OwnerEmail     string
	TermiiSenderID string
}

func loadPlantApprovalNotification(ctx context.Context, s *Server, plantID uuid.UUID) (plantApprovalNotification, error) {
	var n plantApprovalNotification
	err := s.AdminDB.QueryRow(ctx, `
		SELECT p.name, p.slug, p.status, COALESCE(p.termii_sender_id, ''),
		       COALESCE(owner.name, ''), COALESCE(owner.phone, ''), COALESCE(owner.email, '')
		FROM plants p
		LEFT JOIN LATERAL (
			SELECT name, phone, email
			FROM users u
			WHERE u.plant_id = p.id AND u.role = 'owner'
			ORDER BY u.created_at ASC
			LIMIT 1
		) owner ON true
		WHERE p.id = $1
	`, plantID).Scan(&n.PlantName, &n.Slug, &n.PreviousStatus, &n.TermiiSenderID, &n.OwnerName, &n.OwnerPhone, &n.OwnerEmail)
	return n, err
}

func shouldNotifyPlantApproved(previousStatus, newStatus string) bool {
	return strings.EqualFold(strings.TrimSpace(previousStatus), "provisioning") &&
		strings.EqualFold(strings.TrimSpace(newStatus), "active")
}

func notifyPlantApproved(ctx context.Context, s *Server, n plantApprovalNotification) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	loginURL := tenantAbsoluteURL(s.AppBaseURL, n.Slug, "/owner/login")
	ownerName := strings.TrimSpace(n.OwnerName)
	if ownerName == "" {
		ownerName = "there"
	}

	if phone := strings.TrimSpace(n.OwnerPhone); phone != "" {
		message := "Hi " + ownerName + ", " + n.PlantName + " has been approved on GatsToGo. Log in: " + loginURL + " Use your phone number and the password you created during application."
		if err := sms.SendWithSenderID(ctx, s.SMS, n.TermiiSenderID, phone, message); err != nil {
			log.Println("plant approval notification: sms failed:", err)
		}
	} else {
		log.Println("plant approval notification: skipped sms, owner phone missing for", n.Slug)
	}

	if email := strings.TrimSpace(n.OwnerEmail); email != "" {
		body := "Hello " + ownerName + ",\n\n" +
			"Your plant application for " + n.PlantName + " has been approved.\n\n" +
			"Sign in here: " + loginURL + "\n\n" +
			"Use your phone number and the password you created during your application. If you need a new password, use the \"Forgot password\" link on the sign-in page.\n\n" +
			"Welcome to GatsToGo."
		if err := s.Email.Send(ctx, email, "Your "+n.PlantName+" GatsToGo account is approved", body); err != nil {
			log.Println("plant approval notification: email failed:", err)
		}
	} else {
		log.Println("plant approval notification: skipped email, owner email missing for", n.Slug)
	}
}

type dispensingNotification struct {
	PlantName      string
	TermiiSenderID string
	CustomerPhone  string
	Code           string
	SizeLabel      string
	PumpName       string
}

func notifyDispensingStarted(ctx context.Context, s *Server, n dispensingNotification) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	phone := strings.TrimSpace(n.CustomerPhone)
	if phone == "" {
		log.Println("dispensing notification: skipped sms, customer phone missing")
		return
	}
	pump := strings.TrimSpace(n.PumpName)
	if pump == "" {
		pump = "your assigned pump"
	}
	message := n.PlantName + ": gas dispensing has started for code " + n.Code + ". You paid for " + n.SizeLabel + ". Your pump is " + pump + "."
	if err := sms.SendWithSenderID(ctx, s.SMS, n.TermiiSenderID, phone, message); err != nil {
		log.Println("dispensing notification: sms failed:", err)
	}
}

func tenantAbsoluteURL(appBaseURL, slug, path string) string {
	base := strings.TrimRight(strings.TrimSpace(appBaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return base + path
	}

	host := u.Host
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = slug + "." + h + ":" + p
	} else {
		host = slug + "." + host
	}
	u.Host = host
	u.Path = path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
