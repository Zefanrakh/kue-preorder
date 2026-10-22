-- Procurement (M4.1). Every statement is scoped by tenant_id.

-- name: InsertOrder :exec
insert into procurement_orders (id, tenant_id, batch_id, batch_date, supplier_id, adapter_key, external_ref, status, note,
                                created_by, created_at, updated_at)
values (sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(batch_id), sqlc.arg(batch_date), sqlc.narg(supplier_id),
        sqlc.arg(adapter_key), sqlc.narg(external_ref), 'ordered', sqlc.narg(note), sqlc.arg(created_by), sqlc.arg(now), sqlc.arg(now));

-- name: InsertItem :exec
insert into procurement_order_items (id, tenant_id, order_id, ingredient_id, qty, packs, pack_size, pack_unit, pack_price_idr)
values (sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(order_id), sqlc.arg(ingredient_id), sqlc.arg(qty), sqlc.narg(packs),
        sqlc.narg(pack_size), sqlc.narg(pack_unit), sqlc.narg(pack_price_idr));

-- name: LockOrder :one
-- A change to an order holds its row until the transaction ends.
select * from procurement_orders
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
for update;

-- name: GetOrder :one
select * from procurement_orders
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id);

-- name: ListOrders :many
select * from procurement_orders
where tenant_id = sqlc.arg(tenant_id) and batch_date = sqlc.arg(batch_date)
order by created_at, id;

-- name: OrderItems :many
select * from procurement_order_items
where tenant_id = sqlc.arg(tenant_id) and order_id = any(sqlc.arg(order_ids)::uuid[])
order by order_id, ingredient_id;

-- name: SetItemReceived :execrows
update procurement_order_items
set status = 'received', qty_received = sqlc.arg(qty_received), received_at = sqlc.arg(received_at)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id) and status = 'ordered';

-- name: SetItemCancelled :execrows
update procurement_order_items set status = 'cancelled'
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id) and status = 'ordered';

-- name: SetOrderStatus :exec
update procurement_orders set status = sqlc.arg(status), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id);
