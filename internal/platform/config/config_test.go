package config_test

import (
	"log/slog"
	"maps"
	"strings"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// valid returns a complete production environment with overrides applied.
func valid(overrides map[string]string) map[string]string {
	vars := map[string]string{
		"APP_ENV":      "production",
		"DATABASE_URL": "postgres://db",
		"SUPABASE_URL": "https://abc.supabase.co",
	}
	maps.Copy(vars, overrides)
	return vars
}

func TestLoad_ValidEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		vars     map[string]string
		wantPort int
		wantAddr string
		wantLvl  slog.Level
	}{
		{
			name:     "defaults port to 8080",
			vars:     valid(nil),
			wantPort: 8080,
			wantAddr: ":8080",
			wantLvl:  slog.LevelInfo,
		},
		{
			name:     "reads port and debug level in development",
			vars:     valid(map[string]string{"APP_ENV": "development", "PORT": "9000"}),
			wantPort: 9000,
			wantAddr: ":9000",
			wantLvl:  slog.LevelDebug,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load(env(tt.vars))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", cfg.Port, tt.wantPort)
			}
			if got := cfg.HTTPAddr(); got != tt.wantAddr {
				t.Errorf("HTTPAddr() = %q, want %q", got, tt.wantAddr)
			}
			if got := cfg.LogLevel(); got != tt.wantLvl {
				t.Errorf("LogLevel() = %v, want %v", got, tt.wantLvl)
			}
			if cfg.DatabaseURL != "postgres://db" {
				t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, "postgres://db")
			}
		})
	}
}

func TestLoad_SupabaseURLs(t *testing.T) {
	tests := []struct {
		name       string
		vars       map[string]string
		wantURL    string
		wantJWKS   string
		wantIssuer string
	}{
		{
			name:       "derives JWKS and issuer",
			vars:       valid(nil),
			wantURL:    "https://abc.supabase.co",
			wantJWKS:   "https://abc.supabase.co/auth/v1/.well-known/jwks.json",
			wantIssuer: "https://abc.supabase.co/auth/v1",
		},
		{
			name:       "trims trailing slash",
			vars:       valid(map[string]string{"SUPABASE_URL": "https://abc.supabase.co/"}),
			wantURL:    "https://abc.supabase.co",
			wantJWKS:   "https://abc.supabase.co/auth/v1/.well-known/jwks.json",
			wantIssuer: "https://abc.supabase.co/auth/v1",
		},
		{
			name:       "JWKS override",
			vars:       valid(map[string]string{"SUPABASE_JWKS_URL": "https://keys.example.com/jwks.json"}),
			wantURL:    "https://abc.supabase.co",
			wantJWKS:   "https://keys.example.com/jwks.json",
			wantIssuer: "https://abc.supabase.co/auth/v1",
		},
		{
			name:       "http allowed in development for a local stack",
			vars:       valid(map[string]string{"APP_ENV": "development", "SUPABASE_URL": "http://127.0.0.1:54321"}),
			wantURL:    "http://127.0.0.1:54321",
			wantJWKS:   "http://127.0.0.1:54321/auth/v1/.well-known/jwks.json",
			wantIssuer: "http://127.0.0.1:54321/auth/v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load(env(tt.vars))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.SupabaseURL != tt.wantURL {
				t.Errorf("SupabaseURL = %q, want %q", cfg.SupabaseURL, tt.wantURL)
			}
			if cfg.JWKSURL != tt.wantJWKS {
				t.Errorf("JWKSURL = %q, want %q", cfg.JWKSURL, tt.wantJWKS)
			}
			if got := cfg.AuthIssuer(); got != tt.wantIssuer {
				t.Errorf("AuthIssuer() = %q, want %q", got, tt.wantIssuer)
			}
		})
	}
}

