import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// The dashboard lists a device's rows and charts the ones you pick.
//
// It replaced a chart that drew EVERY interface as its own line behind a
// scrolling legend. On a switch with hundreds of ports that is unreadable, and
// slow for the same reason -- the page asked for every series of every panel.
// The owner's words: "we need something that is more user friendly and allows a
// user to quickly select from a list... If you are going to change the UI, make
// sure its easier, not harder, to navigate and use."
//
// So the assertions here are about FINDING A ROW AND READING ITS STATE, not
// about the table existing. A table that renders and cannot be used passes the
// first and fails the ask.
//
// Everything is served locally: assets from the working tree, every API
// response intercepted. Nothing depends on what this host holds.

const BASE = process.env.NETMON_URL || 'http://localhost:8082';
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

// Twelve interfaces: more than the three that open charted, and enough that a
// reader has to find one rather than see them all at once.
const PORTS = Array.from({ length: 12 }, (_, i) => ({
  index: String(i + 1),
  name: `eth${i + 1}`,
}));

const OCTETS = {
  id: 'IF-MIB_snmp_ifHCInOctets', title: 'IF-MIB - ifHCInOctets', type: 'line',
  metric: 'snmp_ifHCInOctets', query: 'snmp_ifHCInOctets{device="dist-01"}',
  module: 'IF-MIB', object: 'ifHCInOctets', kind: 'gauge', unit: 'octets',
  description: 'Octets received.', index_labels: ['ifindex'], series_labels: ['ifname'],
};

const STATUS = {
  id: 'IF-MIB_snmp_ifOperStatus', title: 'IF-MIB - ifOperStatus', type: 'line',
  metric: 'snmp_ifOperStatus', query: 'snmp_ifOperStatus{device="dist-01"}',
  module: 'IF-MIB', object: 'ifOperStatus', kind: 'gauge', description: 'Operational state.',
  index_labels: ['ifindex'], series_labels: ['ifname'],
};

const TABLE = {
  id: 'IF-MIBifindex',
  module: 'IF-MIB',
  index_labels: ['ifindex'],
  columns: [
    { panel_id: OCTETS.id, metric: OCTETS.metric, kind: 'gauge', varies: true, default: true },
    { panel_id: STATUS.id, metric: STATUS.metric, kind: 'gauge', varies: true, default: true },
    // A column every row agrees about: summarized, not dropped.
    {
      panel_id: 'IF-MIB_snmp_ifMtu', metric: 'snmp_ifMtu', kind: 'gauge',
      varies: false, value: '1500', default: false,
    },
  ],
};

async function stubDashboard(page: Page) {
  await page.route(url => url.pathname === '/api/devices' && url.search === '',
    route => route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ devices: [DEVICE] }),
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
      panels: [OCTETS, STATUS], tables: [TABLE],
      mibs: [{ mib: 'IF-MIB', reporting: 2, charts: 2 }], warnings: [],
    }),
  }));
  await page.route(url => url.pathname === '/api/metrics/query', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify([]),
  }));
}

async function openDashboard(page: Page) {
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  // The device has to be IN the selector before it can be opened: setting
  // select.value to an option that does not exist yet is a silent no-op.
  await page.waitForFunction(() => {
    const select = document.getElementById('dashboardDeviceSelect') as HTMLSelectElement | null;
    return !!select && [...select.options].some(o => o.value === '4');
  }, { timeout: 20000 });
  await page.evaluate(() => (window as any).app.openFleetDevice(4));

  // And wait for the pane to have rendered before anything is fed into it.
  // Feeding a dashboard that has not registered its panels yet throws on an
  // empty subscription map, which is a test that has not started rather than a
  // product that is broken -- a distinction worth making explicitly, since the
  // two look identical from the failure message.
  try {
    await page.waitForSelector('.row-table', { timeout: 8000 });
  } catch (error) {
    const state = await page.evaluate(() => ({
      chartsGrids: document.querySelectorAll('.charts-grid').length,
      panelCards: document.querySelectorAll('[id^="panel-"]').length,
      rowTables: document.querySelectorAll('.row-table').length,
      paneTables: [...((window as any).app.paneTables || new Map()).keys()],
      tablesSeen: [...((window as any).app.paneTables || new Map()).values()]
        .map((p: any) => (p.tables || []).length),
    }));
    throw new Error('no .row-table rendered. ' + JSON.stringify(state));
  }
  await page.waitForFunction(
    () => ((window as any).app.activeSubscriptions?.size ?? 0) > 0,
    { timeout: 20000 });
}

