-- Batch aggregation (M3.1). Every statement is scoped by tenant_id.

-- name: EnsureBatch :exec
insert into production_batches (tenant_id, batch_date, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(batch_date), sqlc.arg(now), sqlc.arg(now))
on conflict (tenant_id, batch_date) do nothing;

-- name: LockBatch :one
-- A computation holds its batch's row until the transaction ends, so two
-- computations of one batch take turns and the last one wins whole.
select * from production_batches
where tenant_id = sqlc.arg(tenant_id) and batch_date = sqlc.arg(batch_date)
for update;

-- name: GetBatch :one
select * from production_batches
where tenant_id = sqlc.arg(tenant_id) and batch_date = sqlc.arg(batch_date);

-- name: ListBatches :many
select b.id, b.batch_date, b.status, b.computed_at, b.error,
       (select count(*) from batch_requirements r where r.batch_id = b.id and r.qty_to_buy > 0)::int as items_to_buy,
       (select coalesce(sum(r.packs_to_buy * r.pack_price_idr), 0) from batch_requirements r where r.batch_id = b.id)::bigint as cost_idr
from production_batches b
where b.tenant_id = sqlc.arg(tenant_id) and b.batch_date between sqlc.arg(from_date) and sqlc.arg(to_date)
order by b.batch_date;

-- name: UpcomingBatchDates :many
-- The batches still to be made from a date on: what a recipe change affects.
select batch_date from production_batches
where tenant_id = sqlc.arg(tenant_id) and batch_date >= sqlc.arg(from_date) and status <> 'done'
order by batch_date;

-- name: MarkBatchComputed :exec
update production_batches
set computed_at = sqlc.arg(now), error = null, updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id);

-- name: MarkBatchFailed :exec
-- Runs after the failed computation rolled back, so the batch may not exist yet.
insert into production_batches (tenant_id, batch_date, error, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(batch_date), sqlc.arg(error), sqlc.arg(now), sqlc.arg(now))
on conflict (tenant_id, batch_date) do update set error = excluded.error, updated_at = excluded.updated_at;

-- name: DeleteComponentTotals :exec
delete from batch_component_totals
where tenant_id = sqlc.arg(tenant_id) and batch_id = sqlc.arg(batch_id);

-- name: InsertComponentTotal :exec
insert into batch_component_totals (tenant_id, batch_id, component_id, units)
values (sqlc.arg(tenant_id), sqlc.arg(batch_id), sqlc.arg(component_id), sqlc.arg(units));

-- name: BatchComponentTotals :many
select * from batch_component_totals
where tenant_id = sqlc.arg(tenant_id) and batch_id = sqlc.arg(batch_id)
order by component_id;

-- name: BatchRequirements :many
select * from batch_requirements
where tenant_id = sqlc.arg(tenant_id) and batch_id = sqlc.arg(batch_id)
order by ingredient_id;

-- name: UpsertRequirement :exec
-- A computation sets what is needed and what to buy; what was ordered and
-- the row's status belong to procurement (M4) and are left as they are.
insert into batch_requirements (tenant_id, batch_id, ingredient_id, qty_needed, qty_usable_stock, qty_to_buy,
                                supplier_id, pack_size, pack_unit, pack_price_idr, packs_to_buy, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(batch_id), sqlc.arg(ingredient_id), sqlc.arg(qty_needed), sqlc.arg(qty_usable_stock),
        sqlc.arg(qty_to_buy), sqlc.narg(supplier_id), sqlc.narg(pack_size), sqlc.narg(pack_unit), sqlc.narg(pack_price_idr),
        sqlc.narg(packs_to_buy), sqlc.arg(now))
on conflict (batch_id, ingredient_id) do update
set qty_needed = excluded.qty_needed, qty_usable_stock = excluded.qty_usable_stock, qty_to_buy = excluded.qty_to_buy,
    supplier_id = excluded.supplier_id, pack_size = excluded.pack_size, pack_unit = excluded.pack_unit,
    pack_price_idr = excluded.pack_price_idr, packs_to_buy = excluded.packs_to_buy, updated_at = excluded.updated_at;

-- name: DeleteStaleRequirements :exec
-- Drops the ingredients no longer needed that nobody has ordered yet.
delete from batch_requirements
where tenant_id = sqlc.arg(tenant_id) and batch_id = sqlc.arg(batch_id) and status = 'needed'
  and not (ingredient_id = any(sqlc.arg(keep)::uuid[]));
