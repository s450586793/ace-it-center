package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	fixedComposeProject    = "ace-it-center"
	fixedBackendRepository = "ghcr.io/s450586793/ace-it-center-backend"
	fixedWebRepository     = "ghcr.io/s450586793/ace-it-center-web"
	defaultUpdaterListen   = ":8090"
)

// UpdaterConfig contains the allowlisted updater process configuration.
type UpdaterConfig struct {
	Token             string
	ListenAddr        string
	ComposeProject    string
	ComposeFile       string
	ComposeEnvFile    string
	StateFile         string
	BackupDir         string
	BackendRepository string
	WebRepository     string
	PGHost            string
	PGPort            string
	PGDatabase        string
	PGUser            string
	PGPassword        string
}

// LoadUpdaterConfig reads and validates updater configuration from the environment.
func LoadUpdaterConfig() (UpdaterConfig, error) {
	config := UpdaterConfig{
		Token:             os.Getenv("ACE_UPDATER_TOKEN"),
		ListenAddr:        os.Getenv("ACE_UPDATER_LISTEN_ADDR"),
		ComposeProject:    os.Getenv("ACE_COMPOSE_PROJECT"),
		ComposeFile:       os.Getenv("ACE_COMPOSE_FILE"),
		ComposeEnvFile:    os.Getenv("ACE_COMPOSE_ENV_FILE"),
		StateFile:         os.Getenv("ACE_UPDATER_STATE_FILE"),
		BackupDir:         os.Getenv("ACE_UPDATER_BACKUP_DIR"),
		BackendRepository: os.Getenv("ACE_BACKEND_IMAGE"),
		WebRepository:     os.Getenv("ACE_WEB_IMAGE"),
		PGHost:            os.Getenv("PGHOST"),
		PGPort:            os.Getenv("PGPORT"),
		PGDatabase:        os.Getenv("PGDATABASE"),
		PGUser:            os.Getenv("PGUSER"),
		PGPassword:        os.Getenv("PGPASSWORD"),
	}
	if config.ListenAddr == "" {
		config.ListenAddr = defaultUpdaterListen
	}
	if err := config.validate(); err != nil {
		return UpdaterConfig{}, err
	}
	return config, nil
}

func (config UpdaterConfig) validate() error {
	if strings.TrimSpace(config.Token) != config.Token || utf8.RuneCountInString(config.Token) < 32 || strings.HasPrefix(strings.ToLower(config.Token), "replace-with-") {
		return errors.New("ACE_UPDATER_TOKEN must be a non-placeholder value of at least 32 characters")
	}
	if config.ComposeProject != fixedComposeProject {
		return errors.New("ACE_COMPOSE_PROJECT is not allowed")
	}
	if err := requireRegularFile(config.ComposeFile, "ACE_COMPOSE_FILE"); err != nil {
		return err
	}
	if err := requireRegularFile(config.ComposeEnvFile, "ACE_COMPOSE_ENV_FILE"); err != nil {
		return err
	}
	if !pathUnder("/state", config.StateFile) {
		return errors.New("ACE_UPDATER_STATE_FILE must be under /state")
	}
	if !pathAtOrUnder("/backups", config.BackupDir) {
		return errors.New("ACE_UPDATER_BACKUP_DIR must be under /backups")
	}
	if config.BackendRepository != fixedBackendRepository {
		return errors.New("ACE_BACKEND_IMAGE is not allowed")
	}
	if config.WebRepository != fixedWebRepository {
		return errors.New("ACE_WEB_IMAGE is not allowed")
	}
	for _, value := range []struct {
		name  string
		value string
	}{
		{name: "PGHOST", value: config.PGHost},
		{name: "PGPORT", value: config.PGPort},
		{name: "PGDATABASE", value: config.PGDatabase},
		{name: "PGUSER", value: config.PGUser},
		{name: "PGPASSWORD", value: config.PGPassword},
	} {
		if strings.TrimSpace(value.value) == "" {
			return fmt.Errorf("%s is required", value.name)
		}
	}
	return nil
}

func requireRegularFile(path, variable string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute regular file", variable)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be an existing regular file", variable)
	}
	return nil
}

func pathUnder(root, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	relative, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathAtOrUnder(root, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	relative, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
