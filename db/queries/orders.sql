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
