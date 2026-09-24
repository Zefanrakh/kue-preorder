// Compile-only check that the generated client is usable the way web/ will use
// it: a typed Connect client, auth header, optional fields, and enums. CI runs
// `tsc` over this file and api/gen/ts; nothing here executes.
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { IdentityService, StaffRole, type WhoAmIResponse } from "../gen/ts/kuepreorder/identity/v1/identity_pb";

const client = createClient(IdentityService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

export async function staffRoles(accessToken: string): Promise<StaffRole[]> {
  const res: WhoAmIResponse = await client.whoAmI({}, { headers: { Authorization: `Bearer ${accessToken}` } });
  const customerId: string | undefined = res.customerId;
  void customerId;
  return res.roles.filter((role) => role !== StaffRole.UNSPECIFIED);
}
