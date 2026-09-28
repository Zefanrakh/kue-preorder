-- Payment policies and the payment ledger (M2.3). Every statement is scoped
-- by tenant_id.

-- name: GetPaymentPolicy :one
select * from payment_policies
where tenant_id = sqlc.arg(tenant_id);

-- name: AddPayment :exec
insert into payments (id, tenant_id, order_id, kind, provider, method, amount_idr, fee_idr, status,
                      expires_at, paid_at, raw, created_at, updated_at)
values (sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(order_id), sqlc.arg(kind), sqlc.arg(provider), sqlc.narg(method),
        sqlc.arg(amount_idr), sqlc.arg(fee_idr), sqlc.arg(status), sqlc.narg(expires_at), sqlc.narg(paid_at),
        sqlc.narg(raw), sqlc.arg(now), sqlc.arg(now));

-- name: SetPaymentInvoice :execrows
update payments
set external_id = sqlc.arg(external_id), checkout_url = sqlc.arg(checkout_url), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id) and status = 'pending';

-- name: OrderPayments :many
select * from payments
where tenant_id = sqlc.arg(tenant_id) and order_id = sqlc.arg(order_id)
order by created_at, id;

-- name: ListPaymentMethodFees :many
select * from payment_method_fees
where tenant_id = sqlc.arg(tenant_id)
order by method;

-- name: ExpirePendingPayments :execrows
-- Closes an order's open invoices once they are no longer wanted: paid
-- another way, or the order ended.
update payments
set status = 'expired', updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and order_id = sqlc.arg(order_id) and status = 'pending';

-- name: LockPaymentSettings :exec
-- Serializes a tenant's changes to its payment settings until the
-- transaction ends: the audit sees the true before-state, and two owners
-- cannot switch off the last two methods at once.
select pg_advisory_xact_lock(hashtextextended('payment_settings:' || sqlc.arg(tenant_id)::text, 0));

-- name: UpsertPaymentPolicy :exec
insert into payment_policies (tenant_id, dp_min_percent, dp_covers_ingredient_cost, balance_due_hours_before,
                              dp_invoice_valid_minutes, dp_min_total_idr, min_order_idr, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(dp_min_percent), sqlc.arg(dp_covers_ingredient_cost),
        sqlc.arg(balance_due_hours_before), sqlc.arg(dp_invoice_valid_minutes), sqlc.arg(dp_min_total_idr),
        sqlc.arg(min_order_idr), sqlc.arg(now))
on conflict (tenant_id) do update
set dp_min_percent = excluded.dp_min_percent, dp_covers_ingredient_cost = excluded.dp_covers_ingredient_cost,
    balance_due_hours_before = excluded.balance_due_hours_before,
    dp_invoice_valid_minutes = excluded.dp_invoice_valid_minutes, dp_min_total_idr = excluded.dp_min_total_idr,
    min_order_idr = excluded.min_order_idr, updated_at = excluded.updated_at;

-- name: UpsertPaymentMethodFee :exec
insert into payment_method_fees (tenant_id, method, fixed_idr, rate_bps, vat_included, enabled, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(method), sqlc.arg(fixed_idr), sqlc.arg(rate_bps), sqlc.arg(vat_included),
        sqlc.arg(enabled), sqlc.arg(now))
on conflict (tenant_id, method) do update
set fixed_idr = excluded.fixed_idr, rate_bps = excluded.rate_bps, vat_included = excluded.vat_included,
    enabled = excluded.enabled, updated_at = excluded.updated_at;

-- name: DeletePaymentMethodFee :execrows
delete from payment_method_fees
where tenant_id = sqlc.arg(tenant_id) and method = sqlc.arg(method);
