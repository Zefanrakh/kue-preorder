// Compile-only check of the customer order client as the checkout page will
// use it: one idempotency key per checkout, the Precondition reasons that
// send a customer to sign in or to pay first, and the payment link to open.
// Nothing here executes.
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { CustomerOrderService, PaymentState, type Order } from "../gen/ts/kuepreorder/orders/v1/orders_pb";
import { FieldErrorsSchema, PreconditionSchema } from "../gen/ts/kuepreorder/validation/v1/validation_pb";

const orders = createClient(CustomerOrderService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

type Placed =
  | { kind: "placed"; order: Order; payAt?: string }
  | { kind: "sign_in" }
  | { kind: "pay_first"; message: string }
  | { kind: "fix"; fields: Record<string, string> };

export async function placeOrder(
  accessToken: string,
  checkoutKey: string, // crypto.randomUUID() once per checkout, reused on a retry
  cart: { variantId: string; quantity: number }[],
  pickup: Date,
  termsVersion: string,
  name: string,
  notes: string,
): Promise<Placed> {
  try {
    const res = await orders.placeOrder(
      { items: cart, pickupAt: timestampFromDate(pickup), termsVersion, customerName: name, notes, idempotencyKey: checkoutKey },
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );
    const order = res.order;
    if (order === undefined) {
      throw new Error("PlaceOrder returned no order");
    }
    const open = order.payments.find((p) => p.state === PaymentState.PENDING && p.checkoutUrl !== "");
    return { kind: "placed", order, payAt: open?.checkoutUrl };
  } catch (err) {
    const cerr = ConnectError.from(err);
    if (cerr.code === Code.FailedPrecondition) {
      const [p] = cerr.findDetails(PreconditionSchema);
      return p?.reason === "phone_required" ? { kind: "sign_in" } : { kind: "pay_first", message: p?.message ?? cerr.rawMessage };
    }
    if (cerr.code === Code.InvalidArgument) {
      return { kind: "fix", fields: cerr.findDetails(FieldErrorsSchema)[0]?.fields ?? {} };
    }
    throw cerr;
  }
}

export async function myOrderCodes(accessToken: string): Promise<string[]> {
  const res = await orders.listMyOrders({}, { headers: { Authorization: `Bearer ${accessToken}` } });
  return res.orders.map((o) => o.code);
}
