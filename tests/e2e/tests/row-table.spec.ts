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
const DEFAULT_SELECTED = 3;

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

// A text panel: its reading is words in a label, and its sample is 1.
const ALIAS = {
  id: 'IF-MIB_snmp_ifalias_info', title: 'IF-MIB - ifAlias', type: 'text',
  metric: 'snmp_ifalias_info', query: 'snmp_ifalias_info{device="dist-01"}',
  module: 'IF-MIB', object: 'ifAlias', kind: 'string', description: 'Alias.',
  index_labels: ['ifindex'], series_labels: ['ifname'], value_label: 'value',
};

// A state panel: decoded from a number to a word.
const OPER = {
  id: 'IF-MIB_snmp_ifOperStatus', title: 'IF-MIB - ifOperStatus', type: 'state',
  metric: 'snmp_ifOperStatus', query: 'snmp_ifOperStatus{device="dist-01"}',
  module: 'IF-MIB', object: 'ifOperStatus', kind: 'enum', description: 'State.',
  index_labels: ['ifindex'], series_labels: ['ifname'], enum: { '1': 'up', '2': 'down' },
};

const TABLE = {
  id: 'IF-MIBifindex',
  module: 'IF-MIB',
  index_labels: ['ifindex'],
  columns: [
    { panel_id: OCTETS.id, metric: OCTETS.metric, kind: 'gauge', varies: true, default: true },
    { panel_id: STATUS.id, metric: STATUS.metric, kind: 'gauge', varies: true, default: true },
    { panel_id: ALIAS.id, metric: ALIAS.metric, kind: 'string', varies: true, default: true },
    { panel_id: OPER.id, metric: OPER.metric, kind: 'enum', varies: true, default: true },
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
      panels: [OCTETS, STATUS, ALIAS, OPER], tables: [TABLE],
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
      const metrics = rows.map((port: { index: string; name: string }) => {
        const labels: Record<string, string> = {
          ifindex: port.index, ifname: port.name, device: 'dist-01',
        };
        // An info metric's sample is 1 and its words are in a label.
        if (target.panel.value_label) labels[target.panel.value_label] = 'uplink ' + port.index;
        return {
          metric: labels,
          value: target.panel.metric === 'snmp_ifOperStatus' ? 1 : 1000 + Number(port.index),
        };
      });
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

test('a column whose reading is words shows the words, not a dash', async ({ page }) => {
  // Observed on monitor-01 after the first deploy: IFALIAS, IFDESCR, IFADMINSTATUS
  // and IFCONNECTORPRESENT showed "—" on every row, while the cards above the
  // table displayed the same values. The data was arriving; it stopped at the
  // tile.
  //
  // updatePanel returned early for state and text panels -- calling
  // updatePanelTile and returning -- and the table was fed AFTER that branch.
  // So the two column kinds a reader most needs to navigate by (a port's name
  // and whether it is up) were the two that never reached the table.
  //
  // The fixture that missed it had two panels, both numeric. Every panel type
  // being the same kind is a fixture too regular to tell "feeds the table"
  // from "feeds the table when it happens to be a chart".
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  const firstRow = page.locator('.row-table-grid tbody tr').first();

  await expect(firstRow, 'the text column shows a dash; its reading is in a label and '
    + 'reached the tile instead of the table').toContainText('uplink 1');
  await expect(firstRow, 'the state column shows a dash; a state is a column a reader '
    + 'navigates by, and it never reached the table').not.toContainText('—');
});

// plottedFor reports what one chart HOLDS against what it PLOTS. Both numbers,
// because "3 plotted" proves nothing if only 3 are held, and "40 held" proves
// nothing about what is on screen.
async function plottedFor(page: Page) {
  return page.evaluate(() => {
    const app = (window as any).app;
    const out: { held: number; plotted: number }[] = [];
    for (const chart of Object.values(app.charts || {}) as any[]) {
      if (chart && chart._panelSeries && chart._panelSeries.size > 0) {
        out.push({ held: chart._panelSeries.size, plotted: (chart.getOption().series || []).length });
      }
    }
    return out;
  });
}

test('unticking every row plots nothing, and the table says so', async ({ page }) => {
  // The filter carried "|| charted.size === 0", so an empty selection drew
  // EVERY row rather than none -- absent meaning unfiltered, hand-written into
  // the filter whose purpose is to stop drawing everything.
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  const before = await plottedFor(page);
  expect(before.some(c => c.plotted === DEFAULT_SELECTED), 'nothing opened charted, so '
    + 'this test cannot observe the selection being emptied').toBe(true);

  for (let i = 0; i < DEFAULT_SELECTED; i++) {
    await page.locator('.row-table-grid tbody input[type="checkbox"]:checked').first()
      .click({ force: true });
  }

  const after = await plottedFor(page);
  for (const chart of after) {
    expect(chart.plotted, `a chart plots ${chart.plotted} of ${chart.held} rows with `
      + 'nothing ticked; an empty selection is a choice and drawing every row '
      + 'answers it with the opposite').toBe(0);
    expect(chart.held, 'the rows were discarded rather than merely unplotted; '
      + 'ticking one again would have nothing to draw').toBeGreaterThan(0);
  }

  await expect(page.locator('.row-table-count')).toContainText('none charted');
  await expect(page.locator('.row-table-count')).toContainText('tick a row');
});

test('a table whose columns are all summarized still lists its rows', async ({ page }) => {
  // rowTableMarkup returned '' when no column was default, so there was no
  // section, no rows, nothing to tick -- and because the selection was seeded
  // by rendering, that table's charts then drew every row they held.
  await serveWorkingTree(page);
  await page.route(url => url.pathname === '/api/devices' && url.search === '',
    route => route.fulfill({ status: 200, contentType: 'application/json',
      body: JSON.stringify({ devices: [DEVICE] }) }));
  await page.route(url => url.pathname === '/api/devices/4', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify(DEVICE) }));
  await page.route(url => url.pathname === '/api/devices/4/mibs', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({
      direct_mibs: [{ id: 7, name: 'IF-MIB', object_count: 40, assignment: { enabled: true } }],
      all_mibs: [{ id: 7, name: 'IF-MIB', object_count: 40, assignment: { enabled: true } }],
    }) }));
  await page.route(url => url.pathname === '/api/metrics/query', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify([]) }));
  // Every column uniform, so none is a default column.
  await page.route(url => url.pathname === '/api/devices/4/dashboard', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({
      device: 'dist-01', title: 'dist-01 - IF-MIB',
      panels: [OCTETS],
      tables: [{
        id: 'IF-MIBifindex', module: 'IF-MIB', index_labels: ['ifindex'],
        columns: [{ panel_id: OCTETS.id, metric: OCTETS.metric, kind: 'gauge',
          varies: false, value: '1000', default: false }],
      }],
      mibs: [{ mib: 'IF-MIB', reporting: 1, charts: 1 }], warnings: [],
    }) }));

  await page.evaluate(() => {}).catch(() => {});
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => {
    const sel = document.getElementById('dashboardDeviceSelect') as HTMLSelectElement | null;
    return !!sel && [...sel.options].some(o => o.value === '4');
  }, { timeout: 20000 });
  await page.evaluate(() => (window as any).app.openFleetDevice(4));
  await page.waitForSelector('.row-table', { timeout: 20000 });
  // feed() reads the installed subscription, which subscribeToPanels creates
  // after renderPanels' pause; wait for it as openDashboard does.
  await page.waitForFunction(
    () => ((window as any).app.activeSubscriptions?.size ?? 0) > 0,
    { timeout: 20000 });
  await feed(page, PORTS);

  await expect(page.locator('.row-table-grid tbody th'),
    'the table rendered no rows because none of its columns was a default, so there '
    + 'is nothing to tick and its charts filter nothing').toHaveCount(PORTS.length);
});

