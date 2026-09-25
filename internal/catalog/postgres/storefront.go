package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
)

// ShopCatalog implements catalog.Repository.
func (r *Repository) ShopCatalog(ctx context.Context, tenantID uuid.UUID) ([]catalog.ShopProduct, error) {
	rows, err := r.q.ShopCatalog(ctx, tenantID)
	if err != nil {
		return nil, mapErr(err)
	}
	return groupShopRows(rows)
}

// ShopProduct implements catalog.Repository.
func (r *Repository) ShopProduct(ctx context.Context, tenantID uuid.UUID, slug string) (catalog.ShopProduct, error) {
	rows, err := r.q.ShopProduct(ctx, ShopProductParams{TenantID: tenantID, Slug: slug})
	if err != nil {
		return catalog.ShopProduct{}, mapErr(err)
	}
	products, err := groupShopRows(mapSlice(rows, func(r ShopProductRow) ShopCatalogRow { return ShopCatalogRow(r) }))
	if err != nil {
		return catalog.ShopProduct{}, err
	}
	if len(products) == 0 {
		return catalog.ShopProduct{}, catalog.ErrNotFound
	}
	return products[0], nil
}

// groupShopRows folds one row per variant into products, keeping the order
// of the query: rows of one product are adjacent.
func groupShopRows(rows []ShopCatalogRow) ([]catalog.ShopProduct, error) {
	products := []catalog.ShopProduct{}
	for _, row := range rows {
		options := map[string]string{}
		if err := json.Unmarshal(row.Options, &options); err != nil {
			return nil, fmt.Errorf("decode options of variant %s: %w", row.VariantID, err)
		}
		if n := len(products); n == 0 || products[n-1].ID != row.ProductID {
			products = append(products, catalog.ShopProduct{
				ID: row.ProductID, Name: row.ProductName, Slug: row.Slug,
				Description: row.Description, ImagePath: deref(row.ImagePath),
			})
		}
		p := &products[len(products)-1]
		p.Variants = append(p.Variants, catalog.ShopVariant{
			ID: row.VariantID, Name: row.VariantName, Options: options,
			PriceIDR: row.PriceIdr, MinNoticeHours: row.MinNoticeHours,
		})
	}
	return products, nil
}
