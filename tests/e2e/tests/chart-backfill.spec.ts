import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// A chart opens with history: points stamped before page load prove backfill. All API responses are intercepted.

import { BASE } from '../target';
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

const DEVICE = {
  id: 4, name: 'dist-01', address: '192.0.2.14',
  snmp_version: 'v2c', community: 'public', port: 161,
};

const PANEL = {
  id: 'IF-MIB_snmp_ifHCInOctets',
  title: 'IF-MIB - ifHCInOctets',
  type: 'line',
  metric: 'snmp_ifHCInOctets',
  query: 'snmp_ifHCInOctets{device="dist-01"}',
  module: 'IF-MIB',
  object: 'ifHCInOctets',
  kind: 'gauge',
  unit: 'octets',
  description: 'Octets received.',
  index_labels: ['ifindex'],
};

// HISTORY_POINTS spans well over an hour, which no ticker could have produced
// in the seconds this test runs. Timestamps are seconds, as Prometheus returns
// them, and are computed relative to the moment the fixture is built so they
// are always genuinely in the past.
function historyResponse(now: number) {
  const values: [number, string][] = [];
  for (let i = 60; i >= 1; i--) {
    values.push([Math.floor(now / 1000) - i * 60, String(1000 + i)]);
  }
  return [{ metric: 'snmp_ifHCInOctets', labels: { ifindex: '1', device: 'dist-01' }, values }];
}

async function stubDashboard(page: Page, opts: { history: boolean }) {
  await page.route(url => url.pathname === '/api/devices' && url.search === '',
    route => route.fulfill({
      status: 200, contentType: 'application/json', body: JSON.stringify({ devices: [DEVICE] }),
    }));
  await page.route(url => url.pathname === '/api/devices/4', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify(DEVICE),
  }));
  await page.route(url => url.pathname === '/api/devices/4/mibs', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({
      direct_mibs: [{ id: 7, name: 'IF-MIB', object_count: 40, assignment: { enabled: true } }],
      all_mibs: [{ id: 7, name: 'IF-MIB', object_count: 40, assignment: { enabled: true } }],
    }),
  }));
  await page.route(url => url.pathname === '/api/devices/4/dashboard', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({
      device: 'dist-01', title: 'dist-01 - IF-MIB',
      panels: [PANEL], mibs: [{ mib: 'IF-MIB', reporting: 1, charts: 1 }], warnings: [],
    }),
  }));

  // The range read. Answering it with nothing is how this test asks "what does
  // the chart look like when there is no history to have?" -- which is what the
  // page did for every chart before this change.
  await page.route(url => url.pathname === '/api/metrics/query', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify(opts.history ? historyResponse(Date.now()) : []),
  }));
}

// plottedTimestamps reads the points the chart is actually holding, which is
// what the reader sees -- not what the fixture offered.
async function plottedTimestamps(page: Page): Promise<number[]> {
  return page.evaluate(() => {
    const app = (window as any).app;
    for (const chart of Object.values(app.charts || {}) as any[]) {
      if (chart && chart._panelSeries && chart._panelSeries.size > 0) {
        const [row] = [...chart._panelSeries.values()];
        return row.points.map((point: [number, number]) => point[0]);
      }
    }
    return [];
  });
}

// waitForPoints waits for the chart to hold anything, and on timeout says WHY
// it does not: an ECharts instance that was never constructed is a different
// fault from one that was built and left empty, and "no points" alone cannot
// tell them apart.
async function waitForPoints(page: Page): Promise<number[]> {
  const deadline = Date.now() + 20000;
  for (;;) {
    const points = await plottedTimestamps(page);
    if (points.length > 0) return points;
    if (Date.now() > deadline) {
      const state = await page.evaluate(() => {
        const app = (window as any).app;
        return {
          chartsBuilt: Object.keys(app.charts || {}),
          pendingCharts: [...(app.pendingCharts ? app.pendingCharts.keys() : [])],
          pendingBackfill: [...(app.pendingBackfill ? app.pendingBackfill.keys() : [])],
          // Panel elements are id="panel-<device>-<mib>-<slug>". An earlier
        // version of this probe looked for [id^="chart-"], matched nothing on
        // every run, and reported cards: [] -- which was read as "no card was
        // rendered" and sent the diagnosis a long way in the wrong direction.
        // A selector that resolves to nothing returns silence, and silence is
        // indistinguishable from absence.
        cards: [...document.querySelectorAll('[id^="panel-"]')].map(e => e.id),
        };
      });
      throw new Error('the chart never plotted anything. ' + JSON.stringify(state));
    }
    await page.waitForTimeout(250);
  }
}

async function openDashboard(page: Page) {
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });

  // Wait for the device to be in the selector before opening it.
  await page.waitForFunction(() => {
    const select = document.getElementById('dashboardDeviceSelect') as HTMLSelectElement | null;
    return !!select && [...select.options].some(option => option.value === '4');
  }, { timeout: 20000 });

  await page.evaluate(() => (window as any).app.openFleetDevice(4));
}

