package connect

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1/catalogv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
)

// StorefrontHandler serves kuepreorder.catalog.v1.StorefrontService.
type StorefrontHandler struct {
	shop   *catalog.Storefront
	logger *slog.Logger
}

var _ catalogv1connect.StorefrontServiceHandler = (*StorefrontHandler)(nil)

// NewStorefrontHandler returns a handler backed by shop.
func NewStorefrontHandler(shop *catalog.Storefront, logger *slog.Logger) *StorefrontHandler {
	return &StorefrontHandler{shop: shop, logger: logger}
}

// ListShopProducts implements catalogv1connect.StorefrontServiceHandler.
func (h *StorefrontHandler) ListShopProducts(ctx context.Context, _ *connect.Request[catalogv1.ListShopProductsRequest]) (*connect.Response[catalogv1.ListShopProductsResponse], error) {
	products, err := h.shop.Products(ctx)
	if err != nil {
		return nil, connectError(ctx, h.logger, err)
	}
	return connect.NewResponse(&catalogv1.ListShopProductsResponse{Products: toProtos(products, shopProductToProto)}), nil
}

// GetShopProduct implements catalogv1connect.StorefrontServiceHandler.
func (h *StorefrontHandler) GetShopProduct(ctx context.Context, req *connect.Request[catalogv1.GetShopProductRequest]) (*connect.Response[catalogv1.GetShopProductResponse], error) {
	product, err := h.shop.Product(ctx, req.Msg.GetSlug())
	if err != nil {
		return nil, connectError(ctx, h.logger, err)
	}
	return connect.NewResponse(&catalogv1.GetShopProductResponse{Product: shopProductToProto(product)}), nil
}

func shopProductToProto(p catalog.ShopProduct) *catalogv1.ShopProduct {
	return &catalogv1.ShopProduct{
		Id: p.ID.String(), Name: p.Name, Slug: p.Slug, Description: p.Description, ImagePath: p.ImagePath,
		Variants: toProtos(p.Variants, func(v catalog.ShopVariant) *catalogv1.ShopVariant {
			return &catalogv1.ShopVariant{
				Id: v.ID.String(), Name: v.Name, Options: v.Options, PriceIdr: v.PriceIDR, MinNoticeHours: v.MinNoticeHours,
			}
		}),
	}
}
