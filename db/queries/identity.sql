-- name: ListTenants :many
-- Single-tenant mode resolves its tenant by listing at most a few rows. This is
-- the only identity query not scoped by tenant_id.
select id, name, created_at
from tenants
order by created_at, id
limit sqlc.arg(max_rows);

-- name: ListStaffRoles :many
select role
from staff_roles
where tenant_id = sqlc.arg(tenant_id)
  and auth_user_id = sqlc.arg(auth_user_id)
order by role;

-- name: GrantStaffRole :exec
insert into staff_roles (tenant_id, auth_user_id, role, created_at)
values (sqlc.arg(tenant_id), sqlc.arg(auth_user_id), sqlc.arg(role), sqlc.arg(created_at))
on conflict (tenant_id, auth_user_id, role) do nothing;

-- name: GetCustomerByAuthUser :one
select id, tenant_id, auth_user_id, name, email, phone, created_at
from customers
where tenant_id = sqlc.arg(tenant_id)
  and auth_user_id = sqlc.arg(auth_user_id)::uuid;

-- name: CreateCustomer :one
insert into customers (tenant_id, auth_user_id, name, email, phone, created_at)
values (
    sqlc.arg(tenant_id),
    sqlc.narg(auth_user_id),
    sqlc.arg(name),
    sqlc.narg(email),
    sqlc.narg(phone),
    sqlc.arg(created_at)
)
returning id, tenant_id, auth_user_id, name, email, phone, created_at;

-- name: UpsertCustomer :one
-- A signed-in customer has one record per tenant. The phone comes from the
-- verified token; the name and email from the latest checkout.
insert into customers (tenant_id, auth_user_id, name, email, phone, created_at)
values (sqlc.arg(tenant_id), sqlc.arg(auth_user_id), sqlc.arg(name), sqlc.narg(email), sqlc.arg(phone), sqlc.arg(created_at))
on conflict (tenant_id, auth_user_id) where auth_user_id is not null
do update set name = excluded.name, email = excluded.email, phone = excluded.phone
returning id, name, email, phone;
