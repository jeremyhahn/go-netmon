import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// What actually crosses the wire while a dashboard is open.
//
// Two things used to make this grow with the size of the fleet rather than with
// what the operator was looking at:
//
//   - a warm-up subscription to unqualified names ("snmp_sysUpTime"), which
//     match every series on every device. At ten thousand devices that is about
//     1.5MB every five seconds to every open browser, whichever device it was
//     showing -- and nothing in the page ever read it.
//   - re-sending every subscribed result on every five-second round, though
//     devices are polled every 30 to 60 seconds, so most rounds carried a
//     sample the browser already had.
//
// This measures the frames themselves, against the deployed container.

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

type Frame = { query: string; kind: string; series: number; bytes: number };

async function serveWorkingTree(page: Page) {
  const assets: [string, string, string][] = [
    ['**/js/app.js*', 'js/app.js', 'text/javascript'],
    ['**/js/chart-theme.js*', 'js/chart-theme.js', 'text/javascript'],
    ['**/css/style.css*', 'css/style.css', 'text/css'],
  ];
  for (const [glob, file, type] of assets) {
    await page.route(glob, route => route.fulfill({
      status: 200, contentType: type, body: fs.readFileSync(path.join(STATIC, file), 'utf8'),
    }));
  }
  await page.route(url => url.pathname === '/', async route => {
    const response = await route.fetch();
    await route.fulfill({ response, body: fs.readFileSync(path.join(STATIC, 'index.html'), 'utf8') });
  });
}

// watch records every reading the browser receives, of either kind.
//
// A reading arrives either whole (metric_snapshot) or as what changed
// (metric_delta). Heartbeats are excluded: they carry no data and one covers
// every steady query in a round.
//
// Both kinds are counted deliberately. Watching for one name only is how this
// measurement came to pass while measuring nothing at all: the frames were
// renamed and the filter matched none of them, so a test whose whole point is
// that the wire is quiet reported a quiet wire because it had stopped looking.
function watch(page: Page, frames: Frame[]) {
  page.on('websocket', ws => {
    ws.on('framereceived', f => {
      const payload = f.payload.toString();
      try {
        const msg = JSON.parse(payload);
        if (msg.type !== 'metric_snapshot' && msg.type !== 'metric_delta') return;
        const series = msg.type === 'metric_snapshot'
          ? (msg.metrics?.length || 0)
          : (msg.delta?.changed?.length || 0) + (msg.delta?.removed?.length || 0);
        frames.push({ query: msg.query, kind: msg.type, series, bytes: payload.length });
      } catch { /* not a frame we measure */ }
    });
  });
}

test('an idle fleet overview streams nothing', async ({ page }) => {
  test.setTimeout(180_000);

  const frames: Frame[] = [];
  watch(page, frames);
  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
  });

  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  // Four streaming rounds with no device selected.
  await page.waitForTimeout(22000);

  const bytes = frames.reduce((sum, f) => sum + f.bytes, 0);
  console.log(`\n=== fleet overview, ~4 rounds: ${frames.length} frames, ${bytes} bytes ===`);
  for (const f of frames.slice(0, 5)) {
    console.log(`  ${f.kind} ${f.query} -> ${f.series} series, ${f.bytes} B`);
  }

  // Nothing is selected, so nothing should be streaming. The overview has its
  // own status endpoint and does not need the metric stream at all.
  expect(frames,
    `the overview streamed ${frames.length} frames (${bytes} B) for devices nobody is looking at`)
    .toHaveLength(0);
});

test('a steady chart keeps advancing while its value holds', async ({ page }) => {
  // The point of a tick-based live view is that you can see a value over time,
  // which includes seeing that it has not moved. Sending only changes made a
  // steady line stop at its last change and read as data having stopped, so a
  // steady query is announced by name and the browser carries the reading it
  // already has forward to the current tick.
  test.setTimeout(240_000);

  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  await openDeviceById(page, 9);
  await page.waitForTimeout(20000);

  // Count the points on every chart that has drawn, then leave it alone.
  const pointsNow = () => page.evaluate(() => {
    const app = (window as any).app;
    const counts: Record<string, number> = {};
    for (const [id, chart] of Object.entries<any>(app.charts || {})) {
      if (!chart?._hasData) continue;
      const data = chart.getOption()?.series?.[0]?.data;
      if (Array.isArray(data)) counts[id] = data.length;
    }
    return counts;
  });

  const before = await pointsNow();
  await page.waitForTimeout(35000); // ~7 ticks
  const after = await pointsNow();

  const watched = Object.keys(before).filter(id => id in after);
  const advanced = watched.filter(id => after[id] > before[id]);

  console.log('\n=== chart advance over ~7 idle ticks ===');
  console.log(`  charts with data : ${watched.length}`);
  console.log(`  charts that grew : ${advanced.length}`);
  for (const id of watched.slice(0, 4)) {
    console.log(`    ${id}: ${before[id]} -> ${after[id]} points`);
  }

  expect(watched.length, 'no chart had drawn, so there is nothing to watch').toBeGreaterThan(0);
  // These are 60s-polled readings, so almost none of them changed during the
  // window. Every one of them must still have moved forward.
  expect(advanced.length,
    `only ${advanced.length} of ${watched.length} charts advanced; a steady line that stops reads as data having stopped`)
    .toBe(watched.length);
});

test('an open dashboard streams only what changed', async ({ page }) => {
  test.setTimeout(300_000);

  const frames: Frame[] = [];
  watch(page, frames);
  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
  });

  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  // lab-node-1-bmc is polled every 60s, so most five-second rounds have nothing
  // new to say about it.
  await openDeviceById(page, 9);
  await page.waitForTimeout(20000);

  const settled = frames.length;
  const settledBytes = frames.reduce((sum, f) => sum + f.bytes, 0);

  // Now watch a full minute: at one 60s poll, only one round per subscription
  // can carry anything new.
  await page.waitForTimeout(60000);

  const steady = frames.slice(settled);
  const steadyBytes = steady.reduce((sum, f) => sum + f.bytes, 0);
  const queries = await page.evaluate(() => {
    let n = 0;
    (window as any).app.activeSubscriptions.forEach((v: any) => {
      n += (v.metricQueries?.size || 0) + (v.statusQueries?.size || 0);
    });
    return n;
  });

  console.log('\n=== one dashboard on a 60s-polled device ===');
  console.log(`  subscribed queries    : ${queries}`);
  console.log(`  opening (~4 rounds)   : ${settled} frames, ${settledBytes} B`);
  console.log(`  next 60s (12 rounds)  : ${steady.length} frames, ${steadyBytes} B`);
  console.log(`  without change filter  : ~${queries * 12} frames would have been sent`);

  expect(queries, 'nothing was subscribed, so there is nothing to measure').toBeGreaterThan(0);
  expect(settled, 'opening the dashboard delivered no data at all').toBeGreaterThan(0);

  // Twelve rounds over a 60s poll interval: without change detection every
  // subscription would send twelve times. One poll can only justify about one
  // frame per subscription, and most subscriptions are steady readings that do
  // not move at all.
  expect(steady.length,
    `${steady.length} frames over 12 rounds for ${queries} subscriptions; unchanged results are still being sent`)
    .toBeLessThan(queries * 3);
});
