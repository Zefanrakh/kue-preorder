package payments

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// SettingsRepository is the persistence port of the payment settings. It
// works in the caller's transaction.
type SettingsRepository interface {
	Repository
	// LockSettings serializes the tenant's changes to its payment settings
	// until the transaction ends.
	LockSettings(ctx context.Context, tenantID uuid.UUID) error
	SavePolicy(ctx context.Context, tenantID uuid.UUID, p Policy, at time.Time) error
	SaveFeeRule(ctx context.Context, tenantID uuid.UUID, r FeeRule, at time.Time) error
	// DeleteFeeRule reports whether the tenant had a rule for m.
	DeleteFeeRule(ctx context.Context, tenantID uuid.UUID, m Method) (bool, error)
	// Audit writes an audit entry in the transaction making the change.
	Audit(ctx context.Context, e audit.Entry) error
}

// Principals tells who is calling; identity.Service implements it.
type Principals interface {
	Principal(ctx context.Context) (identity.Principal, error)
}

// Transactor runs a unit of work in one transaction; *db.DB implements it.
type Transactor interface {
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Who may do what (§8): the owner sets the money rules; the kitchen sees them.
var (
	owners = []identity.Role{identity.RoleOwner}
	staff  = []identity.Role{identity.RoleOwner, identity.RoleKitchen}
)

// maxReason bounds the reason given for a change.
const maxReason = 500

// ExampleAmounts are the amounts the CMS shows each method's "Biaya admin"
// for, so a mistyped rate shows at once: a small order, the default DP
// threshold, and a large order.
var ExampleAmounts = []int64{50_000, 150_000, 500_000}

// ErrLastMethod refuses switching off the last method on offer: checkout
// would have no way to pay.
var ErrLastMethod = &apperr.PreconditionError{
	Reason:  "last_method",
	Message: "Minimal satu cara bayar harus tetap aktif.",
}

// SettingsView is what the CMS shows of the payment settings.
type SettingsView struct {
	// Policy has a zero UpdatedAt while the defaults are in use.
	Policy Policy
	// Methods lists every method in the order checkout shows them.
	Methods []MethodSettings
}

// MethodSettings is one method's fee rule as the CMS shows it.
type MethodSettings struct {
	// Rule is what applies; its UpdatedAt is zero while it is Default.
	Rule FeeRule
	// Default is Midtrans' published price, which a reset returns to.
	Default FeeRule
	// Examples is the "Biaya admin" on each of ExampleAmounts.
	Examples []ExampleFee
}

// ExampleFee is the "Biaya admin" on one amount.
type ExampleFee struct {
	AmountIDR, FeeIDR int64
}

// SettingsDeps are what Settings works with.
type SettingsDeps struct {
	Repo       SettingsRepository
	Principals Principals
	Tx         Transactor
	Clock      clock.Clock
}

// Settings lets the owner change the payment policy and the fee rules in
// the CMS (§14), without a deploy: when Midtrans changes its prices, or the
// shop moves its DP threshold. A change applies to orders placed and
// invoices made from then on; an order keeps the DP and the deadlines
// locked at its checkout, and an invoice its fee. Every change needs a
// reason and is audited (§22); saving what is already there changes nothing.
type Settings struct {
	repo       SettingsRepository
	reader     *Reader
	principals Principals
	tx         Transactor
	clock      clock.Clock
}

// NewSettings returns Settings over d.
func NewSettings(d SettingsDeps) *Settings {
	return &Settings{repo: d.Repo, reader: NewReader(d.Repo), principals: d.Principals, tx: d.Tx, clock: d.Clock}
}

func (s *Settings) authorize(ctx context.Context, roles []identity.Role) (identity.Principal, error) {
	p, err := s.principals.Principal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if !slices.ContainsFunc(roles, p.HasRole) {
		return identity.Principal{}, apperr.ErrForbidden
	}
	return p, nil
}

// Get returns the payment settings. Staff only.
func (s *Settings) Get(ctx context.Context) (SettingsView, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return SettingsView{}, err
	}
	return s.view(ctx, p.TenantID)
}

// UpdatePolicy replaces the payment policy. Owner only.
func (s *Settings) UpdatePolicy(ctx context.Context, in Policy, reason string) (SettingsView, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return SettingsView{}, err
	}
	in.UpdatedAt = time.Time{}
	reason = strings.TrimSpace(reason)
	if err := validate(in.Validate(), reason); err != nil {
		return SettingsView{}, err
	}
	now := s.clock.Now()
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSettings(ctx, p.TenantID); err != nil {
			return err
		}
		before, err := s.reader.Policy(ctx, p.TenantID)
		if err != nil {
			return err
		}
		stamp := before.UpdatedAt
		before.UpdatedAt = time.Time{}
		if before == in {
			return nil
		}
		if err := s.repo.SavePolicy(ctx, p.TenantID, in, now); err != nil {
			return err
		}
		return s.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "payments.policy.changed",
			Entity: "payment_policy", EntityID: p.TenantID, Before: policyAudit(before, stamp), After: policyAudit(in, now),
			Reason: reason, At: now,
		})
	})
	if err != nil {
		return SettingsView{}, err
	}
	return s.view(ctx, p.TenantID)
}

