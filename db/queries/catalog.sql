-- name: ComponentsOfVariants :many
-- Level 1 of batch aggregation (§11): the components each variant uses, per
-- unit of the variant. Inactive variants are included on purpose: an order
-- placed before a variant was switched off must still be produced. Ordered
-- so recomputes are deterministic.
select variant_id, component_id, units_per_item
from variant_components
where tenant_id = sqlc.arg(tenant_id)
  and variant_id = any(sqlc.arg(variant_ids)::uuid[])
order by variant_id, component_id;

-- name: IngredientsOfComponents :many
-- Level 2 of batch aggregation (§11): each component's recipe lines with
-- what the recipe engine and the rounding rule need.
select ci.component_id,
       ci.ingredient_id,
       ci.model_type,
       ci.params,
       ci.waste_factor,
       ci.version,
       i.base_unit
from component_ingredients ci
join ingredients i on i.tenant_id = ci.tenant_id and i.id = ci.ingredient_id
where ci.tenant_id = sqlc.arg(tenant_id)
  and ci.component_id = any(sqlc.arg(component_ids)::uuid[])
order by ci.component_id, ci.ingredient_id;

-- name: DefaultPacks :many
-- Level 3 of batch aggregation (§11): the default supplier and pack per
-- ingredient. pack_size is in the ingredient's base unit.
select ingredient_id, supplier_id, pack_size, pack_unit, price_idr
from ingredient_suppliers
where tenant_id = sqlc.arg(tenant_id)
  and is_default
  and ingredient_id = any(sqlc.arg(ingredient_ids)::uuid[])
order by ingredient_id;

-- CMS maintenance (M1.4). Every statement is scoped by tenant_id; an update
-- of another tenant's row matches nothing and surfaces as not found.
-- Timestamps come from platform/clock.

-- name: ListProducts :many
select * from products
where tenant_id = sqlc.arg(tenant_id)
order by name, id;

-- name: CreateProduct :one
insert into products (tenant_id, name, slug, description, image_path, is_active, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(name), sqlc.arg(slug), sqlc.arg(description),
        sqlc.narg(image_path), sqlc.arg(is_active), sqlc.arg(now), sqlc.arg(now))
returning *;

-- name: UpdateProduct :one
update products
set name = sqlc.arg(name), slug = sqlc.arg(slug), description = sqlc.arg(description),
    image_path = sqlc.narg(image_path), is_active = sqlc.arg(is_active), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
returning *;

-- name: ListVariants :many
select * from product_variants
where tenant_id = sqlc.arg(tenant_id) and product_id = sqlc.arg(product_id)
order by name, id;

-- name: GetVariant :one
select * from product_variants
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id);

-- name: CreateVariant :one
insert into product_variants (tenant_id, product_id, sku, name, options, price_idr,
                              production_minutes, min_notice_hours, is_active, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(product_id), sqlc.arg(sku), sqlc.arg(name), sqlc.arg(options),
        sqlc.arg(price_idr), sqlc.arg(production_minutes), sqlc.arg(min_notice_hours),
        sqlc.arg(is_active), sqlc.arg(now), sqlc.arg(now))
returning *;

-- name: UpdateVariant :one
-- The price is not here: it changes only through SetVariantPrice, which is
-- audited. A variant never moves to another product.
update product_variants
set sku = sqlc.arg(sku), name = sqlc.arg(name), options = sqlc.arg(options),
    production_minutes = sqlc.arg(production_minutes), min_notice_hours = sqlc.arg(min_notice_hours),
    is_active = sqlc.arg(is_active), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
returning *;

-- name: LockVariantPrice :one
-- Run inside the price-change transaction: the row stays locked until the
-- new price and its audit entry commit together.
select price_idr from product_variants
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
for update;

-- name: SetVariantPrice :one
update product_variants
set price_idr = sqlc.arg(price_idr), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
returning *;

-- name: ListComponents :many
select * from components
where tenant_id = sqlc.arg(tenant_id)
order by name, id;

-- name: CreateComponent :one
insert into components (tenant_id, name, unit_label, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(name), sqlc.arg(unit_label), sqlc.arg(now), sqlc.arg(now))
returning *;

-- name: UpdateComponent :one
update components
set name = sqlc.arg(name), unit_label = sqlc.arg(unit_label), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
returning *;

-- name: ListIngredients :many
select * from ingredients
where tenant_id = sqlc.arg(tenant_id)
order by name, id;

-- name: CreateIngredient :one
insert into ingredients (tenant_id, name, base_unit, is_perishable, shelf_life_days, leftover_policy, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(name), sqlc.arg(base_unit), sqlc.arg(is_perishable),
        sqlc.narg(shelf_life_days), sqlc.arg(leftover_policy), sqlc.arg(now), sqlc.arg(now))
returning *;

-- name: UpdateIngredient :one
update ingredients
set name = sqlc.arg(name), base_unit = sqlc.arg(base_unit), is_perishable = sqlc.arg(is_perishable),
    shelf_life_days = sqlc.narg(shelf_life_days), leftover_policy = sqlc.arg(leftover_policy),
    updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
returning *;

-- name: ListSuppliers :many
select * from suppliers
where tenant_id = sqlc.arg(tenant_id)
order by name, id;

-- name: CreateSupplier :one
insert into suppliers (tenant_id, name, whatsapp_phone, adapter_key, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(name), sqlc.narg(whatsapp_phone), sqlc.arg(adapter_key),
        sqlc.arg(now), sqlc.arg(now))
returning *;

-- name: UpdateSupplier :one
update suppliers
set name = sqlc.arg(name), whatsapp_phone = sqlc.narg(whatsapp_phone),
    adapter_key = sqlc.arg(adapter_key), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
returning *;

-- name: ListPacks :many
-- The default pack first, then by size.
select * from ingredient_suppliers
where tenant_id = sqlc.arg(tenant_id) and ingredient_id = sqlc.arg(ingredient_id)
order by is_default desc, pack_size, id;

-- name: GetPack :one
select * from ingredient_suppliers
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id);

-- name: CreatePack :one
insert into ingredient_suppliers (tenant_id, ingredient_id, supplier_id, supplier_sku, pack_size,
                                  pack_unit, price_idr, is_default, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(ingredient_id), sqlc.arg(supplier_id), sqlc.narg(supplier_sku),
        sqlc.arg(pack_size), sqlc.arg(pack_unit), sqlc.narg(price_idr), false, sqlc.arg(now), sqlc.arg(now))
returning *;

-- name: UpdatePack :one
-- A pack never moves to another ingredient.
update ingredient_suppliers
set supplier_id = sqlc.arg(supplier_id), supplier_sku = sqlc.narg(supplier_sku), pack_size = sqlc.arg(pack_size),
    pack_unit = sqlc.arg(pack_unit), price_idr = sqlc.narg(price_idr), updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
returning *;

-- name: ClearDefaultPack :exec
update ingredient_suppliers
set is_default = false, updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and ingredient_id = sqlc.arg(ingredient_id) and is_default;

-- name: MarkDefaultPack :one
update ingredient_suppliers
set is_default = true, updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
returning *;
