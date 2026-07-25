package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	ServerURL  string `json:"server_url"`
	NodeID     string `json:"node_id"`
	Credential string `json:"credential"`
}

func SaveConfig(path string, config Config) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".agent-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace agent config: %w", err)
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
	if config.ServerURL == "" || config.NodeID == "" || config.Credential == "" {
		return Config{}, fmt.Errorf("agent config is incomplete")
	}
	return config, nil
}
