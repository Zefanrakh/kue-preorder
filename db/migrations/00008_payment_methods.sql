-- +goose Up
-- Payment methods and fees (M2.6a, docs/architecture.md §14, §27.2): the
-- customer picks a method at checkout and pays its provider fee on top as
-- "Biaya admin" (never for QRIS); orders below a threshold are paid in full.

-- Keep these defaults equal to payments.DefaultPolicy.
alter table payment_policies
    add column dp_min_total_idr bigint not null default 150000 check (dp_min_total_idr >= 0),
    add column min_order_idr bigint not null default 0 check (min_order_idr >= 0);

-- A tenant's own prices per method; a method without a row uses
-- payments.DefaultFeeRules.
create table payment_method_fees (
    tenant_id    uuid not null references tenants (id),
    method       text not null check (method in ('qris', 'bank_transfer', 'ewallet', 'minimarket')),
    fixed_idr    bigint not null check (fixed_idr between 0 and 1000000),
    rate_bps     int not null check (rate_bps between 0 and 5000),
    vat_included boolean not null,
    enabled      boolean not null,
    updated_at   timestamptz not null,
    primary key (tenant_id, method)
);

alter table payment_method_fees enable row level security;

-- The method an invoice offers; empty for manual payments and refunds.
-- QRIS never carries a fee: Bank Indonesia forbids passing it on.
alter table payments
    add column method text check (method in ('qris', 'bank_transfer', 'ewallet', 'minimarket')),
    add constraint payments_qris_without_fee check (method is distinct from 'qris' or fee_idr = 0);

-- +goose Down
alter table payments
    drop constraint payments_qris_without_fee,
    drop column method;

drop table payment_method_fees;

alter table payment_policies
    drop column min_order_idr,
    drop column dp_min_total_idr;
