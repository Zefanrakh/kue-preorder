-- +goose Up
-- Single-tenant mode (docs/architecture.md §25): one default tenant with a
-- fixed id. Its name can be changed later from the CMS.
insert into tenants (id, name)
values ('00000000-0000-0000-0000-000000000001', 'Toko Kue');

-- +goose Down
delete from tenants where id = '00000000-0000-0000-0000-000000000001';
