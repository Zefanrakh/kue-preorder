// Package connect serves the catalog over ConnectRPC: the CMS as
// kuepreorder.catalog.v1.CatalogAdminService and the public shop as
// kuepreorder.catalog.v1.StorefrontService. Handlers only translate between
// protobuf and the catalog's domain types; every rule lives in catalog.Service
// and catalog.Storefront.
package connect

import (
	"context"
	"log/slog"

	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
)

// requestField renames the service's field names that differ from the
// request messages, so a form can mark the field it sent.
var requestField = map[string]string{
	"params": "params_json",
}

func connectError(ctx context.Context, logger *slog.Logger, err error) error {
	return rpcerr.Wrap(ctx, logger, err, requestField)
}

// ids parses the UUID fields of one request; see rpcerr.IDs.
type ids = rpcerr.IDs
