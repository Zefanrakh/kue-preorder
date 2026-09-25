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

-- The storefront (M1.7): what is on sale. One row per active variant of an
-- active product, so a product without an active variant has no row and is
-- not on sale. Product and variants come from one statement, one snapshot.

-- name: ShopCatalog :many
select p.id as product_id, p.name as product_name, p.slug, p.description, p.image_path,
       v.id as variant_id, v.name as variant_name, v.options, v.price_idr, v.min_notice_hours
from products p
join product_variants v on v.tenant_id = p.tenant_id and v.product_id = p.id
where p.tenant_id = sqlc.arg(tenant_id) and p.is_active and v.is_active
order by p.name, p.id, v.price_idr, v.name, v.id;

-- name: ShopProduct :many
select p.id as product_id, p.name as product_name, p.slug, p.description, p.image_path,
       v.id as variant_id, v.name as variant_name, v.options, v.price_idr, v.min_notice_hours
from products p
join product_variants v on v.tenant_id = p.tenant_id and v.product_id = p.id
where p.tenant_id = sqlc.arg(tenant_id) and p.slug = sqlc.arg(slug) and p.is_active and v.is_active
order by v.price_idr, v.name, v.id;

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

-- Recipes (M1.5). Writers lock the parent row (variant or component) first,
-- so two edits of one recipe run one after the other.

-- name: LockVariant :one
select id from product_variants
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
for update;

-- name: ListVariantComponents :many
select component_id, units_per_item from variant_components
where tenant_id = sqlc.arg(tenant_id) and variant_id = sqlc.arg(variant_id)
order by component_id;

-- name: DeleteVariantComponentsExcept :exec
delete from variant_components
where tenant_id = sqlc.arg(tenant_id) and variant_id = sqlc.arg(variant_id)
  and not (component_id = any(sqlc.arg(keep)::uuid[]));

-- name: UpsertVariantComponent :exec
insert into variant_components (tenant_id, variant_id, component_id, units_per_item, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(variant_id), sqlc.arg(component_id), sqlc.arg(units_per_item), sqlc.arg(now), sqlc.arg(now))
on conflict (variant_id, component_id) do update
set units_per_item = excluded.units_per_item, updated_at = excluded.updated_at;

-- name: LockComponent :one
select id from components
where tenant_id = sqlc.arg(tenant_id) and id = sqlc.arg(id)
for update;

-- name: ListRecipeLines :many
select * from component_ingredients
where tenant_id = sqlc.arg(tenant_id) and component_id = sqlc.arg(component_id)
order by ingredient_id;

-- name: GetRecipeLine :one
select * from component_ingredients
where tenant_id = sqlc.arg(tenant_id) and component_id = sqlc.arg(component_id)
  and ingredient_id = sqlc.arg(ingredient_id);

-- name: RecipeLineDiffers :one
-- Compares as jsonb and numeric, so formatting differences are not changes.
select (model_type, params, measured_points, waste_factor)
       is distinct from (sqlc.arg(model_type)::text, sqlc.arg(params)::jsonb,
                         sqlc.arg(measured_points)::jsonb, sqlc.arg(waste_factor)::numeric)
from component_ingredients
where tenant_id = sqlc.arg(tenant_id) and component_id = sqlc.arg(component_id)
  and ingredient_id = sqlc.arg(ingredient_id);

-- name: InsertRecipeLine :one
insert into component_ingredients (tenant_id, component_id, ingredient_id, model_type, params,
                                   measured_points, waste_factor, version, created_at, updated_at)
values (sqlc.arg(tenant_id), sqlc.arg(component_id), sqlc.arg(ingredient_id), sqlc.arg(model_type),
        sqlc.arg(params), sqlc.arg(measured_points), sqlc.arg(waste_factor), 1, sqlc.arg(now), sqlc.arg(now))
returning *;

-- name: UpdateRecipeLine :one
update component_ingredients
set model_type = sqlc.arg(model_type), params = sqlc.arg(params), measured_points = sqlc.arg(measured_points),
    waste_factor = sqlc.arg(waste_factor), version = version + 1, updated_at = sqlc.arg(now)
where tenant_id = sqlc.arg(tenant_id) and component_id = sqlc.arg(component_id)
  and ingredient_id = sqlc.arg(ingredient_id)
returning *;

-- name: DeleteRecipeLine :execrows
delete from component_ingredients
where tenant_id = sqlc.arg(tenant_id) and component_id = sqlc.arg(component_id)
  and ingredient_id = sqlc.arg(ingredient_id);

-- name: VariantsByIDs :many
-- For checkout (M2): what an order needs to price and schedule a variant.
select id, product_id, name, price_idr, production_minutes, min_notice_hours, is_active
from product_variants
where tenant_id = sqlc.arg(tenant_id) and id = any(sqlc.arg(ids)::uuid[])
order by id;
