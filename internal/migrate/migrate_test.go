package migrate

import "testing"

func TestIsSeedAndRoleFiles(t *testing.T) {
	if !isSeedFile("0002_seed_data.up.sql") {
		t.Fatal("expected seed detection")
	}
	if isSeedFile("0001_init_schema.up.sql") {
		t.Fatal("0001 is not seed")
	}
	if !isRoleCredentialsFile("0003_role_credentials.up.sql") {
		t.Fatal("expected role credentials detection")
	}
}

func TestDollarQuote(t *testing.T) {
	if got := dollarQuote(`abc`); got != `$gtg$abc$gtg$` {
		t.Fatalf("got %q", got)
	}
	if got := dollarQuote(`has $gtg$ inside`); got != `$gtgx$has $gtg$ inside$gtgx$` {
		t.Fatalf("got %q", got)
	}
}
