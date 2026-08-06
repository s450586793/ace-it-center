package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUpdaterConfigAcceptsRequiredFixedConfiguration(t *testing.T) {
	composeFile, envFile := setValidUpdaterEnvironment(t)
	t.Setenv("ACE_UPDATER_LISTEN_ADDR", "")

	config, err := LoadUpdaterConfig()
	if err != nil {
		t.Fatalf("LoadUpdaterConfig() error = %v", err)
	}
	if config.ListenAddr != ":8090" {
		t.Fatalf("ListenAddr = %q, want :8090", config.ListenAddr)
	}
	if config.ComposeFile != composeFile || config.ComposeEnvFile != envFile {
		t.Fatalf("compose paths = %#v", config)
	}
	if config.StateFile != "/state/update-state.json" || config.BackupDir != "/backups/updates" {
		t.Fatalf("state paths = %#v", config)
	}
}

func TestLoadUpdaterConfigRejectsUnsafeOrIncompleteValues(t *testing.T) {
	composeFile, _ := setValidUpdaterEnvironment(t)
	directory := t.TempDir()
	for _, test := range []struct {
		name   string
		key    string
		value  string
		remove bool
	}{
		{name: "missing token", key: "ACE_UPDATER_TOKEN", remove: true},
		{name: "placeholder token", key: "ACE_UPDATER_TOKEN", value: "replace-with-a-long-random-token-value"},
		{name: "short token", key: "ACE_UPDATER_TOKEN", value: "too-short"},
		{name: "wrong project", key: "ACE_COMPOSE_PROJECT", value: "other-project"},
		{name: "relative compose file", key: "ACE_COMPOSE_FILE", value: "compose.yaml"},
		{name: "compose directory", key: "ACE_COMPOSE_FILE", value: directory},
		{name: "missing compose file", key: "ACE_COMPOSE_FILE", value: filepath.Join(directory, "missing.yaml")},
		{name: "relative environment file", key: "ACE_COMPOSE_ENV_FILE", value: ".env"},
		{name: "state outside state volume", key: "ACE_UPDATER_STATE_FILE", value: "/state-other/update-state.json"},
		{name: "state volume root", key: "ACE_UPDATER_STATE_FILE", value: "/state"},
		{name: "backup outside backup volume", key: "ACE_UPDATER_BACKUP_DIR", value: "/backups-other"},
		{name: "backup volume root", key: "ACE_UPDATER_BACKUP_DIR", value: "/backups"},
		{name: "wrong backend repository", key: "ACE_BACKEND_IMAGE", value: "ghcr.io/example/backend"},
		{name: "wrong web repository", key: "ACE_WEB_IMAGE", value: "ghcr.io/example/web"},
		{name: "missing password", key: "PGPASSWORD", remove: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidUpdaterEnvironment(t)
			if test.remove {
				t.Setenv(test.key, "")
			} else {
				t.Setenv(test.key, test.value)
			}
			_, err := LoadUpdaterConfig()
			if err == nil {
				t.Fatalf("LoadUpdaterConfig() accepted %s", test.name)
			}
			if strings.Contains(err.Error(), "database-secret") {
				t.Fatalf("LoadUpdaterConfig() leaked password: %v", err)
			}
		})
	}

	_ = composeFile
}

func TestLoadUpdaterConfigRejectsSymbolicLinkComposeFiles(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target-compose.yaml")
	link := filepath.Join(directory, "compose-link.yaml")
	if err := os.WriteFile(target, []byte("name: ace-it-center\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	setValidUpdaterEnvironment(t)
	t.Setenv("ACE_COMPOSE_FILE", link)

	if _, err := LoadUpdaterConfig(); err == nil {
		t.Fatal("LoadUpdaterConfig accepted a symbolic link compose file")
	}
}

func setValidUpdaterEnvironment(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	composeFile := filepath.Join(directory, "compose.yaml")
	envFile := filepath.Join(directory, ".env")
	for _, path := range []string{composeFile, envFile} {
		if err := os.WriteFile(path, []byte("version: '3'\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(path), err)
		}
	}
	for key, value := range map[string]string{
		"ACE_UPDATER_TOKEN":       "1234567890abcdefghijklmnopqrstuvwxyzABCD",
		"ACE_UPDATER_LISTEN_ADDR": ":18090",
		"ACE_COMPOSE_PROJECT":     "ace-it-center",
		"ACE_COMPOSE_FILE":        composeFile,
		"ACE_COMPOSE_ENV_FILE":    envFile,
		"ACE_UPDATER_STATE_FILE":  "/state/update-state.json",
		"ACE_UPDATER_BACKUP_DIR":  "/backups/updates",
		"ACE_BACKEND_IMAGE":       "ghcr.io/s450586793/ace-it-center-backend",
		"ACE_WEB_IMAGE":           "ghcr.io/s450586793/ace-it-center-web",
		"PGHOST":                  "postgres",
		"PGPORT":                  "5432",
		"PGDATABASE":              "ace_it_center",
		"PGUSER":                  "ace",
		"PGPASSWORD":              "database-secret",
	} {
		t.Setenv(key, value)
	}
	return composeFile, envFile
}
