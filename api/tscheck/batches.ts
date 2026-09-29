// Compile-only check of the batch client as the kitchen's shopping page
// will use it: the day's list by supplier, in packs where there is a pack,
// the cost, and a broken recipe shown as it is. Nothing here executes.
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { BatchService, LineStatus, type BatchDetail } from "../gen/ts/kuepreorder/aggregation/v1/batches_pb";
import { BaseUnit } from "../gen/ts/kuepreorder/catalog/v1/catalog_pb";
import { PreconditionSchema } from "../gen/ts/kuepreorder/validation/v1/validation_pb";

const batches = createClient(BatchService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

const auth = (accessToken: string) => ({ headers: { Authorization: `Bearer ${accessToken}` } });

const units: Record<BaseUnit, string> = {
  [BaseUnit.UNSPECIFIED]: "",
  [BaseUnit.GRAM]: "g",
  [BaseUnit.MILLILITRE]: "ml",
  [BaseUnit.PIECE]: "pcs",
};

// One line per ingredient still to buy, such as "Toko Sinar: Tepung 2 kg".
export function shoppingList(batch: BatchDetail): string[] {
  return batch.lines
    .filter((l) => l.toBuy > 0n && l.status !== LineStatus.RECEIVED)
    .map((l) => {
      const what = l.pack !== undefined ? `${l.packs} ${l.pack.unit}` : `${l.toBuy} ${units[l.baseUnit]}`;
      return `${l.pack?.supplierName ?? "Tanpa supplier"}: ${l.ingredientName} ${what}`;
    });
}

export async function today(accessToken: string, date: string): Promise<{ list: string[]; costIdr: bigint; problem?: string } | undefined> {
  try {
    const res = await batches.getBatch({ date }, auth(accessToken));
    const batch = res.batch;
    if (batch === undefined) {
      return undefined;
    }
    return { list: shoppingList(batch), costIdr: batch.costIdr, problem: batch.batch?.error || undefined };
  } catch (err) {
    const cerr = ConnectError.from(err);
    if (cerr.code === Code.NotFound) {
      return undefined; // no order for that day yet
    }
    throw cerr;
  }
}

export async function recompute(accessToken: string, date: string): Promise<string | undefined> {
  try {
    await batches.recomputeBatch({ date }, auth(accessToken));
    return undefined;
  } catch (err) {
    const cerr = ConnectError.from(err);
    if (cerr.code === Code.FailedPrecondition) {
      return cerr.findDetails(PreconditionSchema)[0]?.message ?? cerr.rawMessage;
    }
    throw cerr;
  }
}
