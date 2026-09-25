// Package dbtest gives integration tests a real, migrated Postgres.
//
// Each test package starts one container from TestMain via Main. Migrations run
// once into a template database; every test then gets its own database cloned
// from the template, so tests are isolated and may run in parallel.
//
// Integration tests carry the build tag "integration" and need Docker:
//
//	go test -race -tags=integration ./...
package dbtest

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
)

const (
	image        = "postgres:17-alpine" // Supabase runs Postgres 17.
	templateName = "kue_template"
)

// DefaultTenantID is the tenant seeded by migration 00002.
var DefaultTenantID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

var (
	serverURL *url.URL   // the container's maintenance database
	createMu  sync.Mutex // CREATE DATABASE ... TEMPLATE fails if run concurrently on one template
)

// Main runs the package's tests against a fresh Postgres container and removes
// the container afterwards. Call it from TestMain:
//
//	func TestMain(m *testing.M) { dbtest.Main(m) }
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := startPostgres(ctx)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintf(os.Stderr, "dbtest: remove postgres container: %v\n", err)
		}
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: start postgres (is Docker running?): %v\n", err)
		return 1
	}
	raw, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: connection string: %v\n", err)
		return 1
	}
	if serverURL, err = url.Parse(raw); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: parse connection string: %v\n", err)
		return 1
	}
	if err := execSQL(ctx, "create database "+pgx.Identifier{templateName}.Sanitize()); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: create template database: %v\n", err)
		return 1
	}
	if err := db.Migrate(ctx, urlFor(templateName)); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: migrate template database: %v\n", err)
		return 1
	}
	return m.Run()
}

// startAttempts bounds retries of the container start, a second line of
// defence behind pinDockerHost.
const startAttempts = 3

func startPostgres(ctx context.Context) (*postgres.PostgresContainer, error) {
	pinDockerHost(ctx)
	for attempt := 1; ; attempt++ {
		ctr, err := postgres.Run(ctx, image, postgres.BasicWaitStrategies())
		if err == nil || attempt == startAttempts {
			return ctr, err
		}
		_ = testcontainers.TerminateContainer(ctr)
		fmt.Fprintf(os.Stderr, "dbtest: start postgres, attempt %d of %d: %v; retrying\n", attempt, startAttempts, err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
}

// pinDockerHost points testcontainers at the Docker CLI's current context on
// Windows. Left to itself, testcontainers probes for Docker, and when several
// test packages start containers at once that probe sometimes fails and falls
// through to a "rootless Docker" strategy Windows does not support. Measured
// on Docker Desktop: 1 of 3 full integration runs failed without a pinned
// host, 0 of 3 with it. An explicit DOCKER_HOST is left alone.
func pinDockerHost(ctx context.Context) {
	if runtime.GOOS != "windows" || os.Getenv("DOCKER_HOST") != "" {
		return
	}
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return // no docker CLI: keep testcontainers' own detection
	}
	if host := strings.TrimSpace(string(out)); host != "" {
		_ = os.Setenv("DOCKER_HOST", host)
	}
}

// New returns a pool on a fresh database migrated to the latest version. The
// database is dropped when the test ends.
func New(t *testing.T) *db.DB {
	t.Helper()
	name := createDatabase(t, templateName)
	d, err := db.Open(t.Context(), urlFor(name))
	if err != nil {
		t.Fatalf("dbtest: open %s: %v", name, err)
	}
	t.Cleanup(d.Close)
	return d
}

// NewEmptyURL returns the URL of a fresh database with no migrations applied,
// for tests that drive goose themselves. The database is dropped when the test
// ends.
func NewEmptyURL(t *testing.T) string {
	t.Helper()
	return urlFor(createDatabase(t, "template1"))
}

// CreateTenant inserts a tenant for tests that need more than the seeded one.
func CreateTenant(t *testing.T, d *db.DB, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := d.Pool().QueryRow(t.Context(), "insert into tenants (name) values ($1) returning id", name).Scan(&id); err != nil {
		t.Fatalf("dbtest: create tenant %q: %v", name, err)
	}
	return id
}

func createDatabase(t *testing.T, template string) string {
	t.Helper()
	if serverURL == nil {
		t.Fatal("dbtest: the package's TestMain must call dbtest.Main")
	}
	name := "test_" + strings.ToLower(rand.Text())
	ident := pgx.Identifier{name}.Sanitize()

	createMu.Lock()
	err := execSQL(t.Context(), "create database "+ident+" template "+pgx.Identifier{template}.Sanitize())
	createMu.Unlock()
	if err != nil {
		t.Fatalf("dbtest: create database: %v", err)
	}
	t.Cleanup(func() {
		// t.Context() is already cancelled when cleanups run.
		if err := execSQL(context.WithoutCancel(t.Context()), "drop database if exists "+ident+" with (force)"); err != nil {
			t.Errorf("dbtest: drop database %s: %v", name, err)
		}
	})
	return name
}

// execSQL runs one statement on the maintenance database.
func execSQL(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, serverURL.String())
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("%s: %w", sql, err)
	}
	return nil
}

func urlFor(database string) string {
	u := *serverURL
	u.Path = "/" + database
	return u.String()
}
