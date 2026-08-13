import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// Leaving a device must actually let go of it.
//
// Subscriptions are keyed `${deviceId}-${mibName}`, and the teardown recovered
// the MIB name from that key with split('-')[1]. MIB names carry hyphens --
// HOST-RESOURCES-MIB, IF-MIB, CISCO-PROCESS-MIB -- so the key "14-HOST-RESOURCES-MIB"
// yielded "HOST", the lookup for "14-HOST" missed, and nothing was released.
//
// Every MIB with a hyphen in its name therefore leaked on every device switch,
// which is nearly all of them. Walking nine devices in one session left the
// later dashboards starved: lab-node-5-bmc showed 84 cards still waiting for data
// at the end of a run, and 1 when it was opened on its own.
//
// This walks a few devices and checks that nothing belonging to a device the
// reader has left is still subscribed.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';
const STATIC = path.resolve(__dirname, '../../../internal/webui/static');

test.use({
  launchOptions: {
    executablePath: process.env.CHROME_PATH || '/usr/bin/google-chrome',
    args: [
      '--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu',
      '--disable-features=LocalNetworkAccessChecks,PrivateNetworkAccessChecks',
    ],
  },
});

const ASSETS: [string, string, string][] = [
  ['**/js/app.js*', 'js/app.js', 'text/javascript'],
  ['**/js/chart-theme.js*', 'js/chart-theme.js', 'text/javascript'],
  ['**/css/style.css*', 'css/style.css', 'text/css'],
];

async function serveWorkingTree(page: Page) {
  for (const [glob, file, type] of ASSETS) {
    await page.route(glob, route => route.fulfill({
      status: 200, contentType: type, body: fs.readFileSync(path.join(STATIC, file), 'utf8'),
    }));
  }
  await page.route(url => url.pathname === '/', async route => {
    const response = await route.fetch();
    await route.fulfill({ response, body: fs.readFileSync(path.join(STATIC, 'index.html'), 'utf8') });
  });
}

// The BMCs share a profile, so each carries the same hyphenated MIB names --
// exactly the case the teardown mishandled.
const WALK: [number, string][] = [
  [9, 'lab-node-1-bmc'], [14, 'lab-node-2-bmc'], [15, 'lab-node-3-bmc'], [17, 'lab-node-5-bmc'],
];

test('leaving a device releases its subscriptions', async ({ page }) => {
  test.setTimeout(300_000);

  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* private mode */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  const seen: { device: string; keys: string[] }[] = [];

  for (const [id, name] of WALK) {
    await page.evaluate(deviceId => (window as any).app.openFleetDevice(deviceId), id);
    await page.waitForTimeout(9000);

    const keys: string[] = await page.evaluate(() =>
      Array.from((window as any).app.activeSubscriptions.keys()));
    seen.push({ device: name, keys });
  }

  console.log('\n=== subscriptions held after each device ===');
  for (const { device, keys } of seen) {
    console.log(`  ${device.padEnd(12)} ${String(keys.length).padStart(3)} keys`);
  }

  const last = seen[seen.length - 1];
  expect(last.keys.length, 'the final device subscribed to nothing at all').toBeGreaterThan(0);

  // Only the device still on screen may hold subscriptions. Any key belonging
  // to a device already left behind is a leak.
  const departed = WALK.slice(0, -1).map(([id]) => String(id));
  const stale = last.keys.filter(key => departed.includes(key.split('-')[0]));

  expect(stale,
    `subscriptions survived leaving their device: ${stale.slice(0, 8).join(', ')}`)
    .toHaveLength(0);

  // And the total must not simply grow with every device visited, which is what
  // starved the later dashboards.
  const first = seen[0].keys.length;
  expect(last.keys.length,
    `subscriptions accumulated across the walk: ${seen.map(s => s.keys.length).join(' -> ')}`)
    .toBeLessThanOrEqual(first * 2);
});