func TestLoad_ObservabilityIsOptional(t *testing.T) {
	cfg, err := config.Load(env(valid(nil)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OTLPEndpoint != "" || cfg.SentryDSN != "" {
		t.Errorf("OTLPEndpoint = %q, SentryDSN = %q; want both empty (off)", cfg.OTLPEndpoint, cfg.SentryDSN)
	}
}

func TestLoad_ObservabilitySettings(t *testing.T) {
	tests := []struct {
		name     string
		vars     map[string]string
		wantOTLP string
	}{
		{"https collector", valid(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otlp.example.com"}), "https://otlp.example.com"},
		{"local collector over http in production", valid(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4318"}), "http://localhost:4318"},
		{"loopback IP over http", valid(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318"}), "http://127.0.0.1:4318"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load(env(tt.vars))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.OTLPEndpoint != tt.wantOTLP {
				t.Errorf("OTLPEndpoint = %q, want %q", cfg.OTLPEndpoint, tt.wantOTLP)
			}
		})
	}

	dsn := "https://publickey123@o1.ingest.sentry.io/42"
	cfg, err := config.Load(env(valid(map[string]string{"SENTRY_DSN": dsn})))
	if err != nil || cfg.SentryDSN != dsn {
		t.Errorf("Load(SENTRY_DSN) = %q, %v; want %q", cfg.SentryDSN, err, dsn)
	}
}

func TestLoad_RejectsBadObservabilitySettings(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantMsg string
	}{
		{"remote collector over http in production", valid(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otlp.example.com"}), "OTEL_EXPORTER_OTLP_ENDPOINT"},
		{"sentry DSN without key", valid(map[string]string{"SENTRY_DSN": "https://o1.ingest.sentry.io/42"}), "SENTRY_DSN"},
		{"sentry DSN without project", valid(map[string]string{"SENTRY_DSN": "https://secretkey9@o1.ingest.sentry.io/"}), "SENTRY_DSN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(env(tt.vars))
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to mention %s", err, tt.wantMsg)
			}
			if strings.Contains(err.Error(), "secretkey9") {
				t.Errorf("error leaks the Sentry key: %q", err)
			}
		})
	}
}

func TestLoad_MissingRequiredVarsListsAll(t *testing.T) {
	_, err := config.Load(env(map[string]string{"DATABASE_URL": "   "}))
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "missing required environment variables: APP_ENV, DATABASE_URL, SUPABASE_URL") {
		t.Errorf("error = %q, want APP_ENV, DATABASE_URL, and SUPABASE_URL listed", err)
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantMsg string
	}{
		{"unknown APP_ENV", valid(map[string]string{"APP_ENV": "prod"}), `APP_ENV="prod"`},
		{"non-numeric PORT", valid(map[string]string{"PORT": "http"}), `PORT="http"`},
		{"PORT out of range", valid(map[string]string{"PORT": "70000"}), `PORT="70000"`},
		{"SUPABASE_URL over http in production", valid(map[string]string{"SUPABASE_URL": "http://abc.supabase.co"}), "must use https"},
		{"SUPABASE_URL not absolute", valid(map[string]string{"SUPABASE_URL": "abc.supabase.co"}), `SUPABASE_URL="abc.supabase.co"`},
		{"SUPABASE_URL only a slash", valid(map[string]string{"SUPABASE_URL": "/"}), "SUPABASE_URL"},
		{"JWKS override over http in staging", valid(map[string]string{"APP_ENV": "staging", "SUPABASE_JWKS_URL": "http://keys"}), "SUPABASE_JWKS_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(env(tt.vars))
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to mention %s", err, tt.wantMsg)
			}
		})
	}
}

func TestLoad_ReportsEveryProblemAtOnce(t *testing.T) {
	_, err := config.Load(env(map[string]string{"APP_ENV": "prod", "PORT": "0"}))
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	for _, want := range []string{"DATABASE_URL", "SUPABASE_URL", `APP_ENV="prod"`, `PORT="0"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %s", err, want)
		}
	}
}
