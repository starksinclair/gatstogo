package main

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTenantAbsoluteURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		slug string
		path string
		want string
	}{
		{
			name: "production domain",
			base: "https://gatstogo.com",
			slug: "sportygas",
			path: "/owner/login",
			want: "https://sportygas.gatstogo.com/owner/login",
		},
		{
			name: "localhost with port",
			base: "http://localhost:8080",
			slug: "sportygas",
			path: "/owner/login",
			want: "http://sportygas.localhost:8080/owner/login",
		},
		{
			name: "strips marketing path",
			base: "https://gatstogo.com/get-started",
			slug: "sportygas",
			path: "/owner/login",
			want: "https://sportygas.gatstogo.com/owner/login",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tenantAbsoluteURL(c.base, c.slug, c.path); got != c.want {
				t.Errorf("tenantAbsoluteURL(%q, %q, %q) = %q, want %q", c.base, c.slug, c.path, got, c.want)
			}
		})
	}
}

func TestShouldNotifyPlantApproved(t *testing.T) {
	if !shouldNotifyPlantApproved("provisioning", "active") {
		t.Fatal("expected provisioning -> active to notify")
	}
	if shouldNotifyPlantApproved("suspended", "active") {
		t.Fatal("did not expect suspended -> active to send approval notice")
	}
	if shouldNotifyPlantApproved("provisioning", "suspended") {
		t.Fatal("did not expect provisioning -> suspended to send approval notice")
	}
}

func TestDispensingMessage(t *testing.T) {
	base := dispensingNotification{PlantName: "Sunrise", Code: "4821", SizeLabel: "12.50 kg", PumpName: "Pump 2"}

	got := dispensingMessage(base)
	want := "Sunrise: gas dispensing has started for code 4821. You paid for 12.50 kg. Your pump is Pump 2."
	if got != want {
		t.Errorf("without review link:\n got %q\nwant %q", got, want)
	}

	base.ReviewURL = "https://sunrise.gatstogo.com/reviews/abc123"
	got = dispensingMessage(base)
	if !strings.HasSuffix(got, " Rate your experience: https://sunrise.gatstogo.com/reviews/abc123") {
		t.Errorf("review link missing from message: %q", got)
	}

	base.PumpName = ""
	if !strings.Contains(dispensingMessage(base), "your assigned pump") {
		t.Errorf("empty pump should fall back to generic wording")
	}
}

func TestApplicationEmails(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	n := plantApplicationNotification{
		PlantID: id, PlantName: "Sunrise\nGas", Slug: "sunrise", City: "Owerri", State: "Imo",
		OwnerName: "Ada Obi", OwnerPhone: "08030000000", OwnerEmail: "ada@example.com",
		LegalBusinessName: "Sunrise Gas Ltd", CACNumber: "RC123", NMDPRALicense: "NMD-9",
	}

	subject, body := applicationAdminEmail("https://gatstogo.com/", n)
	if strings.ContainsAny(subject, "\r\n") {
		t.Errorf("subject must be a single line, got %q", subject)
	}
	for _, want := range []string{"Sunrise", "Ada Obi", "08030000000", "ada@example.com", "RC123", "https://gatstogo.com/admin/plants/" + id.String()} {
		if !strings.Contains(body, want) {
			t.Errorf("admin email missing %q:\n%s", want, body)
		}
	}

	_, applicant := applicationApplicantEmail(n)
	if !strings.Contains(applicant, "Hello Ada Obi") || strings.Contains(applicant, "RC123") {
		t.Errorf("applicant email should greet the owner and not repeat business details:\n%s", applicant)
	}
}