test('a table-bound chart names its lines without a pager', async ({ page }) => {
  // The "◀ 1/20 ▶" pager is what the owner reported as unusable: on a switch it
  // is the only way to tell hundreds of lines apart, and reaching the last one
  // takes longer than the question is worth.
  //
  // It exists because a panel could hold forty rows. A panel bound to a table
  // now draws at most MAX_SELECTED_ROWS, which fits as plain names — so the
  // pager should be gone from exactly these charts, and only these.
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  const legend = await page.evaluate(() => {
    const app = (window as any).app;
    for (const chart of Object.values(app.charts || {}) as any[]) {
      if (chart && chart._panelSeries && chart._panelSeries.size > 0) {
        const opt = chart.getOption();
        const first = (opt.legend || [])[0] || {};
        return {
          held: chart._panelSeries.size,
          plotted: (opt.series || []).length,
          type: first.type ?? null,
          shown: first.show !== false,
        };
      }
    }
    return null;
  });

  expect(legend, 'no chart held any series, so there is no legend to inspect').not.toBeNull();
  expect(legend!.held, 'the chart is not holding more rows than it plots, so this '
    + 'test is not measuring a chart the table is filtering').toBeGreaterThan(legend!.plotted);
  expect(legend!.shown, 'the legend is hidden, so the lines have no names at all — '
    + 'which is not the fix, it is the other failure').toBe(true);
  expect(legend!.type, 'the chart plots a handful of rows and still carries the '
    + 'scrolling pager; the pager exists for panels nothing is filtering')
    .not.toBe('scroll');
});

