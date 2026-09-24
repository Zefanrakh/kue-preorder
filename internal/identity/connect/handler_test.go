package connect_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	identityv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/identity/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/identity/v1/identityv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	identityrpc "github.com/Zefanrakh/kue-preorder/internal/identity/connect"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

var now = time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)

type testServer struct {
	client identityv1connect.IdentityServiceClient
	issuer *identitytest.TokenIssuer
	repo   *identitytest.Repository
	tenant uuid.UUID
	logs   *bytes.Buffer
}

// newTestServer wires the identity service exactly as cmd/api does, over a
// real HTTP server, with an in-memory repository and a local token issuer.
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	tenant := uuid.New()
	repo := identitytest.NewRepository(tenant)
	issuer := identitytest.NewTokenIssuer(t)

	keys, err := identity.NewJWKS(t.Context(), issuer.JWKSURL, logger)
	if err != nil {
		t.Fatalf("NewJWKS() error = %v", err)
	}
	tenants, err := identity.ResolveSingleTenant(t.Context(), repo)
	if err != nil {
		t.Fatalf("ResolveSingleTenant() error = %v", err)
	}
	verifier := identity.NewTokenVerifier(keys, identitytest.Issuer, clock.NewFake(now))
	handler := identityrpc.NewHandler(identity.NewService(repo, tenants), logger)

	mux := http.NewServeMux()
	mux.Handle(identityv1connect.NewIdentityServiceHandler(handler,
		connect.WithInterceptors(identityrpc.NewAuthInterceptor(verifier, logger))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &testServer{
		client: identityv1connect.NewIdentityServiceClient(srv.Client(), srv.URL),
		issuer: issuer,
		repo:   repo,
		tenant: tenant,
		logs:   &logs,
	}
}

func (s *testServer) whoAmI(t *testing.T, authorization string) (*identityv1.WhoAmIResponse, error) {
	t.Helper()
	req := connect.NewRequest(&identityv1.WhoAmIRequest{})
	if authorization != "" {
		req.Header().Set("Authorization", authorization)
	}
	res, err := s.client.WhoAmI(t.Context(), req)
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func (s *testServer) bearer(t *testing.T, user uuid.UUID) string {
	t.Helper()
	return "Bearer " + s.issuer.Sign(t, identitytest.Claims(user, now))
}

func TestWhoAmI_StaffMember(t *testing.T) {
	s := newTestServer(t)
	user := uuid.New()
	s.repo.Grant(s.tenant, user, identity.RoleOwner, identity.RoleKitchen)

	got, err := s.whoAmI(t, s.bearer(t, user))

	if err != nil {
		t.Fatalf("WhoAmI() error = %v", err)
	}
	if got.GetTenantId() != s.tenant.String() || got.GetAuthUserId() != user.String() {
		t.Errorf("WhoAmI() = %v, want user %s in tenant %s", got, user, s.tenant)
	}
	wantRoles := []identityv1.StaffRole{identityv1.StaffRole_STAFF_ROLE_KITCHEN, identityv1.StaffRole_STAFF_ROLE_OWNER}
	if !slices.Equal(got.GetRoles(), wantRoles) {
		t.Errorf("Roles = %v, want %v", got.GetRoles(), wantRoles)
	}
	if got.CustomerId != nil {
		t.Errorf("CustomerId = %q, want unset", got.GetCustomerId())
	}
}

func TestWhoAmI_Customer(t *testing.T) {
	s := newTestServer(t)
	user := uuid.New()
	customerID := s.repo.AddCustomer(s.tenant, user)

	got, err := s.whoAmI(t, s.bearer(t, user))

	if err != nil {
		t.Fatalf("WhoAmI() error = %v", err)
	}
	if got.GetCustomerId() != customerID.String() {
		t.Errorf("CustomerId = %q, want %s", got.GetCustomerId(), customerID)
	}
	if len(got.GetRoles()) != 0 {
		t.Errorf("Roles = %v, want none", got.GetRoles())
	}
}

func TestWhoAmI_AcceptsLowercaseBearerScheme(t *testing.T) {
	s := newTestServer(t)
	token := s.issuer.Sign(t, identitytest.Claims(uuid.New(), now))

	if _, err := s.whoAmI(t, "bearer "+token); err != nil {
		t.Errorf("WhoAmI() error = %v, want the scheme to be case-insensitive", err)
	}
}

func TestWhoAmI_RejectsUnauthenticatedCallers(t *testing.T) {
	s := newTestServer(t)
	expired := identitytest.Claims(uuid.New(), now.Add(-2*time.Hour))

	tests := []struct {
		name          string
		authorization string
	}{
		{"no token: the service refuses anonymous callers", ""},
		{"expired token", "Bearer " + s.issuer.Sign(t, expired)},
		{"garbage token", "Bearer not-a-jwt"},
		{"basic auth", "Basic dXNlcjpwYXNz"},
		{"bearer without token", "Bearer   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.whoAmI(t, tt.authorization)
			if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
				t.Errorf("WhoAmI() code = %v (error %v), want Unauthenticated", code, err)
			}
		})
	}
}

func TestWhoAmI_HidesInternalErrors(t *testing.T) {
	s := newTestServer(t)
	s.repo.FailWith(errors.New("connection to 10.0.0.5:5432 refused"))

	_, err := s.whoAmI(t, s.bearer(t, uuid.New()))

	if code := connect.CodeOf(err); code != connect.CodeInternal {
		t.Fatalf("WhoAmI() code = %v (error %v), want Internal", code, err)
	}
	if strings.Contains(err.Error(), "10.0.0.5") {
		t.Errorf("error leaks internal detail to the client: %v", err)
	}
	if !strings.Contains(s.logs.String(), "10.0.0.5") {
		t.Errorf("internal detail not logged; logs = %s", s.logs.String())
	}
}

func TestWhoAmI_NeverLogsTheToken(t *testing.T) {
	s := newTestServer(t)
	token := s.issuer.Sign(t, identitytest.Claims(uuid.New(), now.Add(-2*time.Hour))) // expired: gets logged

	_, _ = s.whoAmI(t, "Bearer "+token)

	if !strings.Contains(s.logs.String(), "access token rejected") {
		t.Fatalf("rejection not logged; logs = %s", s.logs.String())
	}
	if strings.Contains(s.logs.String(), token) {
		t.Error("logs contain the raw access token")
	}
}
