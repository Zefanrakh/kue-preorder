// Compile-only check of the purchasing client as the kitchen's PWA will use
// it: order a supplier's part of the shopping list, tick what arrived, and
// cancel what will not come. Nothing here executes.
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { ItemStatus, ProcurementService, type PurchaseOrder } from "../gen/ts/kuepreorder/procurement/v1/procurement_pb";
import { PreconditionSchema } from "../gen/ts/kuepreorder/validation/v1/validation_pb";

const buying = createClient(ProcurementService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

const auth = (accessToken: string) => ({ headers: { Authorization: `Bearer ${accessToken}` } });

// supplierId "" orders the ingredients without a supplier ("Belanja sendiri").
export async function pesan(accessToken: string, date: string, supplierId: string): Promise<PurchaseOrder | string> {
  try {
    const res = await buying.createOrder({ date, supplierId }, auth(accessToken));
    return res.order ?? "Pesanan tidak terbaca";
  } catch (err) {
    const cerr = ConnectError.from(err);
    if (cerr.code === Code.FailedPrecondition) {
      return cerr.findDetails(PreconditionSchema)[0]?.message ?? cerr.rawMessage;
    }
    throw cerr;
  }
}

// Ticks every waiting item as arrived in full, unless the kitchen typed
// another amount.
export async function terima(accessToken: string, order: PurchaseOrder, typed: Map<string, bigint>): Promise<PurchaseOrder | undefined> {
  const items = order.items
    .filter((it) => it.status === ItemStatus.ORDERED)
    .map((it) => ({ itemId: it.id, qty: typed.get(it.id) ?? it.qty }));
  const res = await buying.receiveOrder({ orderId: order.id, items }, auth(accessToken));
  return res.order;
}

export async function batalkan(accessToken: string, orderId: string, reason: string): Promise<void> {
  await buying.cancelOrder({ orderId, reason }, auth(accessToken));
}
