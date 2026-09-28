package payments_test

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

var shopTenant = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// memSettings keeps the payment settings in memory.
type memSettings struct {
	policy *payments.Policy
	rules  map[payments.Method]payments.FeeRule
	audits []audit.Entry
	locks  int
}

func (m *memSettings) Policy(context.Context, uuid.UUID) (payments.Policy, error) {
	if m.policy == nil {
		return payments.Policy{}, apperr.ErrNotFound
	}
	return *m.policy, nil
}

func (m *memSettings) FeeRules(context.Context, uuid.UUID) (map[payments.Method]payments.FeeRule, error) {
	return maps.Clone(m.rules), nil
}

func (m *memSettings) LockSettings(context.Context, uuid.UUID) error {
	m.locks++
	return nil
}

func (m *memSettings) SavePolicy(_ context.Context, _ uuid.UUID, p payments.Policy, at time.Time) error {
	p.UpdatedAt = at
	m.policy = &p
	return nil
}

func (m *memSettings) SaveFeeRule(_ context.Context, _ uuid.UUID, r payments.FeeRule, at time.Time) error {
	r.UpdatedAt = at
	m.rules[r.Method] = r
	return nil
}

func (m *memSettings) DeleteFeeRule(_ context.Context, _ uuid.UUID, method payments.Method) (bool, error) {
	_, had := m.rules[method]
	delete(m.rules, method)
	return had, nil
}

func (m *memSettings) Audit(_ context.Context, e audit.Entry) error {
	m.audits = append(m.audits, e)
	return nil
}

type caller struct {
	roles []identity.Role
	anon  bool
	user  uuid.UUID
}

