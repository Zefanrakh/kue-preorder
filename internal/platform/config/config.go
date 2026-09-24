// Package config loads and validates process configuration from environment
// variables (docs/architecture.md §28). A process refuses to start when its
// configuration is invalid, and reports every problem at once.
//
// Only variables used by code that exists today are required. Each module adds
// its own variables here when it is built.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
)

// Env is the deployment environment.
type Env string

// Deployment environments accepted in APP_ENV.
const (
	EnvDevelopment Env = "development"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

const defaultPort = 8080

// Config is the validated process configuration.
type Config struct {
	AppEnv      Env
	DatabaseURL string
	Port        int
	// SupabaseURL is the project URL without a trailing slash,
	// e.g. https://<ref>.supabase.co.
	SupabaseURL string
	// JWKSURL serves the public keys of Supabase Auth. It defaults to the
	// project's well-known JWKS endpoint.
	JWKSURL string
}

// HTTPAddr is the address the API server listens on.
func (c Config) HTTPAddr() string {
	return ":" + strconv.Itoa(c.Port)
}

// AuthIssuer is the "iss" claim Supabase Auth puts in access tokens.
func (c Config) AuthIssuer() string {
	return c.SupabaseURL + "/auth/v1"
}

// LogLevel is debug in development and info elsewhere.
func (c Config) LogLevel() slog.Level {
	if c.AppEnv == EnvDevelopment {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// Load reads the configuration through getenv, usually os.Getenv.
func Load(getenv func(string) string) (Config, error) {
	var (
		cfg     Config
		missing []string
		errs    []error
	)
	required := func(key string) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	switch env := Env(required("APP_ENV")); env {
	case EnvDevelopment, EnvStaging, EnvProduction:
		cfg.AppEnv = env
	case "":
	default:
		errs = append(errs, fmt.Errorf("APP_ENV=%q: must be one of development, staging, production", env))
	}

	cfg.DatabaseURL = required("DATABASE_URL")

	cfg.Port = defaultPort
	if raw := strings.TrimSpace(getenv("PORT")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("PORT=%q: must be a number between 1 and 65535", raw))
		} else {
			cfg.Port = port
		}
	}

	if raw := required("SUPABASE_URL"); raw != "" {
		raw = strings.TrimRight(raw, "/")
		if err := checkURL(raw, cfg.AppEnv); err != nil {
			errs = append(errs, fmt.Errorf("SUPABASE_URL=%q: %w", raw, err))
		} else {
			cfg.SupabaseURL = raw
			cfg.JWKSURL = raw + "/auth/v1/.well-known/jwks.json"
		}
	}
	if raw := strings.TrimSpace(getenv("SUPABASE_JWKS_URL")); raw != "" {
		if err := checkURL(raw, cfg.AppEnv); err != nil {
			errs = append(errs, fmt.Errorf("SUPABASE_JWKS_URL=%q: %w", raw, err))
		} else {
			cfg.JWKSURL = raw
		}
	}

	if len(missing) > 0 {
		errs = append([]error{fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))}, errs...)
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

// checkURL accepts an absolute https URL, or http in development for a local
// Supabase stack.
func checkURL(raw string, env Env) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "":
		return errors.New("must be an absolute URL")
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && env == EnvDevelopment:
		return nil
	default:
		return errors.New("must use https")
	}
}
