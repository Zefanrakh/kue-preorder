-- +goose Up
-- Orders and payments (docs/architecture.md §9.6, §9.8, §13, §14). An order
-- locks its prices, schedule, and payment terms at checkout; nothing here is
-- recomputed later. Money is bigint rupiah. Payments are a ledger: what is
-- paid is the sum of paid rows, never a column that gets updated.

-- Where orders come from (§20). The web shop is the only channel for now.
create table channels (
    id         uuid primary key default gen_random_uuid(),
    tenant_id  uuid not null references tenants (id),
    key        text not null check (key ~ '^[a-z][a-z0-9_]*$'),
    name       text not null check (name <> ''),
    is_active  boolean not null default true,
    created_at timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, key)
);

insert into channels (tenant_id, key, name)
values ('00000000-0000-0000-0000-000000000001', 'web', 'Website');

-- A tenant without a row uses payments.DefaultPolicy; the defaults live in Go.
create table payment_policies (
    tenant_id                 uuid primary key references tenants (id),
    dp_min_percent            int not null check (dp_min_percent between 1 and 100),
    dp_covers_ingredient_cost boolean not null,
    balance_due_hours_before  int not null check (balance_due_hours_before between 0 and 168),
    dp_invoice_valid_minutes  int not null check (dp_invoice_valid_minutes between 30 and 10080),
    updated_at                timestamptz not null
);

-- Orders reference their customer within the tenant.
alter table customers add constraint customers_tenant_id_id_key unique (tenant_id, id);

create table orders (
    id                    uuid primary key default gen_random_uuid(),
    tenant_id             uuid not null references tenants (id),
    customer_id           uuid not null,
    channel_id            uuid not null,
    external_order_ref    text,
    status                text not null check (status in ('awaiting_dp', 'confirmed', 'expired', 'in_production',
                                                          'ready', 'out_for_delivery', 'completed', 'cancelled')),
    payment_status        text not null check (payment_status in ('unpaid', 'dp_paid', 'paid_in_full',
                                                                  'forfeited', 'refunded')),
    fulfillment_type      text not null check (fulfillment_type in ('pickup', 'delivery')),
    -- The schedule locked at checkout (§15, scheduling.Plan).
    pickup_at             timestamptz not null,
    production_start_at   timestamptz not null,
    production_date       date not null,             -- Asia/Jakarta; the batch key
    shopping_cutoff_at    timestamptz not null,      -- this order's own cutoff
    dp_due_at             timestamptz not null,
    balance_due_at        timestamptz not null,
    -- Money locked at checkout (§14). Tax stays 0 until the tax policy is set (§27).
    subtotal_idr          bigint not null check (subtotal_idr >= 0),
    tax_idr               bigint not null default 0 check (tax_idr >= 0),
    shipping_idr          bigint not null default 0 check (shipping_idr >= 0),
    total_idr             bigint not null check (total_idr = subtotal_idr + tax_idr + shipping_idr),
    dp_required_idr       bigint not null check (dp_required_idr between 1 and total_idr),
    full_payment_required boolean not null,
    terms_version         text not null check (terms_version <> ''),
    terms_accepted_at     timestamptz not null,
    -- A repeated checkout with the same key returns the first order.
    idempotency_key       uuid not null,
    created_at            timestamptz not null,
    updated_at            timestamptz not null,
    unique (tenant_id, id),
    unique (tenant_id, customer_id, idempotency_key),
    unique (channel_id, external_order_ref),
    check (production_start_at <= pickup_at and shopping_cutoff_at <= production_start_at
           and balance_due_at <= production_start_at),
    foreign key (tenant_id, customer_id) references customers (tenant_id, id),
    foreign key (tenant_id, channel_id) references channels (tenant_id, id)
);

create index orders_production_date_idx on orders (tenant_id, production_date, status);
create index orders_customer_idx on orders (tenant_id, customer_id, created_at);

-- Names, prices, and times are copied at checkout: editing the catalog never
-- changes an order already placed.
create table order_items (
    id                 uuid primary key default gen_random_uuid(),
    tenant_id          uuid not null references tenants (id),
    order_id           uuid not null,
    variant_id         uuid not null,
    product_name       text not null check (product_name <> ''),
    variant_name       text not null check (variant_name <> ''),
    quantity           int not null check (quantity between 1 and 1000),
    unit_price_idr     bigint not null check (unit_price_idr >= 0),
    production_minutes int not null check (production_minutes between 1 and 240),
    min_notice_hours   int not null check (min_notice_hours >= 0),
    unique (order_id, variant_id),
    foreign key (tenant_id, order_id) references orders (tenant_id, id),
    foreign key (tenant_id, variant_id) references product_variants (tenant_id, id)
);

-- The payment ledger (§9.8). amount_idr is what counts towards the order
-- (negative for a refund); fee_idr is the "biaya admin" the customer pays on
-- top, which never counts as paid towards the order.
create table payments (
    id          uuid primary key default gen_random_uuid(),
    tenant_id   uuid not null references tenants (id),
    order_id    uuid not null,
    kind        text not null check (kind in ('dp', 'balance', 'full', 'refund')),
    provider    text not null check (provider in ('xendit', 'manual')),
    external_id text,
    amount_idr  bigint not null check (amount_idr <> 0),
    fee_idr     bigint not null default 0 check (fee_idr >= 0),
    status      text not null check (status in ('pending', 'paid', 'expired', 'failed')),
    expires_at  timestamptz,
    paid_at     timestamptz,
    raw         jsonb,
    created_at  timestamptz not null,
    updated_at  timestamptz not null,
    unique (provider, external_id),
    check ((kind = 'refund') = (amount_idr < 0)),
    check ((status = 'paid') = (paid_at is not null)),
    foreign key (tenant_id, order_id) references orders (tenant_id, id)
);

create index payments_order_idx on payments (tenant_id, order_id);

alter table channels         enable row level security;
alter table payment_policies enable row level security;
alter table orders           enable row level security;
alter table order_items      enable row level security;
alter table payments         enable row level security;

-- +goose Down
drop table payments;
drop table order_items;
drop table orders;
alter table customers drop constraint customers_tenant_id_id_key;
drop table payment_policies;
drop table channels;
