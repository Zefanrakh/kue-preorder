package config_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
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
			vars:     map[string]string{"APP_ENV": "production", "DATABASE_URL": "postgres://db"},
			wantPort: 8080,
			wantAddr: ":8080",
			wantLvl:  slog.LevelInfo,
		},
		{
			name:     "reads port and debug level in development",
			vars:     map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://db", "PORT": "9000"},
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

func TestLoad_MissingRequiredVarsListsAll(t *testing.T) {
	_, err := config.Load(env(map[string]string{"DATABASE_URL": "   "}))
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "missing required environment variables: APP_ENV, DATABASE_URL") {
		t.Errorf("error = %q, want both APP_ENV and DATABASE_URL listed", err)
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantMsg string
	}{
		{
			name:    "unknown APP_ENV",
			vars:    map[string]string{"APP_ENV": "prod", "DATABASE_URL": "postgres://db"},
			wantMsg: `APP_ENV="prod"`,
		},
		{
			name:    "non-numeric PORT",
			vars:    map[string]string{"APP_ENV": "staging", "DATABASE_URL": "postgres://db", "PORT": "http"},
			wantMsg: `PORT="http"`,
		},
		{
			name:    "PORT out of range",
			vars:    map[string]string{"APP_ENV": "staging", "DATABASE_URL": "postgres://db", "PORT": "70000"},
			wantMsg: `PORT="70000"`,
		},
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
	for _, want := range []string{"DATABASE_URL", `APP_ENV="prod"`, `PORT="0"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %s", err, want)
		}
	}
}
