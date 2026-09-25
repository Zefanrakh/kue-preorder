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
