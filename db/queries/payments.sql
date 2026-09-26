-- Payment policies and the payment ledger (M2.3). Every statement is scoped
-- by tenant_id.

-- name: GetPaymentPolicy :one
select * from payment_policies
where tenant_id = sqlc.arg(tenant_id);
