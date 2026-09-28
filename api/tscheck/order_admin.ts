// Compile-only check of the order CMS client as the kitchen's and the
// owner's pages will use it: the day's list, a production step, a manual
// payment with its proof, and a refund, with the Precondition message shown
// as it is. Nothing here executes.
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { OrderStatus, type Order } from "../gen/ts/kuepreorder/orders/v1/orders_pb";
import { CancelMode, OrderAdminService, type StaffOrderSummary } from "../gen/ts/kuepreorder/orders/v1/admin_pb";
import { FieldErrorsSchema, PreconditionSchema } from "../gen/ts/kuepreorder/validation/v1/validation_pb";

const cms = createClient(OrderAdminService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

const auth = (accessToken: string) => ({ headers: { Authorization: `Bearer ${accessToken}` } });

type Outcome = { kind: "done"; order: Order } | { kind: "refused"; message: string } | { kind: "fix"; fields: Record<string, string> };

async function attempt(run: () => Promise<{ order?: Order }>): Promise<Outcome> {
  try {
    const res = await run();
    if (res.order === undefined) {
      throw new Error("no order in the response");
    }
    return { kind: "done", order: res.order };
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

// The kitchen's day: what to bake, by pickup time.
export async function todaysBakes(accessToken: string, day: string): Promise<StaffOrderSummary[]> {
  const res = await cms.listOrders(
    { fromDate: day, toDate: day, statuses: [OrderStatus.CONFIRMED, OrderStatus.IN_PRODUCTION, OrderStatus.READY] },
    auth(accessToken),
  );
  return res.orders;
}

export function markReady(accessToken: string, code: string): Promise<Outcome> {
  return attempt(() => cms.advanceOrder({ code, status: OrderStatus.READY }, auth(accessToken)));
}

export function recordTransfer(accessToken: string, code: string, amountIdr: bigint, reference: string, note: string): Promise<Outcome> {
  return attempt(() => cms.recordManualPayment({ code, amountIdr, reference, note }, auth(accessToken)));
}

export function refund(accessToken: string, code: string, reason: string, refundReference: string): Promise<Outcome> {
  return attempt(() => cms.cancelOrder({ code, mode: CancelMode.REFUND, reason, refundReference }, auth(accessToken)));
}

export function proofs(order: Order): string[] {
  return order.payments.flatMap((p) => (p.manual === undefined ? [] : [`${p.manual.reference}: ${p.manual.note}`]));
}
