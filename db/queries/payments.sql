-- Payment policies and the payment ledger (M2.3). Every statement is scoped
-- by tenant_id.

-- name: GetPaymentPolicy :one
select * from payment_policies
where tenant_id = sqlc.arg(tenant_id);

-- name: AddPayment :exec
insert into payments (id, tenant_id, order_id, kind, provider, amount_idr, fee_idr, status,
                      expires_at, created_at, updated_at)
values (sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(order_id), sqlc.arg(kind), sqlc.arg(provider),
        sqlc.arg(amount_idr), sqlc.arg(fee_idr), sqlc.arg(status), sqlc.narg(expires_at), sqlc.arg(now), sqlc.arg(now));

-- name: SetPaymentInvoice :execrows
update payments
set external_id = sqlc.arg(external_id), checkout_url = sqlc.arg(checkout_url), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id) and status = 'pending';

-- name: OrderPayments :many
select * from payments
where tenant_id = sqlc.arg(tenant_id) and order_id = sqlc.arg(order_id)
order by created_at, id;
