-- +goose Up
-- Procurement (M4.1, docs/architecture.md §9.7, §19): purchase orders made
-- from a batch's shopping list, one per supplier, and received into stock.
-- M4.1 has the manual adapter only: nothing is sent; the kitchen orders or
-- shops itself and ticks what arrived.

create table procurement_orders (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    batch_id     uuid not null,
    batch_date   date not null,                     -- the batch's, for reading without aggregation
    supplier_id  uuid,                              -- null: "Belanja sendiri", ingredients without a supplier
    adapter_key  text not null check (adapter_key in ('manual', 'whatsapp')),
    external_ref text,                              -- the adapter's id, such as a WhatsApp message (M4.2)
    status       text not null default 'ordered' check (status in ('ordered', 'received', 'cancelled')),
    note         text check (length(note) <= 500),
    created_by   uuid not null,                     -- Supabase Auth user id
    sent_at      timestamptz,                       -- M4.2
    created_at   timestamptz not null,
    updated_at   timestamptz not null,
    unique (tenant_id, id),
    foreign key (tenant_id, batch_id) references production_batches (tenant_id, id),
    foreign key (tenant_id, supplier_id) references suppliers (tenant_id, id)
);

create index procurement_orders_batch_idx on procurement_orders (tenant_id, batch_date, created_at);

create table procurement_order_items (
    id             uuid primary key default gen_random_uuid(),
    tenant_id      uuid not null references tenants (id),
    order_id       uuid not null,
    ingredient_id  uuid not null,
    qty            bigint not null check (qty > 0),           -- ordered, in the base unit
    -- The pack it is bought in; all null for an ingredient without one.
    packs          bigint check (packs > 0),
    pack_size      numeric check (pack_size > 0),
    pack_unit      text,
    pack_price_idr bigint check (pack_price_idr >= 0),
    status         text not null default 'ordered' check (status in ('ordered', 'received', 'cancelled')),
    qty_received   bigint check (qty_received >= 0),          -- what arrived, less or more than ordered
    received_at    timestamptz,
    unique (tenant_id, id),
    unique (order_id, ingredient_id),
    check ((packs is null) = (pack_size is null)),
    check ((status = 'received') = (qty_received is not null and received_at is not null)),
    foreign key (tenant_id, order_id) references procurement_orders (tenant_id, id),
    foreign key (tenant_id, ingredient_id) references ingredients (tenant_id, id)
);

-- qty_ordered is what was ordered and has not arrived yet; what arrived
-- moves into the stock, and qty_received keeps track of it for the list.
alter table batch_requirements
    add column qty_received bigint not null default 0 check (qty_received >= 0);

-- A lot received from an order links to its item.
alter table stock_lots
    add column procurement_item_id uuid,
    add constraint stock_lots_procurement_item_fkey
        foreign key (tenant_id, procurement_item_id) references procurement_order_items (tenant_id, id),
    add constraint stock_lots_procurement_source check ((source = 'procurement') = (procurement_item_id is not null));

alter table procurement_orders      enable row level security;
alter table procurement_order_items enable row level security;

-- +goose Down
alter table stock_lots
    drop constraint stock_lots_procurement_source,
    drop constraint stock_lots_procurement_item_fkey,
    drop column procurement_item_id;
alter table batch_requirements drop column qty_received;
drop table procurement_order_items;
drop table procurement_orders;
