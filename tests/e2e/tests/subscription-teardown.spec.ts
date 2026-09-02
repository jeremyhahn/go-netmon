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

// openFleetDevice sets the device selector's value and fires its change event.
// The selector is populated by a separate load from the one that reveals the
// fleet overview, so asking for a device before its option exists sets a value
// the select does not have, fires a change nothing matches, and leaves the page
// on no device at all -- which reads downstream as "nothing was subscribed".
async function openDeviceById(page: Page, deviceId: number) {
  await expect(
    page.locator(`#dashboardDeviceSelect option[value="${deviceId}"]`),
    `device ${deviceId} never appeared in the selector`,
  ).toHaveCount(1, { timeout: 30000 });
  await page.evaluate(id => (window as any).app.openFleetDevice(id), deviceId);
}

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
//
// The walk STARTS on the router deliberately. It carries 321 queries across 11
// tabs against a BMC's 185 across 5, so leaving it puts far more frames in
// flight than leaving a BMC does -- and it is those in-flight frames, arriving
// after the teardown, that the orphan check below is looking for. A walk over
// BMCs alone passes with the guard removed.
const WALK: [number, string][] = [
  [1, 'core-01'],
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
    await openDeviceById(page, Number(id));
    await page.waitForTimeout(9000);

    const keys: string[] = await page.evaluate(() =>
      Array.from((window as any).app.activeSubscriptions.keys()));
    seen.push({ device: name, keys });

    // The readings this browser holds must not outlive the subscriptions that
    // asked for them.
    //
    // A frame for the device just left can arrive after the teardown -- the
    // server had already queued it -- and recording it puts back an entry
    // nothing will remove again. Measured walking nine devices before this was
    // guarded: 628 readings held against 185 live queries, growing with every
    // device visited for the life of the session.
    const held = await page.evaluate(() => {
      const app = (window as any).app;
      let live = 0;
      app.activeSubscriptions.forEach((s: { objects: Map<string, unknown> }) => {
        live += s.objects.size;
      });
      const orphaned = [...app.lastReadings.keys()].filter((q: string) => {
        for (const [, sub] of app.activeSubscriptions) if (sub.objects.has(q)) return false;
        return true;
      });
      return { live, readings: app.lastReadings.size, orphaned: orphaned.length };
    });

    expect(held.orphaned,
      `${name}: ${held.orphaned} readings held for queries nothing is subscribed to ` +
      `(${held.readings} held, ${held.live} live)`).toBe(0);
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

/**
 * The walk above is integration coverage: it proves late frames really do
 * arrive, but only when the server's timing cooperates. These three deliver the
 * frames directly, so they hold whatever the appliance is doing.
 *
 * Both halves of the guard are covered. Without the snapshot guard a late
 * snapshot puts back a reading; without the delta guard a late delta cannot
 * recreate a reading but does leave an orphaned pendingResync entry and asks
 * the server, over the socket, to resend a query nobody is watching.
 */

// liveQuery opens a device and returns one query the page is actually watching,
// so the frames below are shaped like the ones the server sends for it.
async function liveQuery(page: Page): Promise<string> {
  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* private mode */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });
  await openDeviceById(page, 9);

  return await page.waitForFunction(() => {
    const app = (window as any).app;
    for (const [, sub] of app.activeSubscriptions) {
      const first = [...sub.objects.keys()][0];
      if (first) return first;
    }
    return null;
  }, undefined, { timeout: 60000 }).then(handle => handle.jsonValue() as Promise<string>);
}

const SAMPLE = { metric: { __name__: 'snmp_test', device: 'lab-node-1-bmc' }, value: 1 };

test('a snapshot for a query nothing is watching is not recorded', async ({ page }) => {
  test.setTimeout(120_000);
  const query = await liveQuery(page);

  const held = await page.evaluate(([q, sample]) => {
    const app = (window as any).app;
    app.unsubscribeFromAllDashboards(app.currentDashboardDevice);
    app.handleMessage({ type: 'metric_snapshot', query: q, seq: 1, metrics: [sample] });
    return app.lastReadings.has(q);
  }, [query, SAMPLE] as const);

  expect(held, `a reading was recorded for ${query} after it was unsubscribed`).toBe(false);
});

test('a delta for a query nothing is watching asks for no resync', async ({ page }) => {
  test.setTimeout(120_000);
  const query = await liveQuery(page);

  const after = await page.evaluate(([q, sample]) => {
    const app = (window as any).app;
    app.unsubscribeFromAllDashboards(app.currentDashboardDevice);

    const sent: string[] = [];
    app.ws.send = (frame: string) => { sent.push(frame); };

    app.handleMessage({
      type: 'metric_delta', query: q, seq: 9, base_seq: 8,
      delta: { changed: [sample], removed: [] },
    });
    return { pending: app.pendingResync.has(q), sent };
  }, [query, SAMPLE] as const);

  expect(after.pending, `${query} was queued for resync with nothing watching it`).toBe(false);
  expect(after.sent.filter(f => f.includes('resync')),
    'the server was asked to resend a query nobody is watching').toEqual([]);
});

test('a snapshot for a query the page is watching is still recorded', async ({ page }) => {
  test.setTimeout(120_000);
  const query = await liveQuery(page);

  // The guard has to refuse the departed and admit the current. A guard that
  // refused everything would pass both tests above and draw an empty page.
  const held = await page.evaluate(([q, sample]) => {
    const app = (window as any).app;
    app.handleMessage({ type: 'metric_snapshot', query: q, seq: 4, metrics: [sample] });
    const reading = app.lastReadings.get(q);
    return reading ? reading.seq : null;
  }, [query, SAMPLE] as const);

  expect(held, `a live query's own snapshot was dropped`).toBe(4);
});

