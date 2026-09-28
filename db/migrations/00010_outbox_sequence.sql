-- +goose Up
-- The outbox publisher (M2.7a, docs/architecture.md §9.1, §21) sends events
-- in the order they were written. created_at comes from platform/clock and
-- ties within a transaction, and ids are random, so neither orders two events
-- of the same instant; a sequence does. Existing rows get numbers too.
alter table outbox add column seq bigint generated always as identity;
alter table outbox add constraint outbox_seq_key unique (seq);

drop index outbox_unpublished_idx;
create index outbox_unpublished_idx on outbox (seq) where published_at is null;

-- +goose Down
drop index outbox_unpublished_idx;
create index outbox_unpublished_idx on outbox (created_at) where published_at is null;

alter table outbox drop column seq;
