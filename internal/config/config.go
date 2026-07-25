package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL   string
	ListenAddr    string
	SecureCookies bool
}

func Load() (Config, error) {
	config := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		ListenAddr:  os.Getenv("LISTEN_ADDR"),
	}
	if strings.TrimSpace(config.DatabaseURL) == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if config.ListenAddr == "" {
		config.ListenAddr = ":8080"
	}
	secure := os.Getenv("ACE_SECURE_COOKIES")
	if secure == "" {
		secure = "true"
	}
	parsed, err := strconv.ParseBool(secure)
	if err != nil {
		return Config{}, fmt.Errorf("parse ACE_SECURE_COOKIES: %w", err)
	}
	config.SecureCookies = parsed
	return config, nil
}