// feed delivers one tick of readings for both columns, the way the stream does.
async function feed(page: Page, ports: typeof PORTS) {
  await page.evaluate((rows) => {
    const app = (window as any).app;
    const [subscription] = [...app.activeSubscriptions.values()];
    for (const target of subscription.objects.values()) {
      const metrics = rows.map((port: { index: string; name: string }) => ({
        metric: { ifindex: port.index, ifname: port.name, device: 'dist-01' },
        value: target.panel.metric === 'snmp_ifOperStatus' ? 1 : 1000 + Number(port.index),
      }));
      app.updatePanel(target, metrics, Math.floor(Date.now() / 1000));
    }
  }, ports);
}

test('every row is listed, in a name order a reader can scan', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  const names = page.locator('.row-table-grid tbody th');
  await expect(names).toHaveCount(PORTS.length);

  // eth10 after eth9, not after eth1. A plain string sort gets this wrong, and
  // getting it wrong is what "not sorted alphabetically" looked like.
  await expect(names).toHaveText(
    ['eth1', 'eth2', 'eth3', 'eth4', 'eth5', 'eth6',
      'eth7', 'eth8', 'eth9', 'eth10', 'eth11', 'eth12']);
});

test('a reader can find one row without scrolling past the others', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  await page.locator('.row-table-search').fill('eth11');

  const names = page.locator('.row-table-grid tbody th');
  await expect(names).toHaveCount(1);
  await expect(names).toHaveText(['eth11']);
  await expect(page.locator('.row-table-count')).toContainText('1 of 12 rows');
});

test('the chart draws the picked rows, not every row', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  // The complaint, in one assertion: twelve rows of data held, and the plot
  // carrying the three that were picked rather than a line per interface
  // behind a scrolling legend.
  const drawn = await page.evaluate(() => {
    const app = (window as any).app;
    for (const chart of Object.values(app.charts || {}) as { _panelSeries?: Map<string, unknown>; getOption: () => { series?: unknown[] } }[]) {
      if (chart && chart._panelSeries && chart._panelSeries.size > 0) {
        const option = chart.getOption();
        return { held: chart._panelSeries.size, plotted: (option.series || []).length };
      }
    }
    return { held: 0, plotted: 0 };
  });

  expect(drawn.held,
    'the chart is not holding every row, so this test is not measuring the filter')
    .toBe(PORTS.length);
  expect(drawn.plotted,
    `the plot carries ${drawn.plotted} lines for ${drawn.held} rows; drawing every `
    + 'row behind a scrolling legend is the interface being replaced')
    .toBe(3);
});

test('picking more rows is bounded, and says so rather than ignoring the click', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  // Three are charted already; pick past the bound.
  //
  // click() rather than check(): check() asserts the box ends up checked, which
  // is precisely what the bound prevents, so it fails on the behaviour under
  // test rather than observing it.
  for (let i = 0; i < 6; i++) {
    await page.locator('.row-table-grid tbody input[type="checkbox"]:not(:checked)')
      .first().click({ force: true });
  }

  const charted = await page.evaluate(() => {
    const app = (window as any).app;
    const [selected] = [...app.tableSelection.values()];
    return selected.size;
  });
  expect(charted,
    'the bound did not hold, so "select all" can rebuild the hairball through '
    + 'the new control').toBe(8);

  // Refusing silently reads as a broken checkbox.
  await expect(page.locator('#notificationContainer')).toContainText('limited to 8 rows');
});

test('a value every row shares is stated once, not repeated down a column', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  // It is not a column...
  await expect(page.locator('.row-table-grid thead')).not.toContainText('ifMtu');
  // ...and it has not vanished either, which would make its absence carry
  // meaning: uniform, deselected and not-reported would all look the same.
  await expect(page.locator('.row-table-summary')).toContainText('ifMtu 1500');
  await expect(page.locator('.row-table-summary')).toContainText('All rows');
});
