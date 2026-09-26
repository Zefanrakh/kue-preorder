// Compile-only check of the scheduling client as the CMS will use it: an
// optional capacity that can be cleared, WIB times as "HH:MM" strings, and
// FieldErrors on a failed save. Nothing here executes.
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { ScheduleAdminService, type ScheduleSettings } from "../gen/ts/kuepreorder/scheduling/v1/scheduling_pb";
import { FieldErrorsSchema } from "../gen/ts/kuepreorder/validation/v1/validation_pb";

const schedule = createClient(ScheduleAdminService, createConnectTransport({ baseUrl: "http://localhost:8080" }));

export async function savePickupHours(
  accessToken: string,
  current: ScheduleSettings,
  start: string,
  end: string,
  capacity: number | undefined,
): Promise<ScheduleSettings | Record<string, string>> {
  try {
    const res = await schedule.updateScheduleSettings(
      {
        shoppingBufferHours: current.shoppingBufferHours,
        dailyCapacityMinutes: capacity, // undefined removes the limit
        pickupWindowStart: start,
        pickupWindowEnd: end,
      },
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );
    return res.settings ?? current;
  } catch (err) {
    const cerr = ConnectError.from(err);
    if (cerr.code !== Code.InvalidArgument) {
      throw cerr;
    }
    return cerr.findDetails(FieldErrorsSchema)[0]?.fields ?? {};
  }
}

export async function closeDay(accessToken: string, date: string, reason: string): Promise<string> {
  const res = await schedule.addClosedDate({ date, reason }, { headers: { Authorization: `Bearer ${accessToken}` } });
  return res.closedDate?.date ?? date;
}
