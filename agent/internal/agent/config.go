package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type PendingPairing struct {
	ServerURL  string    `json:"server_url"`
	PairingID  string    `json:"pairing_id"`
	Credential string    `json:"pairing_credential"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Config struct {
	ServerURL      string          `json:"server_url,omitempty"`
	NodeID         string          `json:"node_id,omitempty"`
	Credential     string          `json:"credential,omitempty"`
	PendingPairing *PendingPairing `json:"pending_pairing,omitempty"`
}

// SanitizedConfig is safe for local status, IPC, and diagnostic output.
type SanitizedConfig struct {
	ServerURL        string    `json:"server_url"`
	NodeID           string    `json:"node_id"`
	PairingID        string    `json:"pairing_id,omitempty"`
	PairingExpiresAt time.Time `json:"pairing_expires_at,omitempty"`
}

func (config Config) Sanitized() SanitizedConfig {
	result := SanitizedConfig{ServerURL: config.ServerURL, NodeID: config.NodeID}
	if config.PendingPairing != nil {
		result.PairingID = config.PendingPairing.PairingID
		result.PairingExpiresAt = config.PendingPairing.ExpiresAt
	}
	return result
}

func (config Config) IsEnrolled() bool {
	return config.PendingPairing == nil && config.ServerURL != "" && config.NodeID != "" && config.Credential != "" && isHTTPServerURL(config.ServerURL)
}

func (config Config) IsPendingPairing() bool {
	return config.PendingPairing != nil && config.ServerURL == "" && config.NodeID == "" && config.Credential == "" && config.PendingPairing.ServerURL != "" && config.PendingPairing.PairingID != "" && config.PendingPairing.Credential != "" && !config.PendingPairing.ExpiresAt.IsZero() && isHTTPServerURL(config.PendingPairing.ServerURL)
}

func isHTTPServerURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

type configFileOperations struct {
	mkdirAll     func(string, os.FileMode) error
	createTemp   func(string, string) (*os.File, error)
	remove       func(string) error
	openExisting func(string) (io.Closer, error)
	rename       func(string, string) error
}

var configOperations = configFileOperations{
	mkdirAll:     os.MkdirAll,
	createTemp:   os.CreateTemp,
	remove:       os.Remove,
	openExisting: openConfigForReplacement,
	rename:       os.Rename,
}

func SaveConfig(path string, config Config) error {
	directory := filepath.Dir(path)
	if err := configOperations.mkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := secureConfigDirectory(directory); err != nil {
		return fmt.Errorf("secure config directory: %w", err)
	}
	temporary, err := configOperations.createTemp(directory, ".agent-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = configOperations.remove(temporaryPath) }()
	if err := secureConfigFile(temporaryPath); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if err := json.NewEncoder(temporary).Encode(config); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode agent config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := closeExistingConfig(path); err != nil {
		return err
	}
	if err := configOperations.rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace agent config: %w", err)
	}
	return nil
}

func openConfigForReplacement(path string) (io.Closer, error) {
	return os.Open(path)
}

func closeExistingConfig(path string) error {
	existing, err := configOperations.openExisting(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open existing config for replacement: %w", err)
	}
	if err := existing.Close(); err != nil {
		return fmt.Errorf("close existing config for replacement: %w", err)
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open agent config: %w", err)
	}
	defer file.Close()
	var config Config
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode agent config: %w", err)
	}
	if !config.IsEnrolled() && !config.IsPendingPairing() {
		return Config{}, fmt.Errorf("agent config is incomplete")
	}
	return config, nil
}
