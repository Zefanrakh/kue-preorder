-- +goose Up
-- Audit trail for staff actions that change money, stock, or schedules
-- (docs/architecture.md §22): who, when, what changed, and why.
-- Append-only: a trigger rejects every UPDATE and DELETE, so an entry,
-- once written, cannot be edited or removed by the application.

create table audit_log (
    id         uuid primary key default gen_random_uuid(),
    tenant_id  uuid not null references tenants (id),
    actor_id   uuid not null,                        -- Supabase Auth user id
    action     text not null check (action ~ '^[a-z_]+(\.[a-z_]+)+$'),
    entity     text not null check (entity <> ''),
    entity_id  uuid not null,
    before     jsonb,
    after      jsonb,
    reason     text not null check (btrim(reason) <> ''),
    created_at timestamptz not null
);

create index audit_log_entity_idx on audit_log (tenant_id, entity, entity_id, created_at);

-- +goose StatementBegin
create function audit_log_is_append_only() returns trigger
language plpgsql as $$
begin
    raise exception 'audit_log is append-only: % is not allowed', tg_op
        using errcode = 'restrict_violation';
end;
$$;
-- +goose StatementEnd

create trigger audit_log_append_only
    before update or delete on audit_log
    for each row execute function audit_log_is_append_only();

alter table audit_log enable row level security;

-- +goose Down
drop trigger audit_log_append_only on audit_log;
drop function audit_log_is_append_only();
drop table audit_log;
