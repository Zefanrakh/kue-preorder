-- Stock (M3.2). Every statement is scoped by tenant_id.

-- name: LockIngredientStock :exec
-- Serializes the changes to one ingredient's stock until the transaction
-- ends, so a balance read stays true while it is acted on.
select pg_advisory_xact_lock(hashtextextended('stock:' || sqlc.arg(ingredient_id)::text, 0));

-- name: InsertLot :exec
insert into stock_lots (id, tenant_id, ingredient_id, received_at, expires_at, source, procurement_item_id, note,
                        created_by, created_at, updated_at)
values (sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(ingredient_id), sqlc.arg(received_at), sqlc.narg(expires_at),
        sqlc.arg(source), sqlc.narg(procurement_item_id), sqlc.narg(note), sqlc.arg(created_by), sqlc.arg(now), sqlc.arg(now));

-- name: InsertMovement :exec
insert into stock_movements (tenant_id, lot_id, ingredient_id, kind, qty, batch_id, reason, actor_id, created_at)
values (sqlc.arg(tenant_id), sqlc.arg(lot_id), sqlc.arg(ingredient_id), sqlc.arg(kind), sqlc.arg(qty),
        sqlc.narg(batch_id), sqlc.narg(reason), sqlc.arg(actor_id), sqlc.arg(now));

-- name: InsertCheck :exec
insert into stock_checks (tenant_id, lot_id, result, reason, note, checked_by, checked_at)
values (sqlc.arg(tenant_id), sqlc.arg(lot_id), sqlc.arg(result), sqlc.narg(reason), sqlc.narg(note),
        sqlc.arg(checked_by), sqlc.arg(now));

-- name: SetLotStatus :exec
update stock_lots set status = sqlc.arg(status), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id);

-- name: Lots :many
-- The lots of the ingredients (every ingredient when none is given) in any
-- of statuses, oldest first, each with its balance: the sum of its
-- movements.
select l.id, l.ingredient_id, l.received_at, l.expires_at, l.status, l.source, l.procurement_item_id, l.note, l.created_at,
       coalesce((select sum(m.qty) from stock_movements m where m.tenant_id = l.tenant_id and m.lot_id = l.id), 0)::bigint as balance
from stock_lots l
where l.tenant_id = sqlc.arg(tenant_id)
  and (cardinality(sqlc.arg(ingredient_ids)::uuid[]) = 0 or l.ingredient_id = any(sqlc.arg(ingredient_ids)::uuid[]))
  and l.status = any(sqlc.arg(statuses)::text[])
order by l.ingredient_id, l.received_at, l.id;

-- name: GetLot :one
select l.id, l.ingredient_id, l.received_at, l.expires_at, l.status, l.source, l.procurement_item_id, l.note, l.created_at,
       coalesce((select sum(m.qty) from stock_movements m where m.tenant_id = l.tenant_id and m.lot_id = l.id), 0)::bigint as balance
from stock_lots l
where l.tenant_id = sqlc.arg(tenant_id) and l.id = sqlc.arg(id);

-- name: ExpireLots :execrows
-- Marks the available lots past their expiry at now (§12).
update stock_lots set status = 'expired', updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and status = 'available' and expires_at <= sqlc.arg(now);

-- name: LastOKChecks :many
-- The latest "masih bagus" check of each of the lots that have one.
select lot_id, max(checked_at)::timestamptz as checked_at
from stock_checks
where tenant_id = sqlc.arg(tenant_id) and lot_id = any(sqlc.arg(lot_ids)::uuid[]) and result = 'ok'
group by lot_id;
