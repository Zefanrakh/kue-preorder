// Compile-only check of the catalog client as the CMS will use it: enums,
// optional fields, int64 money as bigint, and FieldErrors details on a failed
// save. Nothing here executes.
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { BaseUnit, CatalogAdminService, type Ingredient } from "../gen/ts/kuepreorder/catalog/v1/catalog_pb";
import { FieldErrorsSchema } from "../gen/ts/kuepreorder/validation/v1/validation_pb";

const catalog = createClient(CatalogAdminService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

type Saved<T> = { ok: true; value: T } | { ok: false; fields: Record<string, string> };

// createIngredient saves the form, or returns the message for each field to fix.
export async function createIngredient(accessToken: string, name: string, shelfLifeDays?: number): Promise<Saved<Ingredient>> {
  try {
    const res = await catalog.createIngredient(
      { name, baseUnit: BaseUnit.GRAM, perishable: shelfLifeDays !== undefined, shelfLifeDays },
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );
    if (res.ingredient === undefined) {
      throw new Error("CreateIngredient returned no ingredient");
    }
    return { ok: true, value: res.ingredient };
  } catch (err) {
    const cerr = ConnectError.from(err);
    if (cerr.code !== Code.InvalidArgument && cerr.code !== Code.AlreadyExists) {
      throw cerr;
    }
    const [details] = cerr.findDetails(FieldErrorsSchema);
    return { ok: false, fields: details?.fields ?? {} };
  }
}

// Money is int64 rupiah: a bigint in TypeScript, never a float.
export async function variantPrices(accessToken: string, productId: string): Promise<Map<string, bigint>> {
  const res = await catalog.listVariants({ productId }, { headers: { Authorization: `Bearer ${accessToken}` } });
  return new Map(res.variants.map((v) => [v.id, v.priceIdr]));
}
