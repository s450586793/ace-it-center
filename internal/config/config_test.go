package config

import (
	"strings"
	"testing"
)

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

func TestLoadAllowsLocalDevelopmentWithoutUpdater(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://ace:secret@postgres/ace?sslmode=disable")
	t.Setenv("ACE_UPDATER_URL", "")
	t.Setenv("ACE_UPDATER_TOKEN", "")

	config, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.UpdaterURL != "" || config.UpdaterToken != "" {
		t.Fatalf("updater configuration = %#v, want absent", config)
	}
}

func TestLoadRequiresValidCompleteUpdaterConfiguration(t *testing.T) {
	for _, test := range []struct {
		name  string
		url   string
		token string
		want  bool
	}{
		{name: "complete", url: "http://updater:8090", token: "1234567890abcdefghijklmnopqrstuvwxyzABCD", want: true},
		{name: "URL only", url: "http://updater:8090"},
		{name: "token only", token: "1234567890abcdefghijklmnopqrstuvwxyzABCD"},
		{name: "HTTPS", url: "https://updater:8090", token: "1234567890abcdefghijklmnopqrstuvwxyzABCD"},
		{name: "userinfo", url: "http://user:pass@updater:8090", token: "1234567890abcdefghijklmnopqrstuvwxyzABCD"},
		{name: "query", url: "http://updater:8090?next=other", token: "1234567890abcdefghijklmnopqrstuvwxyzABCD"},
		{name: "fragment", url: "http://updater:8090#other", token: "1234567890abcdefghijklmnopqrstuvwxyzABCD"},
		{name: "short token", url: "http://updater:8090", token: "too-short"},
		{name: "placeholder token", url: "http://updater:8090", token: "replace-with-a-long-random-token-value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://ace:database-secret@postgres/ace?sslmode=disable")
			t.Setenv("ACE_UPDATER_URL", test.url)
			t.Setenv("ACE_UPDATER_TOKEN", test.token)

			config, err := Load()
			if test.want {
				if err != nil {
					t.Fatalf("Load() error = %v", err)
				}
				if config.UpdaterURL != test.url || config.UpdaterToken != test.token {
					t.Fatalf("updater configuration = %#v", config)
				}
				return
			}
			if err == nil {
				t.Fatal("Load() accepted unsafe or incomplete updater configuration")
			}
			for _, secret := range []string{"database-secret", test.token} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatalf("Load() leaked secret in error: %v", err)
				}
			}
		})
	}
}
