import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// What actually renders, device by device.
//
// The API reports a panel count and whether each panel names a metric that
// exists. Neither says the panel drew anything: a card can carry the right
// metric name, subscribe to it, and still show "Waiting for data…" forever
// because the widget type is wrong for the value, the value never reaches the
// chart, or the series is there under a shape the renderer does not read.
//
// This walks every device's dashboard in a real browser and reports what each
// card ended up as. It is a diagnostic first and a regression second: the
// assertions at the end are deliberately about the fleet, not about one panel,
// so it fails when rendering breaks rather than when a device changes.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';
const STATIC = path.resolve(__dirname, '../../../internal/webui/static');

// Route interception drops the metadata Chrome's Local Network Access check
// needs, and every /api call from an intercepted page is then blocked as
// cross-origin — the page loads and every fetch fails.
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

// serveWorkingTree runs the checked-out UI against the deployed API, so a fix
// can be seen before it is built into an image.
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

type CardState = {
  id: string;
  state: 'plotted' | 'waiting' | 'nodata' | 'error' | 'unknown';
  title: string;
};

// auditDevice opens one device and classifies every card on every MIB tab.
async function auditDevice(page: Page, deviceId: number) {
  await openDeviceById(page, deviceId);

  // Wait for the page to actually BE on this device before reading anything
  // off it. A fixed pause was enough while switching device took seconds; the
  // dashboard is fetched in one request now, and the read landed on the
  // previous device's markup -- so every row of the report named the device
  // before it, and the first read found an empty page.
  await page.waitForFunction(id => {
    const app = (window as any).app;
    if (String(app.currentDashboardDevice) !== String(id)) return false;
    const held = app.deviceDashboards?.[id];
    if (!held) return false;
    return document.querySelectorAll('#mibTabList .tab-button').length > 0;
  }, deviceId, { timeout: 60000 });
  await page.waitForTimeout(2000);

  // Visit each MIB tab: cards on a hidden tab never render.
  const tabs = await page.locator('#mibTabsContainer [data-mib], #mibTabsContainer .mib-tab').all();
  for (const tab of tabs) {
    await tab.click().catch(() => {});
    await page.waitForTimeout(2500);
    // Scrolling builds the deferred charts.
    for (let i = 0; i < 4; i++) {
      await page.mouse.wheel(0, 1400);
      await page.waitForTimeout(400);
    }
  }

  // A card that has just been built is legitimately "waiting": its no-data
  // deadline is 30s away, and a row the device does not populate only reads as
  // empty once that lands. Classifying before then reports a verdict the app
  // has not reached yet, so settle first -- but only as long as it takes.
  await page.waitForFunction(() => {
    const cards = Array.from(document.querySelectorAll('.panel'));
    return !cards.some(card =>
      /waiting for data/i.test(card.textContent || '') && !card.classList.contains('is-nodata'));
  }, undefined, { timeout: 45000 }).catch(() => { /* report whatever is left */ });

  return page.evaluate(() => {
    const app = (window as any).app;
    const out: CardState[] = [];
    for (const el of Array.from(document.querySelectorAll('.chart-canvas'))) {
      const id = (el as HTMLElement).id;
      const card = el.closest('.panel') as HTMLElement | null;
      const chart = app.charts?.[id];
      const title = card?.querySelector('.panel-title')?.textContent?.trim() || id;

      let state: CardState['state'] = 'unknown';
      if (card?.classList.contains('is-nodata')) state = 'nodata';
      else if (card?.classList.contains('is-error')) state = 'error';
      else if (chart && chart._hasData) state = 'plotted';
      else if (card?.textContent?.match(/waiting for data/i)) state = 'waiting';

      out.push({ id, state, title });
    }
    // Status tiles are cards too, and they carry text rather than a plot.
    const tiles = Array.from(document.querySelectorAll('.status-value')).map(el => {
      const tile = el as HTMLElement;
      const card = tile.closest('.panel');
      let state: CardState['state'] = 'plotted';
      // A tile that has never had a sample carries data-waiting until its
      // deadline, and is marked is-nodata after it.
      if (card?.classList.contains('is-nodata')) state = 'nodata';
      else if (tile.dataset.waiting) state = 'waiting';
      return {
        id: tile.id,
        state,
        title: card?.querySelector('.panel-title')?.textContent?.trim()
          || (tile.textContent || '').slice(0, 40),
      };
    });
    return [...out, ...tiles];
  });
}

// One device per profile, plus every member of the Supermicro fleet: the BMCs
// share a profile but not their firmware state, and a card that draws on one
// board is not proof it draws on the next.
const ALL_DEVICES: [number, string][] = [
  [1, 'core-01'], [3, 'pdu-03'], [4, 'bastion'],
  [5, 'dist-01'], [9, 'lab-node-1-bmc'], [14, 'lab-node-2-bmc'],
  [15, 'lab-node-3-bmc'], [16, 'lab-node-4-bmc'], [17, 'lab-node-5-bmc'],
];

// AUDIT_DEVICES narrows the run to named devices, which is how a device that
// only fails late in a long session gets examined on its own.
const DEVICES = process.env.AUDIT_DEVICES
  ? ALL_DEVICES.filter(([, name]) => process.env.AUDIT_DEVICES!.split(',').includes(name))
  : ALL_DEVICES;

test('every device dashboard draws its cards, with none left waiting', async ({ page }) => {
  test.setTimeout(1_200_000);

  const failures: string[] = [];
  const report: string[] = [];

  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* private mode */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  for (const [id, name] of DEVICES) {
    const cards = await auditDevice(page, id);
    const by = (s: string) => cards.filter(c => c.state === s).length;

    report.push(
      `${name.padEnd(14)} cards=${String(cards.length).padStart(4)}  ` +
      `plotted=${String(by('plotted')).padStart(4)}  waiting=${String(by('waiting')).padStart(4)}  ` +
      `nodata=${String(by('nodata')).padStart(4)}  error=${String(by('error')).padStart(3)}  ` +
      `unknown=${String(by('unknown')).padStart(4)}`);

    const stuck = cards.filter(c => c.state === 'waiting');
    if (stuck.length > 0) {
      report.push(`   still waiting: ${stuck.slice(0, 6).map(c => c.title).join(' | ')}`);
      // A card that is still waiting after the dashboard has settled is not
      // slow, it is broken: either it was offered for an object the device does
      // not report, or its query cannot return anything. Both were true here,
      // and between them left 507 cards in this state across five devices.
      failures.push(`${name}: ${stuck.length} cards still waiting for data ` +
        `(e.g. ${stuck[0].title})`);
    }
    const broken = cards.filter(c => c.state === 'error');
    if (broken.length > 0) {
      report.push(`   errored: ${broken.slice(0, 6).map(c => c.title).join(' | ')}`);
      failures.push(`${name}: ${broken.length} cards in an error state`);
    }
    if (cards.length === 0) failures.push(`${name}: no cards rendered at all`);

    // And most of what is offered has to draw. A dashboard of cards that all
    // resolve to "no data" is technically settled and useless.
    const drawn = by('plotted');
    if (cards.length > 0 && drawn < cards.length / 2) {
      failures.push(`${name}: only ${drawn} of ${cards.length} cards drew anything`);
    }
  }

  console.log('\n' + report.join('\n') + '\n');

  expect(failures, failures.join('; ')).toHaveLength(0);
});
