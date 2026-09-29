-- +goose Up
-- Batch aggregation (M3.1, docs/architecture.md §9.7, §11): every order
-- produced on one date forms a batch; its components' total units and the
-- ingredients to buy are computed again whenever the batch's orders or the
-- recipes change.

create table production_batches (
    id          uuid primary key default gen_random_uuid(),
    tenant_id   uuid not null references tenants (id),
    batch_date  date not null,                  -- Asia/Jakarta; orders.production_date
    status      text not null default 'open'
                check (status in ('open', 'locked', 'in_production', 'done')),
    -- When the last computation succeeded; null until one has.
    computed_at timestamptz,
    -- Why the last computation failed, such as a recipe that cannot be
    -- evaluated; null once one succeeds. Nothing is guessed meanwhile.
    error       text check (error <> ''),
    created_at  timestamptz not null,
    updated_at  timestamptz not null,
    unique (tenant_id, id),
    unique (tenant_id, batch_date)
);

-- U_c: the total units of each component the batch's orders need (level 1
-- of §11), kept as the audit trail of level 2.
create table batch_component_totals (
    tenant_id    uuid not null references tenants (id),
    batch_id     uuid not null,
    component_id uuid not null,
    units        numeric not null check (units > 0),
    primary key (batch_id, component_id),
    foreign key (tenant_id, batch_id) references production_batches (tenant_id, id) on delete cascade,
    foreign key (tenant_id, component_id) references components (tenant_id, id)
);

-- The shopping list. Quantities are whole units of the ingredient's base
-- unit (g, ml, pcs). What has been ordered (M4) is never overwritten: when
-- the need grows after ordering, the difference shows as more to buy.
create table batch_requirements (
    tenant_id        uuid not null references tenants (id),
    batch_id         uuid not null,
    ingredient_id    uuid not null,
    qty_needed       bigint not null check (qty_needed >= 0),
    qty_usable_stock bigint not null default 0 check (qty_usable_stock >= 0),   -- §12, from M3.2
    qty_ordered      bigint not null default 0 check (qty_ordered >= 0),        -- from M4
    qty_to_buy       bigint not null check (qty_to_buy >= 0),                  -- needed - usable - ordered, at least 0
    -- The default pack when computed; all null when the ingredient has none.
    supplier_id      uuid,
    pack_size        numeric check (pack_size > 0),                             -- in the base unit
    pack_unit        text,
    pack_price_idr   bigint check (pack_price_idr >= 0),
    packs_to_buy     bigint check (packs_to_buy >= 0),
    status           text not null default 'needed' check (status in ('needed', 'ordered', 'received')),
    updated_at       timestamptz not null,
    primary key (batch_id, ingredient_id),
    check ((supplier_id is null) = (pack_size is null) and (pack_size is null) = (packs_to_buy is null)),
    foreign key (tenant_id, batch_id) references production_batches (tenant_id, id) on delete cascade,
    foreign key (tenant_id, ingredient_id) references ingredients (tenant_id, id),
    foreign key (tenant_id, supplier_id) references suppliers (tenant_id, id)
);

create index production_batches_date_idx on production_batches (tenant_id, batch_date, status);

alter table production_batches     enable row level security;
alter table batch_component_totals enable row level security;
alter table batch_requirements     enable row level security;

-- +goose Down
drop table batch_requirements;
drop table batch_component_totals;
drop table production_batches;
