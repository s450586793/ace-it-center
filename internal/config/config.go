package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Config struct {
	DatabaseURL   string
	ListenAddr    string
	SecureCookies bool
	UpdaterURL    string
	UpdaterToken  string
}

func Load() (Config, error) {
	config := Config{
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		ListenAddr:   os.Getenv("LISTEN_ADDR"),
		UpdaterURL:   os.Getenv("ACE_UPDATER_URL"),
		UpdaterToken: os.Getenv("ACE_UPDATER_TOKEN"),
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
	if err := config.validateUpdater(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) validateUpdater() error {
	urlSet := config.UpdaterURL != ""
	tokenSet := config.UpdaterToken != ""
	if !urlSet && !tokenSet {
		return nil
	}
	if !urlSet || !tokenSet {
		return errors.New("ACE_UPDATER_URL and ACE_UPDATER_TOKEN must be configured together")
	}
	parsedURL, err := url.Parse(config.UpdaterURL)
	if err != nil || parsedURL.Scheme != "http" || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return errors.New("ACE_UPDATER_URL is invalid")
	}
	if strings.TrimSpace(config.UpdaterToken) != config.UpdaterToken || utf8.RuneCountInString(config.UpdaterToken) < 32 || strings.HasPrefix(strings.ToLower(config.UpdaterToken), "replace-with-") {
		return errors.New("ACE_UPDATER_TOKEN must be a non-placeholder value of at least 32 characters")
	}
	return nil
}
