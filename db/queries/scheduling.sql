-- Scheduling settings and closed dates (M2.2). Every statement is scoped by
-- tenant_id. Timestamps come from platform/clock.

-- name: GetScheduleSettings :one
select * from schedule_settings
where tenant_id = sqlc.arg(tenant_id);

-- name: UpsertScheduleSettings :one
insert into schedule_settings (tenant_id, shopping_buffer_hours, daily_capacity_minutes,
                               pickup_window_start, pickup_window_end, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(shopping_buffer_hours), sqlc.narg(daily_capacity_minutes),
        sqlc.arg(pickup_window_start), sqlc.arg(pickup_window_end), sqlc.arg(now))
on conflict (tenant_id) do update
set shopping_buffer_hours  = excluded.shopping_buffer_hours,
    daily_capacity_minutes = excluded.daily_capacity_minutes,
    pickup_window_start    = excluded.pickup_window_start,
    pickup_window_end      = excluded.pickup_window_end,
    updated_at             = excluded.updated_at
returning *;

-- name: ListClosedDates :many
select * from closed_dates
where tenant_id = sqlc.arg(tenant_id)
  and date between sqlc.arg(from_date) and sqlc.arg(to_date)
order by date;

-- name: AddClosedDate :one
insert into closed_dates (tenant_id, date, reason, created_at)
values (sqlc.arg(tenant_id), sqlc.arg(date), sqlc.arg(reason), sqlc.arg(now))
returning *;

-- name: RemoveClosedDate :execrows
delete from closed_dates
where tenant_id = sqlc.arg(tenant_id) and date = sqlc.arg(date);
