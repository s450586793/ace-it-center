package config

import "testing"

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted an empty DATABASE_URL")
	}
}

func TestLoadAppliesDefaultsAndParsesCookieMode(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://ace:secret@postgres/ace?sslmode=disable")
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("ACE_SECURE_COOKIES", "false")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.ListenAddr != ":8080" {
		t.Fatalf("ListenAddr = %q, want :8080", got.ListenAddr)
	}
	if got.SecureCookies {
		t.Fatal("SecureCookies = true, want explicit false")
	}
}

func TestLoadRejectsInvalidCookieMode(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://ace:secret@postgres/ace?sslmode=disable")
	t.Setenv("ACE_SECURE_COOKIES", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted an invalid ACE_SECURE_COOKIES value")
	}
}