test('searching a port alias charts that port and says how many matched', async ({ page }) => {
  // The owner asked for search across historical data on 2026-09-02 and it was
  // never built. Viewing history worked; FINDING a series did not — a reader
  // who knows a port is called "uplink 7" had no path from that string to its
  // chart.
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  await page.locator('#seriesSearch').fill('uplink 7');

  const charted = await page.evaluate(() => {
    const app = (window as any).app;
    const [selected] = [...app.tableSelection.values()];
    return [...selected];
  });
  expect(charted, 'the search matched but charted something other than the one row '
    + 'whose alias was typed').toEqual(['7']);

  await expect(page.locator('#seriesSearchCount')).toContainText('1 row');
});

test('a search that matches nothing says so, rather than going blank', async ({ page }) => {
  // A blank count after a search reads as the search not having run — the
  // failure this codebase has spent two days removing, in a text field.
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  await page.locator('#seriesSearch').fill('nothing-matches-this');

  await expect(page.locator('#seriesSearchCount')).toContainText('No reading matches');
  await expect(page.locator('#seriesSearchCount')).toContainText('nothing-matches-this');
});

test('clearing the search restores the default selection', async ({ page }) => {
  // Otherwise clearing a search silently keeps its result, and the charts go on
  // showing a filter the reader has just removed.
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  await page.locator('#seriesSearch').fill('uplink 7');
  await page.locator('#seriesSearch').fill('');

  const charted = await page.evaluate(() => {
    const app = (window as any).app;
    const [selected] = [...app.tableSelection.values()];
    return selected.size;
  });
  expect(charted, 'clearing the box left the search result charted, so the reader '
    + 'has no way back to where they started').toBe(DEFAULT_SELECTED);
});

test('a broad search is bounded by the same ceiling as picking by hand', async ({ page }) => {
  // "uplink" matches every port. Charting all of them would rebuild the
  // hairball the table exists to replace, through a different door — and the
  // ceiling a reader is held to by hand has to hold for a search too, or the
  // bound is a property of the control rather than of the chart.
  await serveWorkingTree(page);
  await stubDashboard(page);
  await openDashboard(page);
  await feed(page, PORTS);

  await page.locator('#seriesSearch').fill('uplink');

  const charted = await page.evaluate(() => {
    const app = (window as any).app;
    const [selected] = [...app.tableSelection.values()];
    return selected.size;
  });
  expect(charted, `a search charted ${charted} rows; the ceiling is 8 whether the `
    + 'rows are picked by hand or by matching').toBe(8);

  // And it must SAY that it charted fewer than it found, or the reader believes
  // they are looking at every match.
  await expect(page.locator('#seriesSearchCount')).toContainText(`${PORTS.length} rows`);
  await expect(page.locator('#seriesSearchCount')).toContainText('charting 8');
});
