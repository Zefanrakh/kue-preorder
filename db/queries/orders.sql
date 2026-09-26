-- Orders (M2.3). Every statement is scoped by tenant_id.

-- name: BatchCutoffs :many
-- The shopping cutoff of each production date from..to: the earliest own
-- cutoff among the orders counting for production (§15). The statuses come
-- from orders.Status.CountsForProduction, so the rule lives in one place.
select production_date, min(shopping_cutoff_at)::timestamptz as cutoff
from orders
where tenant_id = sqlc.arg(tenant_id)
  and production_date between sqlc.arg(from_date) and sqlc.arg(to_date)
  and status = any(sqlc.arg(statuses)::text[])
group by production_date
order by production_date;

-- name: LockCustomerOrders :exec
-- Serializes one customer's checkouts until the transaction ends, so the
-- limit on unpaid orders and the idempotency check cannot race.
select pg_advisory_xact_lock(hashtextextended('orders:' || sqlc.arg(customer_id)::text, 0));

-- name: FindOrderByIdempotencyKey :one
select id from orders
where tenant_id = sqlc.arg(tenant_id) and customer_id = sqlc.arg(customer_id)
  and idempotency_key = sqlc.arg(idempotency_key);

-- name: CountCustomerOrders :one
select count(*)::int from orders
where tenant_id = sqlc.arg(tenant_id) and customer_id = sqlc.arg(customer_id) and status = sqlc.arg(status);

-- name: InsertOrder :one
-- Returns no row when the random code is already taken: the caller draws
-- another, and the transaction goes on.
insert into orders (id, tenant_id, customer_id, channel_id, code, status, payment_status, fulfillment_type,
                    pickup_at, production_start_at, production_date, shopping_cutoff_at, dp_due_at, balance_due_at,
                    subtotal_idr, tax_idr, shipping_idr, total_idr, dp_required_idr, full_payment_required,
                    terms_version, terms_accepted_at, idempotency_key, notes,
                    customer_name, customer_phone, customer_email, created_at, updated_at)
select sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(customer_id), c.id, sqlc.arg(code), sqlc.arg(status),
       sqlc.arg(payment_status), sqlc.arg(fulfillment_type), sqlc.arg(pickup_at), sqlc.arg(production_start_at),
       sqlc.arg(production_date), sqlc.arg(shopping_cutoff_at), sqlc.arg(dp_due_at), sqlc.arg(balance_due_at),
       sqlc.arg(subtotal_idr), sqlc.arg(tax_idr), sqlc.arg(shipping_idr), sqlc.arg(total_idr),
       sqlc.arg(dp_required_idr), sqlc.arg(full_payment_required), sqlc.arg(terms_version), sqlc.arg(now),
       sqlc.arg(idempotency_key), sqlc.arg(notes), sqlc.arg(customer_name), sqlc.arg(customer_phone),
       sqlc.narg(customer_email), sqlc.arg(now), sqlc.arg(now)
from channels c
where c.tenant_id = sqlc.arg(tenant_id) and c.key = sqlc.arg(channel_key)
on conflict (tenant_id, code) do nothing
returning id;

-- name: InsertOrderItem :exec
insert into order_items (tenant_id, order_id, variant_id, product_name, variant_name, quantity,
                         unit_price_idr, production_minutes, min_notice_hours)
values (sqlc.arg(tenant_id), sqlc.arg(order_id), sqlc.arg(variant_id), sqlc.arg(product_name),
        sqlc.arg(variant_name), sqlc.arg(quantity), sqlc.arg(unit_price_idr), sqlc.arg(production_minutes),
        sqlc.arg(min_notice_hours));

-- name: GetOrder :one
select * from orders
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id);

-- name: GetCustomerOrderByCode :one
select * from orders
where tenant_id = sqlc.arg(tenant_id) and customer_id = sqlc.arg(customer_id) and code = sqlc.arg(code);

-- name: OrderItems :many
select * from order_items
where tenant_id = sqlc.arg(tenant_id) and order_id = sqlc.arg(order_id)
order by product_name, variant_name, id;

-- name: ListCustomerOrders :many
-- A customer's orders, newest first, with what the list shows of the items.
select o.id, o.code, o.status, o.payment_status, o.pickup_at, o.total_idr, o.created_at,
       (select count(*) from order_items i where i.order_id = o.id)::int as item_count,
       coalesce((select i.product_name || ' ' || i.variant_name from order_items i
                 where i.order_id = o.id order by i.product_name, i.variant_name, i.id limit 1), '')::text as first_item
from orders o
where o.tenant_id = sqlc.arg(tenant_id) and o.customer_id = sqlc.arg(customer_id)
order by o.created_at desc, o.id
limit sqlc.arg(max_rows);

-- name: ActiveOrderDays :many
-- The production and pickup days (Asia/Jakarta) of the orders in statuses
-- that touch from..to: what keeps a closed day on hold (§15).
select id, production_date, (pickup_at at time zone 'Asia/Jakarta')::date as pickup_date
from orders
where tenant_id = sqlc.arg(tenant_id)
  and status = any(sqlc.arg(statuses)::text[])
  and (production_date between sqlc.arg(from_date) and sqlc.arg(to_date)
       or (pickup_at at time zone 'Asia/Jakarta')::date between sqlc.arg(from_date) and sqlc.arg(to_date));
