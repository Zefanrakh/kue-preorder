-- +goose Up
-- Platform and identity tables (docs/architecture.md §9.1, §9.2).
--
-- Row level security is enabled on every table with no policies. Supabase
-- exposes the public schema through its Data API and the anon key ships to
-- browsers, so without RLS anyone could read these tables directly. The Go
-- backend connects as the table owner, which RLS does not restrict.
--
-- Timestamps written by the application come from platform/clock; a
-- default now() exists only for seed rows and manual inserts.

create table tenants (
    id         uuid primary key default gen_random_uuid(),
    name       text not null check (name <> ''),
    created_at timestamptz not null default now()
);

create table staff_roles (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    auth_user_id uuid not null,
    role         text not null check (role in ('owner', 'kitchen')),
    created_at   timestamptz not null default now(),
    unique (tenant_id, auth_user_id, role)
);

create table customers (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    auth_user_id uuid,
    name         text not null check (name <> ''),
    email        text,
    phone        text,
    created_at   timestamptz not null default now()
);

-- A member has one customer row per tenant. Guests have no auth user, so any
-- number of guest rows may exist.
create unique index customers_tenant_auth_user_key
    on customers (tenant_id, auth_user_id)
    where auth_user_id is not null;

-- Global, not per tenant: providers deliver events before we know the tenant.
create table webhook_events (
    id           uuid primary key default gen_random_uuid(),
    provider     text not null,
    event_id     text not null,
    received_at  timestamptz not null,
    processed_at timestamptz,
    unique (provider, event_id)
);

create table outbox (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    aggregate    text not null,
    event_type   text not null,
    payload      jsonb not null,
    created_at   timestamptz not null,
    published_at timestamptz
);

-- The publisher reads unpublished events in the order they were written.
create index outbox_unpublished_idx on outbox (created_at) where published_at is null;

alter table tenants          enable row level security;
alter table staff_roles      enable row level security;
alter table customers        enable row level security;
alter table webhook_events   enable row level security;
alter table outbox           enable row level security;
-- goose creates its version table in public before the first migration runs.
-- "if exists" also lets sqlc parse this file without knowing that table.
alter table if exists goose_db_version enable row level security;

-- +goose Down
drop table outbox;
drop table webhook_events;
drop table customers;
drop table staff_roles;
drop table tenants;
