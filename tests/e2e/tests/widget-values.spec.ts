import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// A card that draws is not the same as a card that is right.
//
// The render audit proves every card resolves. This checks the three ways a
// resolved card can still be wrong: the widget type does not suit the value, a
// number is shown without the unit its MIB declares, or an enumeration is shown
// as the integer on the wire rather than the name the MIB gives it.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';

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

async function openDevice(page: Page, deviceId: number, mib: string) {
  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  await openDeviceById(page, deviceId);

  // Wait for the tab to EXIST rather than for a fixed number of seconds. The
  // rail is built from one request now, so it appears quickly -- but the
  // panels behind it fill on the streaming tick, and a fixed 15s was short
  // enough that these read "Waiting for data" and reported a product failure
  // that was a fixture impatience.
  const tab = page.locator('#mibTabList .tab-button', { hasText: mib }).first();
  await expect(tab, `no ${mib} tab for device ${deviceId}`).toBeVisible({ timeout: 60000 });
  await tab.click();

  // And wait for the pane to actually fill: nothing below can distinguish an
  // empty panel from a wrong one.
  await expect.poll(async () => page.evaluate(() => {
    const pane = document.querySelector('#mibTabContent .tab-pane.active');
    if (!pane) return -1;
    const total = pane.querySelectorAll('.panel').length;
    const waiting = pane.querySelectorAll('.chart-container.is-empty').length +
      [...pane.querySelectorAll('.status-value')]
        .filter(el => (el as HTMLElement).dataset.waiting).length;
    return total === 0 ? -1 : waiting;
  }), {
    timeout: 90000,
    message: `${mib} on device ${deviceId} never filled, so nothing below can be judged`,
  }).toBe(0);

  for (let i = 0; i < 4; i++) { await page.mouse.wheel(0, 1200); await page.waitForTimeout(400); }
}

test('a text reading is shown as text, not plotted as a number', async ({ page }) => {
  // The ATEN KVM states its condition in sentences: "PSU2 is abnormal, please
  // check the device". There is nothing to plot, and a line chart of a constant
  // 1 would hide the only thing the object says.
  await openDevice(page, 4, 'KNGeneric-MIB');

  // The tab that was opened, not the whole document. Every module's pane is
  // built up front but filled in turn, so scanning all of them measures
  // whichever ones happened to have arrived -- which is why this read one tile
  // on one run and fifty-five on the next.
  const shown = await page.evaluate(() =>
    Array.from(document.querySelectorAll('#mibTabContent .tab-pane.active .status-value'))
      .map(el => (el.textContent || '').trim())
      .filter(Boolean));

  const psu = shown.find(s => /PSU\d is|abnormal/i.test(s));
  expect(psu, `no PSU reading among the status tiles: ${shown.slice(0, 12).join(' | ')}`).toBeTruthy();
  // The device's own words, not a number.
  expect(psu).toMatch(/[A-Za-z]{3,}/);
});

test('a counter is charted as a rate, with per-second units', async ({ page }) => {
  await openDevice(page, 1, 'IF-MIB');

  const charted = await page.evaluate(() => {
    const app = (window as any).app;
    const rates: { id: string; unit: string }[] = [];
    for (const [id, chart] of Object.entries<any>(app.charts || {})) {
      if (!/Octets|Pkts/i.test(id) || !chart?._hasData) continue;
      const option = chart.getOption();
      rates.push({ id, unit: option?.yAxis?.[0]?.name || '' });
    }
    return rates.slice(0, 6);
  });

  expect(charted.length, 'no interface counter drew anything').toBeGreaterThan(0);
  // A counter shown as a rate is per-second; showing the raw counter would draw
  // a line that only ever climbs and says nothing about throughput.
  const persecond = charted.filter(c => /\/s|per second|bps/i.test(c.unit));
  expect(persecond.length,
    `counters charted without a per-second unit: ${JSON.stringify(charted)}`).toBeGreaterThan(0);
});

test('an enumerated value is shown by name, not by its number', async ({ page }) => {
  await openDevice(page, 1, 'IF-MIB');

  // The readings themselves, not the tile's textContent.
  //
  // A tile is a list of rows and textContent runs them together with no
  // separator -- "Port-channel1.1000upGigabitEthernet0up" -- so a word-boundary
  // match for "up" finds nothing however correct the decode is. Reading the
  // value cells is both more precise and what the assertion is actually about.
  const statuses = await page.evaluate(() => {
    const pane = document.querySelector('#mibTabContent .tab-pane.active');
    const rows = Array.from(pane?.querySelectorAll('.panel-row-value') || [])
      .map(el => (el.textContent || '').trim());
    const singles = Array.from(pane?.querySelectorAll('.status-value') || [])
      .filter(el => !el.querySelector('.panel-rows'))
      .map(el => (el.textContent || '').trim());
    return [...rows, ...singles].filter(t => t && !/waiting for data/i.test(t));
  });

  expect(statuses.length, 'no status tiles rendered at all').toBeGreaterThan(0);

  // ifOperStatus is 1..7 on the wire and up/down/testing in the MIB. Somewhere
  // among the tiles the words have to appear; a tile reading "1" means the
  // enumeration never reached the browser.
  const named = statuses.filter(s => /\b(up|down|dormant|testing|notPresent|running|active)\b/i.test(s));
  expect(named.length,
    `every status tile is numeric or blank: ${statuses.slice(0, 12).join(' | ')}`).toBeGreaterThan(0);
});

test('a value with a declared unit carries it into the axis', async ({ page }) => {
  // CISCO-PROCESS-MIB declares kilo-bytes on its memory columns and percent on
  // the CPU ones. A number with no unit is not a reading, it is a number.
  await openDevice(page, 1, 'CISCO-PROCESS-MIB');

  const units = await page.evaluate(() => {
    const app = (window as any).app;
    const found: { id: string; unit: string }[] = [];
    for (const [id, chart] of Object.entries<any>(app.charts || {})) {
      if (!chart?._hasData) continue;
      const option = chart.getOption();
      const unit = option?.yAxis?.[0]?.name || '';
      if (unit) found.push({ id, unit });
    }
    return found;
  });

  expect(units.length, 'no chart on this MIB declared a unit on its axis').toBeGreaterThan(0);
});
