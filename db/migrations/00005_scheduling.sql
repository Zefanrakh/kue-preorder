-- +goose Up
-- Scheduling (docs/architecture.md §9.5, §15): when customers may pick up,
-- how long before production the kitchen shops, and the days the shop is
-- closed. A tenant without a settings row uses scheduling.DefaultSettings;
-- the first change in the CMS inserts the row. The defaults live in Go only.

create table schedule_settings (
    tenant_id              uuid primary key references tenants (id),
    shopping_buffer_hours  int not null check (shopping_buffer_hours between 0 and 168),
    -- Null: no daily limit.
    daily_capacity_minutes int check (daily_capacity_minutes between 1 and 1440),
    -- Wall-clock times in Asia/Jakarta.
    pickup_window_start    time not null,
    pickup_window_end      time not null,
    updated_at             timestamptz not null,
    check (pickup_window_start < pickup_window_end)
);

-- A calendar day in Asia/Jakarta on which nothing is picked up or produced.
create table closed_dates (
    id         uuid primary key default gen_random_uuid(),
    tenant_id  uuid not null references tenants (id),
    date       date not null,
    reason     text not null check (btrim(reason) <> ''),
    created_at timestamptz not null,
    unique (tenant_id, date)
);

alter table schedule_settings enable row level security;
alter table closed_dates      enable row level security;

-- +goose Down
drop table closed_dates;
drop table schedule_settings;
