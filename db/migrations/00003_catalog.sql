-- +goose Up
-- Catalog (docs/architecture.md §9.3): what customers buy (products and
-- variants), what the kitchen makes (components), what it buys (ingredients
-- from suppliers), and the recipe lines between them.
--
-- Parent tables expose unique (tenant_id, id), and children reference them
-- with composite foreign keys, so a recipe line can never join rows of two
-- tenants. Names, slugs, and SKUs are unique per tenant, not globally.
--
-- The contents of component_ingredients.params are checked by the recipe
-- engine (recipe.Build and recipe.Validate) in the catalog service; the
-- database only checks their shape.

create table products (
    id          uuid primary key default gen_random_uuid(),
    tenant_id   uuid not null references tenants (id),
    name        text not null check (name <> ''),
    slug        text not null check (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    description text not null default '',
    image_path  text,
    is_active   boolean not null default true,
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, slug)
);

create table product_variants (
    id                 uuid primary key default gen_random_uuid(),
    tenant_id          uuid not null references tenants (id),
    product_id         uuid not null,
    sku                text not null check (sku <> ''),
    name               text not null check (name <> ''),
    options            jsonb not null default '{}' check (jsonb_typeof(options) = 'object'),
    price_idr          bigint not null check (price_idr >= 0),
    production_minutes int not null check (production_minutes between 1 and 240),
    -- Order to pickup, shopping time included (§15).
    min_notice_hours   int not null check (min_notice_hours >= 0),
    is_active          boolean not null default true,
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, sku),
    foreign key (tenant_id, product_id) references products (tenant_id, id)
);

create index product_variants_product_idx on product_variants (tenant_id, product_id);

-- Half-made goods made in one go and shared by variants: dough, filling, topping.
create table components (
    id         uuid primary key default gen_random_uuid(),
    tenant_id  uuid not null references tenants (id),
    name       text not null check (name <> ''),
    unit_label text not null check (unit_label <> ''), -- "porsi", "loyang", "pcs"
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    unique (tenant_id, id)
);

create unique index components_tenant_name_key on components (tenant_id, lower(name));

-- Linear: one unit of the variant uses units_per_item units of the component.
create table variant_components (
    id             uuid primary key default gen_random_uuid(),
    tenant_id      uuid not null references tenants (id),
    variant_id     uuid not null,
    component_id   uuid not null,
    units_per_item numeric not null check (units_per_item > 0),
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now(),
    unique (variant_id, component_id),
    foreign key (tenant_id, variant_id) references product_variants (tenant_id, id),
    foreign key (tenant_id, component_id) references components (tenant_id, id)
);

create table ingredients (
    id              uuid primary key default gen_random_uuid(),
    tenant_id       uuid not null references tenants (id),
    name            text not null check (name <> ''),
    base_unit       text not null check (base_unit in ('g', 'ml', 'pcs')),
    is_perishable   boolean not null default false,
    shelf_life_days int check (shelf_life_days > 0),
    leftover_policy text not null default 'auto' check (leftover_policy in ('auto', 'confirm', 'never')),
    created_at      timestamptz not null default now(),
    updated_at      timestamptz not null default now(),
    unique (tenant_id, id),
    -- §12, "when in doubt, do not count it": leftovers of a perishable
    -- ingredient are never counted without a check.
    constraint ingredients_perishable_not_auto check (not is_perishable or leftover_policy <> 'auto')
);

create unique index ingredients_tenant_name_key on ingredients (tenant_id, lower(name));

-- Non-linear: the recipe model applied to a batch's total units of the
-- component (§10, §11).
create table component_ingredients (
    id              uuid primary key default gen_random_uuid(),
    tenant_id       uuid not null references tenants (id),
    component_id    uuid not null,
    ingredient_id   uuid not null,
    model_type      text not null check (model_type in ('affine', 'power', 'piecewise', 'formula')),
    params          jsonb not null check (jsonb_typeof(params) = 'object'),
    measured_points jsonb not null default '[]' check (jsonb_typeof(measured_points) = 'array'),
    waste_factor    numeric not null default 1.0 check (waste_factor >= 1),
    version         int not null default 1 check (version >= 1),
    created_at      timestamptz not null default now(),
    updated_at      timestamptz not null default now(),
    unique (component_id, ingredient_id),
    foreign key (tenant_id, component_id) references components (tenant_id, id),
    foreign key (tenant_id, ingredient_id) references ingredients (tenant_id, id)
);

create table suppliers (
    id             uuid primary key default gen_random_uuid(),
    tenant_id      uuid not null references tenants (id),
    name           text not null check (name <> ''),
    whatsapp_phone text check (whatsapp_phone ~ '^\+[1-9][0-9]{6,14}$'), -- E.164
    adapter_key    text not null default 'manual' check (adapter_key in ('manual', 'whatsapp')),
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now(),
    unique (tenant_id, id),
    constraint suppliers_whatsapp_needs_phone check (adapter_key <> 'whatsapp' or whatsapp_phone is not null)
);

create unique index suppliers_tenant_name_key on suppliers (tenant_id, lower(name));

create table ingredient_suppliers (
    id            uuid primary key default gen_random_uuid(),
    tenant_id     uuid not null references tenants (id),
    ingredient_id uuid not null,
    supplier_id   uuid not null,
    supplier_sku  text,
    -- In the ingredient's base unit (1000 for a 1 kg bag of flour in g, 10 for
    -- a tray of 10 eggs), so rounding up to packs never mixes units.
    pack_size     numeric not null check (pack_size > 0),
    pack_unit     text not null check (pack_unit <> ''), -- display label: "sak", "karton"
    price_idr     bigint check (price_idr >= 0),         -- per pack
    is_default    boolean not null default false,
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now(),
    unique (tenant_id, ingredient_id, supplier_id, pack_size),
    foreign key (tenant_id, ingredient_id) references ingredients (tenant_id, id),
    foreign key (tenant_id, supplier_id) references suppliers (tenant_id, id)
);

-- At most one default pack per ingredient: the one the shopping list rounds to.
create unique index ingredient_suppliers_one_default
    on ingredient_suppliers (tenant_id, ingredient_id)
    where is_default;

alter table products              enable row level security;
alter table product_variants      enable row level security;
alter table components            enable row level security;
alter table variant_components    enable row level security;
alter table ingredients           enable row level security;
alter table component_ingredients enable row level security;
alter table suppliers             enable row level security;
alter table ingredient_suppliers  enable row level security;

-- +goose Down
drop table ingredient_suppliers;
drop table suppliers;
drop table component_ingredients;
drop table ingredients;
drop table variant_components;
drop table components;
drop table product_variants;
drop table products;
