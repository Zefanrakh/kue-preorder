-- +goose Up
-- Retries of the PWA's offline queue (M5.1, docs/screens.md "Celah API" 6):
-- ReceiveStock and CountStock carry an idempotency key, claimed here in the
-- same transaction as their effect. A retry with the same key finds the
-- claim and has no second effect; the primary key makes two at once one.

create table inventory_requests (
    tenant_id       uuid not null references tenants (id),
    rpc             text not null check (rpc in ('receive', 'count')),
    idempotency_key uuid not null,
    -- The request as first sent, canonical JSON; a retry must match it.
    request         text not null,
    -- receive: the new lot; count: the ingredient counted.
    result_id       uuid not null,
    created_by      uuid not null,                   -- Supabase Auth user id
    created_at      timestamptz not null,
    primary key (tenant_id, rpc, idempotency_key)
);

alter table inventory_requests enable row level security;

-- +goose Down
drop table inventory_requests;
