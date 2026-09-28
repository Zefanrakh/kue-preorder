// Compile-only check of the payment settings client as the owner's CMS page
// will use it: the fees shown next to Midtrans' prices with the server's
// examples, a change with its reason, and the refusal to switch off the
// last method. Nothing here executes.
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { PaymentMethod } from "../gen/ts/kuepreorder/orders/v1/checkout_pb";
import {
  PaymentSettingsService,
  type FeeRule,
  type PaymentPolicy,
  type PaymentSettings,
} from "../gen/ts/kuepreorder/payments/v1/settings_pb";
import { FieldErrorsSchema, PreconditionSchema } from "../gen/ts/kuepreorder/validation/v1/validation_pb";

const cms = createClient(PaymentSettingsService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

const auth = (accessToken: string) => ({ headers: { Authorization: `Bearer ${accessToken}` } });

type Saved = { kind: "saved"; settings: PaymentSettings } | { kind: "refused"; message: string } | { kind: "fix"; fields: Record<string, string> };

async function save(run: () => Promise<{ settings?: PaymentSettings }>): Promise<Saved> {
  try {
    const res = await run();
    if (res.settings === undefined) {
      throw new Error("no settings in the response");
    }
    return { kind: "saved", settings: res.settings };
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

// One line per method: its fee on each example amount, and whether it is
// the shop's own price or Midtrans' published one.
export async function feeTable(accessToken: string): Promise<string[]> {
  const res = await cms.getPaymentSettings({}, auth(accessToken));
  return (res.settings?.methods ?? []).map((m) => {
    const own = m.rule?.updatedAt !== undefined ? "tarif toko" : "tarif Midtrans";
    const fees = m.examples.map((e) => `${e.amountIdr}: ${e.feeIdr}`).join(", ");
    return `${PaymentMethod[m.rule?.method ?? PaymentMethod.UNSPECIFIED]} (${own}) ${fees}`;
  });
}

export function lowerDPThreshold(accessToken: string, policy: PaymentPolicy, thresholdIdr: bigint, reason: string): Promise<Saved> {
  return save(() => cms.updatePaymentPolicy({ policy: { ...policy, dpMinTotalIdr: thresholdIdr }, reason }, auth(accessToken)));
}

export function switchOff(accessToken: string, rule: FeeRule, reason: string): Promise<Saved> {
  return save(() => cms.updatePaymentMethod({ rule: { ...rule, enabled: false }, reason }, auth(accessToken)));
}

export function backToMidtrans(accessToken: string, method: PaymentMethod, reason: string): Promise<Saved> {
  return save(() => cms.resetPaymentMethod({ method, reason }, auth(accessToken)));
}
