// Compile-only check of the stock client as the kitchen's PWA will use it:
// "Belanja masuk", the list to check before shopping with "Masih bagus" or
// "Buang", and a stock count. Nothing here executes.
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { CheckResult, DiscardReason, InventoryService, type LotToCheck } from "../gen/ts/kuepreorder/inventory/v1/inventory_pb";
import { FieldErrorsSchema, PreconditionSchema } from "../gen/ts/kuepreorder/validation/v1/validation_pb";

const stock = createClient(InventoryService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

const auth = (accessToken: string) => ({ headers: { Authorization: `Bearer ${accessToken}` } });

type Done = { kind: "done" } | { kind: "refused"; message: string } | { kind: "fix"; fields: Record<string, string> };

async function attempt(run: () => Promise<unknown>): Promise<Done> {
  try {
    await run();
    return { kind: "done" };
  } catch (err) {
    const cerr = ConnectError.from(err);
    if (cerr.code === Code.FailedPrecondition) {
      return { kind: "refused", message: cerr.findDetails(PreconditionSchema)[0]?.message ?? cerr.rawMessage };
    }
    if (cerr.code === Code.InvalidArgument) {
      return { kind: "fix", fields: cerr.findDetails(FieldErrorsSchema)[0]?.fields ?? {} };
    }
    throw cerr;
  }
}

export function belanjaMasuk(accessToken: string, ingredientId: string, qty: bigint, boughtAt: Date, note: string): Promise<Done> {
  return attempt(() => stock.receiveStock({ ingredientId, qty, receivedAt: timestampFromDate(boughtAt), note }, auth(accessToken)));
}

export async function toCheck(accessToken: string): Promise<LotToCheck[]> {
  const res = await stock.listLotsToCheck({}, auth(accessToken));
  return res.lots;
}

export function masihBagus(accessToken: string, lotId: string): Promise<Done> {
  return attempt(() => stock.checkLot({ lotId, result: CheckResult.OK }, auth(accessToken)));
}

export function buang(accessToken: string, lotId: string, reason: DiscardReason, note = ""): Promise<Done> {
  return attempt(() => stock.checkLot({ lotId, result: CheckResult.DISCARD, reason, note }, auth(accessToken)));
}

export function opname(accessToken: string, ingredientId: string, actualQty: bigint, reason: string): Promise<Done> {
  return attempt(() => stock.countStock({ ingredientId, actualQty, reason }, auth(accessToken)));
}