/**
 * Loading a dashboard is a chain of awaits, and none of them is cancelled when
 * the reader moves on. Each of the three below is one suspension point where an
 * abandoned load could still install a subscription for a device nobody is
 * looking at -- which is worse than a stale paint, because a subscription is
 * what makes the departed device's frames look legitimate to applySnapshot.
 */

test('leaving for the fleet cancels a dashboard load already in flight', async ({ page }) => {
  test.setTimeout(180_000);

  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* private mode */ }
  });

  // Held long enough that the reader is unambiguously gone before it answers.
  await page.route(/\/api\/devices\/\d+\/dashboard$/, async route => {
    const response = await route.fetch();
    await new Promise(resolve => setTimeout(resolve, 4000));
    await route.fulfill({ response });
  });

  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  await openDeviceById(page, 9);
  await page.evaluate(() => (window as any).app.showFleetOverview());
  await page.waitForTimeout(12000);

  const subscriptions = await page.evaluate(() =>
    Array.from((window as any).app.activeSubscriptions.keys()));
  expect(subscriptions,
    'a dashboard the reader walked away from subscribed anyway').toEqual([]);
});

test('a render interrupted mid-pause installs no subscription', async ({ page }) => {
  test.setTimeout(180_000);
  await liveQuery(page);

  // renderPanels writes its markup, waits 100ms for layout, and only then
  // subscribes. This drives that window directly rather than trying to hit it
  // by navigating at the right moment.
  const remaining = await page.evaluate(async () => {
    const app = (window as any).app;
    const deviceId = app.currentDashboardDevice;
    const held = app.deviceDashboards[deviceId];
    const [module] = [...held.dashboards.keys()];

    app.unsubscribeFromAllDashboards(deviceId);

    const load = app.dashboardLoad;
    const rendering = app.renderPanels(deviceId, module, held.dashboards.get(module), load);
    app.dashboardLoad++;
    await rendering;

    return app.activeSubscriptions.size;
  });

  expect(remaining, 'the abandoned render subscribed after the reader left').toBe(0);
});

test('a departed device cannot paint its status banner over the current one', async ({ page }) => {
  test.setTimeout(180_000);
  await liveQuery(page);

  // The status is forced to one that DOES paint. A healthy device hides the
  // banner, so asserting it stayed hidden against a healthy fleet asserts
  // nothing -- verified by removing the guard and watching the test pass.
  await page.route(/\/api\/devices\/\d+\/status$/, route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      connected: false,
      last_error: 'the load that asked for this had already been abandoned',
      last_poll_time: new Date().toISOString(),
      metrics_collected: 0,
      poll_interval: '30s',
    }),
  }));

  // The banner is shared by every device, and fetchAndDisplayDeviceStatus
  // paints it before returning -- so its caller's generation check is too late.
  const banner = await page.evaluate(async () => {
    const app = (window as any).app;
    const el = document.getElementById('deviceStatusBanner');
    el.style.display = 'none';

    const load = app.dashboardLoad;
    const fetching = app.fetchAndDisplayDeviceStatus(app.currentDashboardDevice, load);
    app.dashboardLoad++;
    await fetching;

    return { display: el.style.display, text: el.textContent || '' };
  });

  expect(banner.display, 'a departed load painted the shared status banner').toBe('none');
  expect(banner.text, 'a departed load wrote into the shared status banner')
    .not.toContain('already been abandoned');
});

/**
 * A rejection is a completion path too. Both of these drive the failure branch,
 * which the tests above do not reach: they use successful responses, and the
 * catch blocks were the two places still touching shared state unconditionally.
 */

test('a failed status request for a departed device leaves the current banner alone', async ({ page }) => {
  test.setTimeout(180_000);
  await liveQuery(page);

  await page.route(/\/api\/devices\/\d+\/status$/, route => route.abort('failed'));

  const banner = await page.evaluate(async () => {
    const app = (window as any).app;
    // The banner belonging to the device on screen.
    app.showDeviceStatusBanner('error', 'Polling Error', 'the device the reader is looking at', null);
    const el = document.getElementById('deviceStatusBanner');
    const before = el.style.display;

    const load = app.dashboardLoad;
    const fetching = app.fetchAndDisplayDeviceStatus(app.currentDashboardDevice, load);
    app.dashboardLoad++;
    await fetching;

    return { before, after: el.style.display, text: el.textContent || '' };
  });

  expect(banner.before, 'the fixture never painted a banner, so it proves nothing')
    .not.toBe('none');
  expect(banner.after,
    "a departed device's failed request cleared the current device's banner")
    .toBe(banner.before);
  expect(banner.text).toContain('the device the reader is looking at');
});

test('a failed dashboard load for a departed device reports no failure', async ({ page }) => {
  test.setTimeout(180_000);
  await liveQuery(page);

  // Long enough that the status request ahead of it has certainly finished, so
  // the load is past its first check and the rejection lands in the catch.
  await page.route(/\/api\/devices\/\d+\/dashboard$/, async route => {
    await new Promise(resolve => setTimeout(resolve, 4000));
    await route.abort('failed');
  });

  const notices = await page.evaluate(async () => {
    const app = (window as any).app;
    const seen: string[] = [];
    app.showNotification = (message: string) => { seen.push(message); };

    const loading = app.loadDeviceDashboards(app.currentDashboardDevice);
    await new Promise(resolve => setTimeout(resolve, 2000));
    app.dashboardLoad++;
    await loading;

    return seen;
  });

  expect(notices,
    'the reader was told the dashboard they had already left failed to load').toEqual([]);
});
