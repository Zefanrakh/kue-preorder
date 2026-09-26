// Compile-only check of the checkout client as the cart page will use it: a
// quote over GET, the schedule-or-rejection oneof, and a suggested pickup to
// offer instead of a refused one. Nothing here executes.
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { CheckoutService, PickupProblem } from "../gen/ts/kuepreorder/orders/v1/checkout_pb";

const checkout = createClient(CheckoutService, createConnectTransport({ baseUrl: "http://localhost:8080", useHttpGet: true }));

type CartView =
  | { kind: "ok"; total: bigint; firstPayment: bigint; payInFull: boolean; balanceDue?: Date }
  | { kind: "pickup"; message: string; closed: boolean; suggestion?: Date }
  | { kind: "busy" };

export async function viewCart(lines: { variantId: string; quantity: number }[], pickup: Date): Promise<CartView> {
  try {
    const q = await checkout.quoteOrder({ items: lines, pickupAt: timestampFromDate(pickup) });
    switch (q.pickup.case) {
      case "schedule": {
        const s = q.pickup.value;
        return {
          kind: "ok",
          total: q.totalIdr,
          firstPayment: q.dpRequiredIdr,
          payInFull: s.fullPaymentRequired,
          balanceDue: s.balanceDueAt ? timestampDate(s.balanceDueAt) : undefined,
        };
      }
      case "rejection": {
        const r = q.pickup.value;
        return {
          kind: "pickup",
          message: r.message,
          closed: r.problem === PickupProblem.CLOSED,
          suggestion: r.suggestedPickupAt ? timestampDate(r.suggestedPickupAt) : undefined,
        };
      }
      default:
        throw new Error("quote without a schedule or a rejection");
    }
  } catch (err) {
    if (ConnectError.from(err).code === Code.ResourceExhausted) {
      return { kind: "busy" };
    }
    throw err;
  }
}