func (c *caller) Principal(context.Context) (identity.Principal, error) {
	if c.anon {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	return identity.Principal{AuthUserID: c.user, TenantID: shopTenant, Roles: c.roles}, nil
}

type directTx struct{}

func (directTx) Tx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type settingsShop struct {
	repo     *memSettings
	caller   *caller
	clock    *clock.Fake
	settings *payments.Settings
}

func newSettingsShop() *settingsShop {
	s := &settingsShop{
		repo:   &memSettings{rules: map[payments.Method]payments.FeeRule{}},
		caller: &caller{roles: []identity.Role{identity.RoleOwner}, user: uuid.New()},
		clock:  clock.NewFake(at),
	}
	s.settings = payments.NewSettings(payments.SettingsDeps{Repo: s.repo, Principals: s.caller, Tx: directTx{}, Clock: s.clock})
	return s
}

func method(v payments.SettingsView, m payments.Method) payments.MethodSettings {
	for _, ms := range v.Methods {
		if ms.Rule.Method == m {
			return ms
		}
	}
	return payments.MethodSettings{}
}

func examples(ms payments.MethodSettings) []int64 {
	var fees []int64
	for _, e := range ms.Examples {
		fees = append(fees, e.FeeIDR)
	}
	return fees
}

func fieldError(err error, field string) string {
	var v *apperr.ValidationError
	if errors.As(err, &v) {
		return v.Fields[field]
	}
	return ""
}

func TestSettings_Defaults(t *testing.T) {
	s := newSettingsShop()
	s.caller.roles = []identity.Role{identity.RoleKitchen}

	v, err := s.settings.Get(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if v.Policy != payments.DefaultPolicy() || !v.Policy.UpdatedAt.IsZero() {
		t.Errorf("policy = %+v, want the defaults", v.Policy)
	}
	if len(v.Methods) != 4 || v.Methods[0].Rule.Method != payments.MethodQRIS {
		t.Fatalf("methods = %+v, want all four, QRIS first", v.Methods)
	}
	for _, ms := range v.Methods {
		if ms.Rule != ms.Default || len(ms.Examples) != len(payments.ExampleAmounts) || ms.Examples[1].AmountIDR != 150_000 {
			t.Errorf("%s = %+v, want the default rule and its examples", ms.Rule.Method, ms)
		}
	}
	if got := examples(method(v, payments.MethodBankTransfer)); got[0] != 4440 || got[2] != 4440 {
		t.Errorf("bank transfer examples = %v, want Rp4.440 on every amount", got)
	}
	if got := examples(method(v, payments.MethodEWallet)); got[1] != 3062 {
		t.Errorf("e-wallet examples = %v, want Rp3.062 on Rp150.000 (2%%, grossed up)", got)
	}
}

func TestSettings_UpdatePolicy(t *testing.T) {
	s := newSettingsShop()
	in := payments.DefaultPolicy()
	in.DPMinTotalIDR, in.DPMinPercent = 100_000, 40

	v, err := s.settings.UpdatePolicy(t.Context(), in, "  DP juga untuk pesanan Rp100.000 ke atas  ")

	if err != nil {
		t.Fatal(err)
	}
	if v.Policy.DPMinTotalIDR != 100_000 || v.Policy.DPMinPercent != 40 || !v.Policy.UpdatedAt.Equal(at) {
		t.Errorf("policy = %+v, want the new threshold and percentage", v.Policy)
	}
	if len(s.repo.audits) != 1 || s.repo.locks != 1 {
		t.Fatalf("%d audits after %d locks, want one of each", len(s.repo.audits), s.repo.locks)
	}
	a := s.repo.audits[0]
	before, _ := a.Before.(map[string]any)
	after, _ := a.After.(map[string]any)
	if a.Action != "payments.policy.changed" || a.Reason != "DP juga untuk pesanan Rp100.000 ke atas" || a.ActorID != s.caller.user ||
		a.TenantID != shopTenant || before["dp_min_total_idr"] != int64(150_000) || before["default"] != true ||
		after["dp_min_total_idr"] != int64(100_000) || after["dp_min_percent"] != int32(40) || after["default"] != false {
		t.Errorf("audit = %+v", a)
	}

	s.clock.Advance(time.Hour)
	if v, err := s.settings.UpdatePolicy(t.Context(), in, "Sama saja"); err != nil || len(s.repo.audits) != 1 || !v.Policy.UpdatedAt.Equal(at) {
		t.Errorf("saving the same policy: %v, %d audits, updated %v; want nothing changed", err, len(s.repo.audits), v.Policy.UpdatedAt)
	}
}

func TestSettings_UpdatePolicyRejects(t *testing.T) {
	s := newSettingsShop()
	in := payments.DefaultPolicy()
	in.DPMinPercent, in.DPMinTotalIDR = 0, -1

	_, err := s.settings.UpdatePolicy(t.Context(), in, " ")

	for _, field := range []string{"dp_min_percent", "dp_min_total_idr", "reason"} {
		if fieldError(err, field) == "" {
			t.Errorf("error = %v, want a message on %s", err, field)
		}
	}
	if s.repo.policy != nil || len(s.repo.audits) != 0 {
		t.Error("a refused policy was saved")
	}
}

func TestSettings_UpdateMethod(t *testing.T) {
	s := newSettingsShop()
	va := payments.FeeRule{Method: payments.MethodBankTransfer, FixedIDR: 3500, Enabled: true}

	v, err := s.settings.UpdateMethod(t.Context(), va, "Midtrans turunkan tarif VA")

	if err != nil {
		t.Fatal(err)
	}
	ms := method(v, payments.MethodBankTransfer)
	if ms.Rule.FixedIDR != 3500 || !ms.Rule.UpdatedAt.Equal(at) || ms.Default.FixedIDR != 4000 || examples(ms)[0] != 3885 {
		t.Errorf("bank transfer = %+v, want Rp3.500 + PPN = Rp3.885 next to the default", ms)
	}
	a := s.repo.audits[0]
	if before, _ := a.Before.(map[string]any); a.Action != "payments.method_fee.changed" || before["fixed_idr"] != int64(4000) || before["default"] != true {
		t.Errorf("audit = %+v", a)
	}
	if _, err := s.settings.UpdateMethod(t.Context(), va, "Sama"); err != nil || len(s.repo.audits) != 1 {
		t.Errorf("saving the same rule: %v, %d audits; want nothing changed", err, len(s.repo.audits))
	}
}

// Whatever rate is typed for QRIS, the customer pays nothing for it: Bank
// Indonesia forbids it. The rate only records the shop's own cost.
func TestSettings_QRISStaysFree(t *testing.T) {
	s := newSettingsShop()

	v, err := s.settings.UpdateMethod(t.Context(), payments.FeeRule{Method: payments.MethodQRIS, FixedIDR: 1000, RateBPS: 100, Enabled: true}, "Catat biaya QRIS")

	if err != nil {
		t.Fatal(err)
	}
	for _, fee := range examples(method(v, payments.MethodQRIS)) {
		if fee != 0 {
			t.Errorf("QRIS examples = %v, want Rp0", examples(method(v, payments.MethodQRIS)))
		}
	}
}

func TestSettings_KeepsOneMethodOnOffer(t *testing.T) {
	s := newSettingsShop()
	off := func(m payments.Method) error {
		rule := payments.DefaultFeeRules()[m]
		rule.Enabled = false
		_, err := s.settings.UpdateMethod(t.Context(), rule, "Matikan")
		return err
	}
	for _, m := range payments.Methods[1:] {
		if err := off(m); err != nil {
			t.Fatalf("switching off %s: %v", m, err)
		}
	}

	err := off(payments.MethodQRIS)

	var p *apperr.PreconditionError
	if !errors.As(err, &p) || p.Reason != "last_method" {
		t.Errorf("switching off the last method: error = %v, want last_method", err)
	}
	if _, ok := s.repo.rules[payments.MethodQRIS]; ok || len(s.repo.audits) != 3 {
		t.Errorf("the refusal saved something: %+v, %d audits", s.repo.rules, len(s.repo.audits))
	}
}

func TestSettings_UpdateMethodRejects(t *testing.T) {
	s := newSettingsShop()
	for name, rule := range map[string]payments.FeeRule{
		"method":    {Method: "cash", Enabled: true},
		"fixed_idr": {Method: payments.MethodMinimarket, FixedIDR: -1, Enabled: true},
		"rate_bps":  {Method: payments.MethodEWallet, RateBPS: 5001, Enabled: true},
	} {
		if _, err := s.settings.UpdateMethod(t.Context(), rule, "x"); fieldError(err, name) == "" {
			t.Errorf("UpdateMethod(%+v) error = %v, want a message on %s", rule, err, name)
		}
	}
	if _, err := s.settings.UpdateMethod(t.Context(), payments.DefaultFeeRules()[payments.MethodEWallet], ""); fieldError(err, "reason") == "" {
		t.Errorf("UpdateMethod() without a reason: error = %v", err)
	}
	if len(s.repo.rules) != 0 {
		t.Errorf("refused rules were saved: %+v", s.repo.rules)
	}
}

func TestSettings_ResetMethod(t *testing.T) {
	s := newSettingsShop()
	_, err := s.settings.UpdateMethod(t.Context(), payments.FeeRule{Method: payments.MethodMinimarket, FixedIDR: 6000}, "Matikan minimarket")
	if err != nil {
		t.Fatal(err)
	}

	v, err := s.settings.ResetMethod(t.Context(), payments.MethodMinimarket, "Kembali ke tarif Midtrans")

	if err != nil {
		t.Fatal(err)
	}
	if ms := method(v, payments.MethodMinimarket); ms.Rule != ms.Default || !ms.Rule.Enabled {
		t.Errorf("minimarket = %+v, want the default, on offer again", ms.Rule)
	}
	a := s.repo.audits[len(s.repo.audits)-1]
	if before, _ := a.Before.(map[string]any); a.Action != "payments.method_fee.reset" || before["fixed_idr"] != int64(6000) || before["enabled"] != false {
		t.Errorf("audit = %+v", a)
	}
	if _, err := s.settings.ResetMethod(t.Context(), payments.MethodMinimarket, "Lagi"); err != nil || len(s.repo.audits) != 2 {
		t.Errorf("resetting a default: %v, %d audits; want nothing changed", err, len(s.repo.audits))
	}
	if _, err := s.settings.ResetMethod(t.Context(), "cash", "x"); fieldError(err, "method") == "" {
		t.Errorf("ResetMethod(cash) error = %v, want a message on method", err)
	}
}

func TestSettings_Roles(t *testing.T) {
	policy := payments.DefaultPolicy()
	calls := map[string]struct {
		owners bool
		call   func(s *settingsShop) error
	}{
		"Get": {false, func(s *settingsShop) error { _, err := s.settings.Get(t.Context()); return err }},
		"UpdatePolicy": {true, func(s *settingsShop) error {
			_, err := s.settings.UpdatePolicy(t.Context(), policy, "x")
			return err
		}},
		"UpdateMethod": {true, func(s *settingsShop) error {
			_, err := s.settings.UpdateMethod(t.Context(), payments.DefaultFeeRules()[payments.MethodEWallet], "x")
			return err
		}},
		"ResetMethod": {true, func(s *settingsShop) error {
			_, err := s.settings.ResetMethod(t.Context(), payments.MethodEWallet, "x")
			return err
		}},
	}
	for name, c := range calls {
		s := newSettingsShop()
		if err := c.call(s); err != nil {
			t.Errorf("owner %s: %v", name, err)
		}
		s.caller.roles = []identity.Role{identity.RoleKitchen}
		if err := c.call(s); c.owners != errors.Is(err, apperr.ErrForbidden) {
			t.Errorf("kitchen %s: error = %v, want forbidden %t", name, err, c.owners)
		}
		s.caller.roles = nil
		if err := c.call(s); !errors.Is(err, apperr.ErrForbidden) {
			t.Errorf("customer %s: error = %v, want ErrForbidden", name, err)
		}
		s.caller.anon = true
		if err := c.call(s); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Errorf("nobody %s: error = %v, want ErrUnauthenticated", name, err)
		}
	}
}
