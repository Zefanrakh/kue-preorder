-- +goose Up
-- Stock (M3.2, docs/architecture.md §9.4, §12): lots of an ingredient, an
-- append-only ledger of their movements, and the kitchen's checks of what
-- is left. Stock is the sum of the movements; no balance column is ever
-- updated. Quantities are whole units of the ingredient's base unit.

create table stock_lots (
    id            uuid primary key default gen_random_uuid(),
    tenant_id     uuid not null references tenants (id),
    ingredient_id uuid not null,
    received_at   timestamptz not null,
    -- received_at plus the ingredient's shelf life, or set by hand; null
    -- when the shelf life is unknown.
    expires_at    timestamptz check (expires_at > received_at),
    -- available: may count as stock (§12); exhausted: nothing left;
    -- discarded: thrown away after a check; expired: past expires_at.
    status        text not null default 'available'
                  check (status in ('available', 'exhausted', 'discarded', 'expired')),
    -- manual: "Belanja masuk" in the PWA; procurement: M4; adjustment: a
    -- stock count found more than there was.
    source        text not null check (source in ('manual', 'procurement', 'adjustment')),
    note          text check (length(note) <= 500),
    created_by    uuid not null,                     -- Supabase Auth user id
    created_at    timestamptz not null,
    updated_at    timestamptz not null,
    unique (tenant_id, id),
    unique (tenant_id, id, ingredient_id),
    foreign key (tenant_id, ingredient_id) references ingredients (tenant_id, id)
);

create index stock_lots_ingredient_idx on stock_lots (tenant_id, ingredient_id, status, received_at);

create table stock_movements (
    id            uuid primary key default gen_random_uuid(),
    tenant_id     uuid not null references tenants (id),
    lot_id        uuid not null,
    ingredient_id uuid not null,
    kind          text not null check (kind in ('receive', 'consume', 'waste', 'adjust')),
    qty           bigint not null check (qty <> 0),   -- in the base unit; positive in, negative out
    batch_id      uuid,                               -- consume: the batch it went into (M3.3)
    reason        text check (btrim(reason) <> '' and length(reason) <= 500),
    actor_id      uuid not null,                      -- Supabase Auth user id
    created_at    timestamptz not null,
    check (kind <> 'receive' or qty > 0),
    check (kind not in ('consume', 'waste') or qty < 0),
    check (kind not in ('waste', 'adjust') or reason is not null),
    -- The movement's ingredient is its lot's.
    foreign key (tenant_id, lot_id, ingredient_id) references stock_lots (tenant_id, id, ingredient_id),
    foreign key (tenant_id, batch_id) references production_batches (tenant_id, id)
);

create index stock_movements_lot_idx on stock_movements (tenant_id, lot_id);

-- +goose StatementBegin
create function stock_movements_is_append_only() returns trigger
language plpgsql as $$
begin
    raise exception 'stock_movements is append-only: % is not allowed', tg_op
        using errcode = 'restrict_violation';
end;
$$;
-- +goose StatementEnd

create trigger stock_movements_append_only
    before update or delete on stock_movements
    for each row execute function stock_movements_is_append_only();

-- The kitchen's look at what is left of a lot (§12): "masih bagus" or
-- "buang". A perishable lot counts only with an ok check in the last 24 hours.
create table stock_checks (
    id         uuid primary key default gen_random_uuid(),
    tenant_id  uuid not null references tenants (id),
    lot_id     uuid not null,
    result     text not null check (result in ('ok', 'discard')),
    reason     text check (reason in ('smell', 'mold', 'expired', 'other')),   -- discard only
    note       text check (length(note) <= 500),
    checked_by uuid not null,                         -- Supabase Auth user id
    checked_at timestamptz not null,
    check ((result = 'discard') = (reason is not null)),
    check (reason is distinct from 'other' or note is not null),
    foreign key (tenant_id, lot_id) references stock_lots (tenant_id, id)
);

create index stock_checks_lot_idx on stock_checks (tenant_id, lot_id, checked_at);

alter table stock_lots      enable row level security;
alter table stock_movements enable row level security;
alter table stock_checks    enable row level security;

-- +goose Down
drop table stock_checks;
drop trigger stock_movements_append_only on stock_movements;
drop function stock_movements_is_append_only();
drop table stock_movements;
drop table stock_lots;
