package main

import "testing"

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
