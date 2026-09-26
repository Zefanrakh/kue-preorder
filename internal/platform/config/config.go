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
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Zefanrakh/kue-preorder/internal/platform/webhook"
)

var (
	// headerName matches an HTTP header name (RFC 9110 token).
	headerName   = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]+$`)
	digits       = regexp.MustCompile(`^[0-9]+$`)
	templateName = regexp.MustCompile(`^[a-z0-9_]{1,512}$`)
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
	// OTLPEndpoint turns tracing on when set. The OpenTelemetry SDK reads the
	// rest of its OTEL_* variables (headers, service name, sampler) itself.
	OTLPEndpoint string
	// SentryDSN turns error reporting to Sentry on when set.
	SentryDSN string
	// ClientIPHeader names the header in which the proxy in front of the API
	// puts the caller's IP, such as CF-Connecting-IP or Fly-Client-IP; for
	// X-Forwarded-For the last entry is used. Rate limits count per that IP.
	// Required in production, where the connection's address is the proxy.
	// Empty elsewhere: the connection's address is used.
	ClientIPHeader string
	// SendSMSHookSecret verifies Supabase Auth's Send SMS hook, which hands
	// over sign-in codes (§8): "v1,whsec_<base64>". Required in production;
	// without it the hook is not served.
	SendSMSHookSecret string
	// WhatsApp sends sign-in codes and, later, notifications. All three or
	// none; required in production. Without them, development logs the codes.
	WhatsAppToken         string
	WhatsAppPhoneNumberID string
	WhatsAppOTPTemplate   string
}

// HasWhatsApp reports whether WhatsApp is configured.
func (c Config) HasWhatsApp() bool { return c.WhatsAppToken != "" }

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

	if raw := strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); raw != "" {
		if err := checkURL(raw, cfg.AppEnv); err != nil {
			errs = append(errs, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT=%q: %w", raw, err))
		} else {
			cfg.OTLPEndpoint = raw
		}
	}
	if raw := strings.TrimSpace(getenv("SENTRY_DSN")); raw != "" {
		// The DSN embeds a key; keep it out of error messages.
		if err := checkSentryDSN(raw, cfg.AppEnv); err != nil {
			errs = append(errs, fmt.Errorf("SENTRY_DSN: %w", err))
		} else {
			cfg.SentryDSN = raw
		}
	}

	if raw := strings.TrimSpace(getenv("CLIENT_IP_HEADER")); raw != "" {
		if !headerName.MatchString(raw) {
			errs = append(errs, fmt.Errorf("CLIENT_IP_HEADER=%q: must be an HTTP header name", raw))
		} else {
			cfg.ClientIPHeader = raw
		}
	} else if cfg.AppEnv == EnvProduction {
		missing = append(missing, "CLIENT_IP_HEADER")
	}

	if raw := strings.TrimSpace(getenv("SUPABASE_SEND_SMS_HOOK_SECRET")); raw != "" {
		// The secret stays out of error messages.
		if _, err := webhook.NewStandardVerifier(raw); err != nil {
			errs = append(errs, fmt.Errorf("SUPABASE_SEND_SMS_HOOK_SECRET: %w", err))
		} else {
			cfg.SendSMSHookSecret = raw
		}
	} else if cfg.AppEnv == EnvProduction {
		missing = append(missing, "SUPABASE_SEND_SMS_HOOK_SECRET")
	}

	wa := map[string]string{}
	for _, key := range []string{"WHATSAPP_API_TOKEN", "WHATSAPP_PHONE_NUMBER_ID", "WHATSAPP_OTP_TEMPLATE"} {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			wa[key] = v
		}
	}
	switch {
	case len(wa) == 3:
		if !digits.MatchString(wa["WHATSAPP_PHONE_NUMBER_ID"]) {
			errs = append(errs, fmt.Errorf("WHATSAPP_PHONE_NUMBER_ID=%q: must be the number's id, digits only", wa["WHATSAPP_PHONE_NUMBER_ID"]))
		}
		if !templateName.MatchString(wa["WHATSAPP_OTP_TEMPLATE"]) {
			errs = append(errs, fmt.Errorf("WHATSAPP_OTP_TEMPLATE=%q: must be a template name, lowercase letters, digits, and underscores", wa["WHATSAPP_OTP_TEMPLATE"]))
		}
		cfg.WhatsAppToken, cfg.WhatsAppPhoneNumberID, cfg.WhatsAppOTPTemplate = wa["WHATSAPP_API_TOKEN"], wa["WHATSAPP_PHONE_NUMBER_ID"], wa["WHATSAPP_OTP_TEMPLATE"]
	case len(wa) > 0:
		errs = append(errs, errors.New("WHATSAPP_API_TOKEN, WHATSAPP_PHONE_NUMBER_ID, and WHATSAPP_OTP_TEMPLATE go together: set all three or none"))
	case cfg.AppEnv == EnvProduction:
		missing = append(missing, "WHATSAPP_API_TOKEN", "WHATSAPP_PHONE_NUMBER_ID", "WHATSAPP_OTP_TEMPLATE")
	}

	if len(missing) > 0 {
		errs = append([]error{fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))}, errs...)
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

// checkURL accepts an absolute https URL. Plain http is allowed in development
// (a local Supabase stack) and to a loopback address in any environment (an
// OpenTelemetry collector running next to the process).
func checkURL(raw string, env Env) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "":
		return errors.New("must be an absolute URL")
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && (env == EnvDevelopment || isLoopback(u.Hostname())):
		return nil
	default:
		return errors.New("must use https")
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkSentryDSN checks the shape https://<public key>@<host>/<project id>.
// Its errors never echo the DSN.
func checkSentryDSN(raw string, env Env) error {
	if err := checkURL(raw, env); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	if u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" {
		return errors.New("must look like https://<public key>@<host>/<project id>")
	}
	return nil
}
