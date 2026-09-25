// Compile-only check of the storefront client as the shop pages will use it:
// no sign-in, GET requests a server or CDN can cache, and int64 prices as
// bigint formatted as rupiah without going through a float. Nothing here executes.
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { StorefrontService, type ShopProduct } from "../gen/ts/kuepreorder/catalog/v1/storefront_pb";

const shop = createClient(StorefrontService, createConnectTransport({ baseUrl: "http://localhost:8080", useHttpGet: true }));

const rupiah = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });

export async function menu(): Promise<{ name: string; from: string }[]> {
  const res = await shop.listShopProducts({});
  return res.products.map((p) => ({ name: p.name, from: rupiah.format(p.variants[0]?.priceIdr ?? 0n) }));
}

// productPage returns undefined for a product that is not on sale.
export async function productPage(slug: string): Promise<ShopProduct | undefined> {
  try {
    return (await shop.getShopProduct({ slug })).product;
  } catch (err) {
    if (ConnectError.from(err).code === Code.NotFound) {
      return undefined;
    }
    throw err;
  }
}