// UpdateMethod replaces one method's fee rule. At least one method stays
// on offer. Owner only.
func (s *Settings) UpdateMethod(ctx context.Context, in FeeRule, reason string) (SettingsView, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return SettingsView{}, err
	}
	in.UpdatedAt = time.Time{}
	reason = strings.TrimSpace(reason)
	if err := validate(in.Validate(), reason); err != nil {
		return SettingsView{}, err
	}
	now := s.clock.Now()
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSettings(ctx, p.TenantID); err != nil {
			return err
		}
		rules, err := s.reader.FeeRules(ctx, p.TenantID)
		if err != nil {
			return err
		}
		before := rules[in.Method]
		stamp := before.UpdatedAt
		before.UpdatedAt = time.Time{}
		if before == in {
			return nil
		}
		rules[in.Method] = in
		if !anyEnabled(rules) {
			return ErrLastMethod
		}
		if err := s.repo.SaveFeeRule(ctx, p.TenantID, in, now); err != nil {
			return err
		}
		return s.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "payments.method_fee.changed",
			Entity: "payment_method_fee", EntityID: p.TenantID, Before: ruleAudit(before, stamp), After: ruleAudit(in, now),
			Reason: reason, At: now,
		})
	})
	if err != nil {
		return SettingsView{}, err
	}
	return s.view(ctx, p.TenantID)
}

// ResetMethod returns one method to Midtrans' published price, which is
// on offer. A method already at the default changes nothing. Owner only.
func (s *Settings) ResetMethod(ctx context.Context, m Method, reason string) (SettingsView, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return SettingsView{}, err
	}
	reason = strings.TrimSpace(reason)
	f := apperr.Fields{}
	f.Check(m.valid(), "method", "Metode bayar tidak dikenal.")
	if err := validate(f.Err(), reason); err != nil {
		return SettingsView{}, err
	}
	now := s.clock.Now()
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSettings(ctx, p.TenantID); err != nil {
			return err
		}
		rules, err := s.reader.FeeRules(ctx, p.TenantID)
		if err != nil {
			return err
		}
		had, err := s.repo.DeleteFeeRule(ctx, p.TenantID, m)
		if err != nil || !had {
			return err
		}
		return s.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "payments.method_fee.reset",
			Entity: "payment_method_fee", EntityID: p.TenantID,
			Before: ruleAudit(rules[m], rules[m].UpdatedAt), After: ruleAudit(DefaultFeeRules()[m], time.Time{}),
			Reason: reason, At: now,
		})
	})
	if err != nil {
		return SettingsView{}, err
	}
	return s.view(ctx, p.TenantID)
}

func (s *Settings) view(ctx context.Context, tenant uuid.UUID) (SettingsView, error) {
	policy, err := s.reader.Policy(ctx, tenant)
	if err != nil {
		return SettingsView{}, err
	}
	rules, err := s.reader.FeeRules(ctx, tenant)
	if err != nil {
		return SettingsView{}, err
	}
	defaults := DefaultFeeRules()
	v := SettingsView{Policy: policy}
	for _, m := range Methods {
		ms := MethodSettings{Rule: rules[m], Default: defaults[m]}
		for _, amount := range ExampleAmounts {
			fee, err := rules[m].Fee(amount)
			if err != nil {
				return SettingsView{}, err
			}
			ms.Examples = append(ms.Examples, ExampleFee{AmountIDR: amount, FeeIDR: fee})
		}
		v.Methods = append(v.Methods, ms)
	}
	return v, nil
}

// validate joins the field errors of a change with those of its reason.
func validate(err error, reason string) error {
	f := apperr.Fields{}
	f.Check(reason != "", "reason", "Isi alasan perubahan.")
	f.Check(utf8.RuneCountInString(reason) <= maxReason, "reason", "Alasan paling panjang 500 karakter.")
	return apperr.Join(err, f.Err())
}

func anyEnabled(rules map[Method]FeeRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// policyAudit is a policy as its audit entry shows it; a zero updatedAt
// means the defaults were in use.
func policyAudit(p Policy, updatedAt time.Time) map[string]any {
	return map[string]any{
		"dp_min_percent": p.DPMinPercent, "dp_covers_ingredient_cost": p.DPCoversIngredientCost,
		"balance_due_hours_before": p.BalanceDueHoursBefore, "dp_invoice_valid_minutes": p.DPInvoiceValidMinutes,
		"dp_min_total_idr": p.DPMinTotalIDR, "min_order_idr": p.MinOrderIDR, "default": updatedAt.IsZero(),
	}
}

func ruleAudit(r FeeRule, updatedAt time.Time) map[string]any {
	return map[string]any{
		"method": r.Method, "fixed_idr": r.FixedIDR, "rate_bps": r.RateBPS, "vat_included": r.VATIncluded,
		"enabled": r.Enabled, "default": updatedAt.IsZero(),
	}
}
