package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	keycloakURL          string
	keycloakRealm        string
	keycloakClientID     string
	keycloakClientSecret string
	rolesClientID        string
	outlineURL           string
	outlineToken         string
	syncInterval         time.Duration
	dryRun               bool
	logLevel             slog.Level
}

// RunOnce performs a single Sync Run using the configuration from the process
// environment and returns its error.
func RunOnce(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	return runSync(ctx, cfg, newLogger(cfg.logLevel))
}

// Run runs the Sync Service: a Sync Run at startup, then another on every
// SYNC_INTERVAL tick until ctx is cancelled. Failed runs are logged and do not
// stop the loop; Run returns nil once ctx is done.
func Run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.logLevel)

	ticker := time.NewTicker(cfg.syncInterval)
	defer ticker.Stop()

	for {
		if err := runSync(ctx, cfg, logger); err != nil && (ctx.Err() == nil || !errors.Is(err, context.Canceled)) {
			logger.Error("sync run failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
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

	cfg.syncInterval = 5 * time.Minute
	if raw := os.Getenv("SYNC_INTERVAL"); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			return config{}, fmt.Errorf("invalid SYNC_INTERVAL: %q must be a positive duration", raw)
		}
		cfg.syncInterval = interval
	}

	if raw := os.Getenv("DRY_RUN"); raw != "" {
		dryRun, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid DRY_RUN: %q must be a boolean", raw)
		}
		cfg.dryRun = dryRun
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