test('a chart opens with history rather than accumulating it', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDashboard(page, { history: true });

  // Captured before page load: backfilled points are stamped before this.
  const openedAt = Date.now();
  await openDashboard(page);

  const points = await waitForPoints(page);
  const older = points.filter(at => at < openedAt);
  expect(older.length,
    `the chart holds ${points.length} point(s) and none is older than the page load, `
    + 'so every one of them was appended by the live ticker -- which is what the '
    + 'chart did before backfill existed, and what it would still do if the range '
    + 'read were never issued').toBeGreaterThan(1);

  // The history must span time, not be one point.
  const span = Math.max(...older) - Math.min(...older);
  expect(span, 'the backfilled points cover no time, so the chart still has nothing to draw a line through')
    .toBeGreaterThan(5 * 60 * 1000);
});

test('a device with no history yet leaves the page working, not broken', async ({ page }) => {
  // An empty history must not throw or invent points.
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(String(error)));

  await serveWorkingTree(page);
  await stubDashboard(page, { history: false });

  const openedAt = Date.now();
  await openDashboard(page);
  await expect(page.locator('#mibTabsContainer')).toContainText('IF-MIB', { timeout: 20000 });

  expect(errors, 'an empty range response threw, and a chart that cannot fetch history '
    + 'must not be able to break the page').toEqual([]);

  const points = await plottedTimestamps(page);
  expect(points.filter(at => at < openedAt),
    'points older than the page load exist although the range read returned nothing '
    + 'to backfill, so they were invented').toEqual([]);
});

test('history is merged behind live points rather than replacing them', async ({ page }) => {
  // Backfill must merge with live points already plotted, not replace them.
  await serveWorkingTree(page);
  await stubDashboard(page, { history: true });
  await openDashboard(page);

  await waitForPoints(page);

  const survived = await page.evaluate(async () => {
    const app = (window as any).app;
    const [subscription] = [...app.activeSubscriptions.values()];
    const [target] = [...subscription.objects.values()];
    const chart = app.chartFor(target.elementId);

    // A live reading inside the history window, so the merge must sort.
    const live = Date.now() - 30 * 60 * 1000 + 1234;
    const [row] = [...chart._panelSeries.values()];
    row.points.push([live, 4242]);

    await app.backfillPanel(target);

    const [after] = [...chart._panelSeries.values()];
    return {
      keptLive: after.points.some((point: [number, number]) => point[0] === live),
      historyPoints: after.points.filter((point: [number, number]) => point[0] < Date.now()).length,
      ascending: after.points.every((point: [number, number], i: number, all: [number, number][]) =>
        i === 0 || all[i - 1][0] <= point[0]),
    };
  });

  expect(survived.keptLive,
    'the live point was dropped: backfill replaced the series instead of merging into it, '
    + 'so fetching the oldest readings discarded the newest').toBe(true);
  expect(survived.historyPoints,
    'the history did not survive alongside the live point').toBeGreaterThan(1);
  expect(survived.ascending,
    'the merged points are not in time order, so the line doubles back on itself').toBe(true);
});

test('an empty feed keeps the line and says the reading is not current', async ({ page }) => {
  // A metric with no current reading keeps its plotted history and is shown as not current.
  await serveWorkingTree(page);
  await stubDashboard(page, { history: true });
  await openDashboard(page);

  const before = await waitForPoints(page);
  expect(before.length, 'nothing was plotted, so this test cannot observe a loss')
    .toBeGreaterThan(1);

  // The stream reports the query has no current reading.
  await page.evaluate(() => {
    const app = (window as any).app;
    const [subscription] = [...app.activeSubscriptions.values()];
    const [target] = [...subscription.objects.values()];
    app.clearPanel(target);
  });

  const after = await plottedTimestamps(page);
  expect(after, 'the history was discarded; a reading stopping is not a reason to '
    + 'destroy the record of what it used to be').toEqual(before);

  // And the card must SAY so, visibly, in words a reader can name.
  const banner = page.locator('.chart-stale:not([hidden]) .chart-stale-text');
  await expect(banner, 'the line was kept with nothing saying the feed is empty, so '
    + 'the chart now reads as current when it is not').toBeVisible();
  await expect(banner).toContainText('no current reading');
  await expect(banner).toContainText('Last known');

  // And it must come OFF when readings resume. A chart still captioned "no
  // current reading" while it is being updated is the inverse defect -- health
  // rendered as absence -- and it teaches a reader to ignore the label, which
  // costs the real case later.
  await page.evaluate(() => {
    const app = (window as any).app;
    const [subscription] = [...app.activeSubscriptions.values()];
    const [target] = [...subscription.objects.values()];
    app.updatePanelChart(target, [{ metric: { ifindex: '1', device: 'dist-01' }, value: 7 }], Date.now());
  });

  await expect(page.locator('.chart-stale:not([hidden])'),
    'the reading came back and the card still says there is none').toHaveCount(0);
});
