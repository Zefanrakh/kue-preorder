-- +goose Up
-- Checkout (M2.4, docs/architecture.md §9.6): a short code a customer can
-- read out on WhatsApp, free notes such as the writing on the cake, the
-- customer's details as they were at checkout (for invoices and the kitchen),
-- and the payment link of each invoice. No order exists before this migration.

-- Six characters without look-alikes (no 0, 1, I, L, O): random, so it
-- says nothing about how many orders the shop gets.
alter table orders
    add column code text not null check (code ~ '^[ABCDEFGHJKMNPQRSTUVWXYZ2-9]{6}$'),
    add column notes text not null default '' check (char_length(notes) <= 500),
    add column customer_name text not null check (customer_name <> ''),
    add column customer_phone text not null check (customer_phone ~ '^\+[1-9][0-9]{6,14}$'),
    add column customer_email text,
    add constraint orders_tenant_id_code_key unique (tenant_id, code);

-- 'dev' is the fake provider of development; production refuses to start with it.
alter table payments
    add column checkout_url text,
    drop constraint payments_provider_check,
    add constraint payments_provider_check check (provider in ('xendit', 'manual', 'dev'));

-- +goose Down
alter table payments
    drop constraint payments_provider_check,
    add constraint payments_provider_check check (provider in ('xendit', 'manual')),
    drop column checkout_url;

alter table orders
    drop constraint orders_tenant_id_code_key,
    drop column customer_email,
    drop column customer_phone,
    drop column customer_name,
    drop column notes,
    drop column code;
