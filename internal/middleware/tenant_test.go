package middleware

import "testing"

func TestExtractSubdomain(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"sunrise.gatstogo.ng", "sunrise"},
		{"sunrise.localhost:8080", "sunrise"},
		{"localhost:8080", ""},
		{"localhost", ""},
		{"gatstogo.ng", "gatstogo"},
		{"www.gatstogo.ng", "www"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ExtractSubdomain(c.host); got != c.want {
			t.Errorf("ExtractSubdomain(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

// TestHasBaseDomainSuffixRejectsSpoofedHosts is the regression test for a
// real, confirmed Host-header-injection vulnerability (see BaseDomain's
// own doc comment): before this check existed, Tenant resolved a real
// plant for ANY Host whose leftmost label matched a slug, regardless of
// what came after it -- confirmed live, including a real password-reset
// email whose link embedded an attacker-chosen host. Every case below
// with want=false is a variant of that exact attack.
func TestHasBaseDomainSuffixRejectsSpoofedHosts(t *testing.T) {
	const baseDomain = "gatstogo.ng"

	cases := []struct {
		name string
		host string
		want bool
	}{
		{"legitimate subdomain", "sunrise.gatstogo.ng", true},
		{"legitimate subdomain with port", "sunrise.gatstogo.ng:8080", true},
		{"the attack this fixes: attacker-controlled host after a real slug", "sunrise.evil.com", false},
		{"attacker-controlled host, no port", "sunrise.attacker.net", false},
		{"lookalike domain with no dot boundary", "sunrise.evilgatstogo.ng", false},
		{"lookalike domain, base domain as a bare suffix without leading dot", "sunriseXgatstogo.ng", false},
		{"bare base domain, no subdomain at all", "gatstogo.ng", false},
		{"completely unrelated host", "example.com", false},
		{"empty host", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasBaseDomainSuffix(c.host, baseDomain); got != c.want {
				t.Errorf("hasBaseDomainSuffix(%q, %q) = %v, want %v", c.host, baseDomain, got, c.want)
			}
		})
	}
}

// TestHasBaseDomainSuffixFailsClosedWhenUnconfigured confirms an
// empty/unconfigured BaseDomain rejects every host, rather than
// (correctly, but far too dangerously) matching everything the way a
// naive strings.HasSuffix(host, "."+"") would.
func TestHasBaseDomainSuffixFailsClosedWhenUnconfigured(t *testing.T) {
	for _, host := range []string{"sunrise.gatstogo.ng", "anything", ""} {
		if hasBaseDomainSuffix(host, "") {
			t.Errorf("hasBaseDomainSuffix(%q, \"\") = true, want false (must fail closed)", host)
		}
	}
}

// TestLocalDevBaseDomain confirms the actual local-dev configuration
// (APP_BASE_DOMAIN=localhost, .env) accepts the real dev URL shape this
// app is reached at (sunrise.localhost:8080) and rejects a spoofed one.
func TestLocalDevBaseDomain(t *testing.T) {
	if !hasBaseDomainSuffix("sunrise.localhost:8080", "localhost") {
		t.Error("expected sunrise.localhost:8080 to match base domain \"localhost\"")
	}
	if hasBaseDomainSuffix("sunrise.evil.com", "localhost") {
		t.Error("expected sunrise.evil.com NOT to match base domain \"localhost\"")
	}
}
