// The e2e target stack's seeded devices, looked up by name; NETMON_DEVICE picks another.
import type { APIRequestContext } from '@playwright/test';
import { BASE } from './target';

export const SEEDED_DEVICES = ['snmpsim-01', 'snmpsim-02'] as const;

export const SEEDED_DEVICE: string = process.env.NETMON_DEVICE || SEEDED_DEVICES[0];

export interface StackDevice {
  id: number;
  name: string;
}

// deviceByName looks a device up by name in the web UI's device list. It
// throws, naming the devices the stack does have, rather than letting a spec
// time out on a selector that never appears.
export async function deviceByName(
  request: APIRequestContext, name: string = SEEDED_DEVICE,
): Promise<StackDevice> {
  const response = await request.get(`${BASE}/api/devices`);
  if (!response.ok()) {
    throw new Error(`GET /api/devices answered ${response.status()}`);
  }
  const body = await response.json() as { devices?: StackDevice[] };
  const devices = body.devices ?? [];
  const device = devices.find(d => d.name === name);
  if (!device) {
    throw new Error(`device ${name} is not on ${BASE}; it has: ` +
      (devices.map(d => d.name).join(', ') || 'none'));
  }
  return { id: device.id, name: device.name };
}

// reportingModules is the modules the stack's dashboard for a device draws
// from series that are actually reporting: what the poller walks, not what a
// spec expects.
export async function reportingModules(
  request: APIRequestContext, device: StackDevice,
): Promise<string[]> {
  const response = await request.get(`${BASE}/api/devices/${device.id}/dashboard`);
  if (!response.ok()) {
    throw new Error(`GET /api/devices/${device.id}/dashboard (${device.name}) answered ` +
      `${response.status()}: ${await response.text()}`);
  }
  const body = await response.json() as { mibs?: { mib: string; reporting: number }[] };
  return (body.mibs ?? []).filter(m => m.reporting > 0).map(m => m.mib);
}
