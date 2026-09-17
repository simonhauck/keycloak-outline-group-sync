package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

type config struct {
	keycloakURL          string
	keycloakRealm        string
	keycloakClientID     string
	keycloakClientSecret string
	rolesClientID        string
	outlineURL           string
	outlineToken         string
	logLevel             slog.Level
}

// Run performs one Sync Run using the configuration from the process
// environment. It is the single entry point exercised by tests.
func Run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.logLevel}))
	return run(ctx, cfg, logger)
}

func loadConfig() (config, error) {
	var cfg config

	required := []struct {
		name   string
		target *string
	}{
		{"KEYCLOAK_URL", &cfg.keycloakURL},
		{"KEYCLOAK_REALM", &cfg.keycloakRealm},
		{"KEYCLOAK_CLIENT_ID", &cfg.keycloakClientID},
		{"KEYCLOAK_CLIENT_SECRET", &cfg.keycloakClientSecret},
		{"OUTLINE_URL", &cfg.outlineURL},
		{"OUTLINE_TOKEN", &cfg.outlineToken},
	}
	for _, setting := range required {
		*setting.target = os.Getenv(setting.name)
		if *setting.target == "" {
			return config{}, fmt.Errorf("missing required environment variable %s", setting.name)
		}
	}

	cfg.rolesClientID = os.Getenv("KEYCLOAK_ROLES_CLIENT_ID")
	if cfg.rolesClientID == "" {
		cfg.rolesClientID = cfg.keycloakClientID
	}

	for _, setting := range []struct {
		name  string
		value string
	}{
		{"KEYCLOAK_URL", cfg.keycloakURL},
		{"OUTLINE_URL", cfg.outlineURL},
	} {
		if err := validateURL(setting.value); err != nil {
			return config{}, fmt.Errorf("invalid %s: %w", setting.name, err)
		}
	}

	cfg.logLevel = slog.LevelInfo
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		level, err := parseLogLevel(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid LOG_LEVEL: %w", err)
		}
		cfg.logLevel = level
	}

	return cfg, nil
}

func validateURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%q must use http or https", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%q must include a host", raw)
	}
	return nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%q is not one of debug, info, warn, error", raw)
	}
}
