import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// The dashboard's landing view. It used to be a device picker over an empty
// frame; it is now every configured device's health, read from
// /api/devices/status in one request, with the poller's own state word beside
// each device.
//
// These run the working tree over the live API by page.route(), so they pass
// against a deployment that has not shipped the files yet and keep passing once
// it has. Nothing here mutates server state: every case is either read-only
// against the real fleet or a synthesised payload confined to one page.

import { BASE } from '../target';
import { SEEDED_DEVICE } from '../stack';
const STATIC = path.resolve(__dirname, '../../../internal/webui/static');
const STATUS_URL = `${BASE}/api/devices/status`;

// Route interception drops the metadata Chrome's Local Network Access check
// needs, and every /api call from an intercepted page is then blocked as
// cross-origin. The rest of the launch options match the project config.
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

function consoleErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('console', msg => { if (msg.type() === 'error') errors.push(msg.text()); });
  page.on('pageerror', err => errors.push(`pageerror: ${err.message}`));
  return errors;
}

// openOverview lands on the dashboard with nothing selected, which is the state
// the app starts in unless a previous session stored a device.
async function openOverview(page: Page) {
  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* private mode */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });
}

type FleetEntry = {
  id: number; name: string; address: string; state: string;
  error?: string;
  status?: Record<string, unknown>;
};

// serveFleet answers /api/devices/status with a payload of our own, so a state
// the live stack is not currently in can still be driven through the UI.
//
// It narrows and pages the way the server does, because the overview now asks
// for a page rather than the whole fleet: a fixture that ignored limit, offset,
// search and state would answer questions the browser did not ask and would
// hide a paging defect rather than expose one. The summary counts every device
// regardless of the filters, which is the contract the chips depend on.
async function serveFleet(page: Page, devices: FleetEntry[]) {
  await page.route('**/api/devices/status*', route => {
    const params = new URL(route.request().url()).searchParams;
    const search = (params.get('search') || '').toLowerCase();
    const state = params.get('state') || '';
    const limit = Number(params.get('limit') || 0);
    const offset = Number(params.get('offset') || 0);

    const summary: Record<string, number> = {};
    for (const device of devices) {
      summary[device.state] = (summary[device.state] || 0) + 1;
    }

    const matching = devices.filter(device => {
      if (state && device.state !== state) return false;
      if (!search) return true;
      return `${device.name} ${device.address ?? ''} ${device.state}`.toLowerCase().includes(search);
    });
    const page_ = limit ? matching.slice(offset, offset + limit) : matching;

    return route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        devices: page_, total: matching.length, configured: devices.length, summary,
      }),
    });
  });
}

// rowFor returns the row of the named device.
function rowFor(page: Page, name: string) {
  return page.locator('.fleet-row').filter({ has: page.locator('.fleet-open', { hasText: new RegExp(`^${name}$`) }) });
}

// ---------------------------------------------------------------------------
// The real fleet
// ---------------------------------------------------------------------------

test.describe('fleet overview', () => {
  test('the landing view is the whole fleet, not an empty frame', async ({ page, request }) => {
    const errors = consoleErrors(page);
    const expected = await (await request.get(STATUS_URL)).json();

    await openOverview(page);

    const rows = page.locator('.fleet-row');
    await expect(rows).toHaveCount(expected.devices.length, { timeout: 20000 });

    // The device picker is still there and still says nothing is selected, so
    // this is genuinely the landing state rather than a device that got opened.
    await expect(page.locator('#dashboardDeviceSelect')).toHaveValue('');
    await expect(page.locator('#mibTabsContainer')).toBeHidden();

    // Every configured device is named, in the order the server sent, with no
    // client-side re-sorting.
    const names = await page.locator('.fleet-open').allTextContents();
    expect(names).toEqual(expected.devices.map((d: FleetEntry) => d.name));

    // The old landing view spent most of a 1080-tall window on the words "No
    // device selected". Measure that it no longer does.
    const filled = await page.evaluate(() => {
      const overview = document.getElementById('fleetOverview')!;
      return overview.getBoundingClientRect().bottom / window.innerHeight;
    });
    console.log(`overview fills ${(filled * 100).toFixed(0)}% of the window at ${expected.devices.length} devices`);
    expect(filled, 'the landing view should not be mostly empty').toBeGreaterThan(0.5);

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('each device shows the state the server decided, not one re-derived here', async ({ page, request }) => {
    const expected = await (await request.get(STATUS_URL)).json();
    await openOverview(page);
    await expect(page.locator('.fleet-row').first()).toBeVisible({ timeout: 20000 });

    const labels: Record<string, string> = {
      ok: 'Healthy', failing: 'Failing', unreachable: 'Unreachable',
      never_polled: 'Never polled', not_registered: 'Not registered', unknown: 'Unknown',
    };

    for (const device of expected.devices as FleetEntry[]) {
      const state = rowFor(page, device.name).locator('.fleet-state');
      await expect(state, `${device.name} is ${device.state}`).toHaveText(labels[device.state] ?? device.state);
    }

    // And the counts above the list are those same states counted, not a score.
    const seen = new Map<string, number>();
    for (const device of expected.devices as FleetEntry[]) {
      seen.set(device.state, (seen.get(device.state) || 0) + 1);
    }
    for (const [state, count] of seen) {
      await expect(page.locator(`.fleet-chip[data-fleet-state="${state}"] .fleet-chip-count`))
        .toHaveText(String(count));
    }
    await expect(page.locator('.fleet-chip[data-fleet-state=""] .fleet-chip-count'))
      .toHaveText(String(expected.devices.length));
  });

  test('an unreachable device is presented as unreachable, with the reason one click away', async ({ page }) => {
    // This was written against bastion while bastion was genuinely unreachable, and
    // read the live status to prove it. Then bastion was fixed — it answers v1 —
    // and the test failed for the best possible reason, which is not a reason a
    // test should fail. The state is served instead, so this stays a test of how
    // an unreachable device is presented rather than of whether one exists.
    const bastion: FleetEntry = {
      id: 4, name: 'bastion', address: '198.51.100.14', state: 'unreachable',
      error: 'device 198.51.100.14 answered none of the 47 OIDs polled: '
           + 'request timeout (after 3 retries) on every chunk',
      status: { consecutive_failures: 12 },
    };
    await serveFleet(page, [bastion]);

    await openOverview(page);
    const row = rowFor(page, 'bastion');
    await expect(row.locator('.fleet-state')).toHaveText('Unreachable');

    // The failure count is on the row; the message is clipped to one line.
    const failures = Number((bastion.status as any).consecutive_failures);
    await expect(row.locator('.badge.error')).toContainText(`${failures} failed poll`);

    const trigger = row.locator('.desc-trigger');
    await expect(trigger).toBeVisible();
    const clipped = await trigger.evaluate(el => ({
      shown: el.getBoundingClientRect().width,
      needed: el.scrollWidth,
      lines: getComputedStyle(el).whiteSpace,
    }));
    expect(clipped.lines, 'the default view stays one line').toBe('nowrap');
    expect(clipped.needed, 'the message is longer than the cell').toBeGreaterThan(clipped.shown);

    // ...and the whole of it is in the sheet.
    await trigger.click();
    const sheet = page.locator('#descSheet');
    await expect(sheet).toBeVisible();
    await expect(page.locator('#descSheetTitle')).toHaveText('bastion');
    await expect(page.locator('#descSheetEyebrow')).toHaveText('Unreachable');
    await expect(page.locator('#descSheetBody')).toContainText('answered none of');
    await expect(page.locator('#descSheetMeta')).toContainText(String(failures));

    // Reading the reason is not the same gesture as opening the device: the
    // row is clickable, but the control inside it is not the row.
    await expect(page.locator('#dashboardDeviceSelect')).toHaveValue('');
    await expect(page.locator('#mibTabsContainer')).toBeHidden();

    await page.keyboard.press('Escape');
    await expect(sheet).toBeHidden();

    // The device name is a real button, so the whole row is reachable from the
    // keyboard without a pointer.
    await row.locator('.fleet-open').focus();
    await expect(row.locator('.fleet-open')).toBeFocused();
  });

  test('opening a device from the overview shows its dashboard, and clearing comes back', async ({ page }) => {
    const errors = consoleErrors(page);
    await openOverview(page);

    await rowFor(page, SEEDED_DEVICE).locator('.fleet-open').click();

    await expect(page.locator('#dashboardDeviceSelect')).toHaveValue(/\d+/);
    await expect(page.locator('#mibTabsContainer')).toBeVisible({ timeout: 45000 });
    await expect(page.locator('#mibTabList .tab-button').first()).toBeVisible({ timeout: 45000 });
    // The rebuilt per-device dashboard is untouched: the overview hides rather
    // than sharing the screen with it.
    await expect(page.locator('#dashboardContent')).toBeHidden();

    // The empty option is the way back.
    await page.locator('#dashboardDeviceSelect').selectOption('');
    await expect(page.locator('#fleetOverview')).toBeVisible();
    await expect(page.locator('.fleet-row').first()).toBeVisible({ timeout: 20000 });
    await expect(page.locator('#mibTabsContainer')).toBeHidden();

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  // -------------------------------------------------------------------------
  // The six states, including the three the live stack is not currently in
  // -------------------------------------------------------------------------

  test('all six states are presented distinctly, and none of them reads as healthy', async ({ page }) => {
    const now = Date.now();
    const iso = (offset: number) => new Date(now + offset).toISOString();
    const ZERO = '0001-01-01T00:00:00Z';

    await serveWorkingTree(page);
    await serveFleet(page, [
      {
        id: 1, name: 'state-failing', address: '198.51.100.101', state: 'failing',
        status: {
          name: 'state-failing', connected: false, last_poll_success: false,
          last_poll_time: iso(-45000), next_poll_time: iso(15000),
          last_error: 'poller: device state-failing answered 2 of 4 SNMP operations',
          metrics_collected: 0, consecutive_failures: 2, unreachable: false, poll_interval: '',
        },
      },
      {
        id: 2, name: 'state-never', address: '198.51.100.102', state: 'never_polled',
        status: {
          name: 'state-never', connected: false, last_poll_success: false,
          last_poll_time: ZERO, next_poll_time: ZERO,
          metrics_collected: 0, consecutive_failures: 0, unreachable: false, poll_interval: '',
        },
      },
      {
        id: 3, name: 'state-notreg', address: '198.51.100.103', state: 'not_registered',
        error: 'poller: device "state-notreg" is not registered with the poller',
      },
      {
        id: 4, name: 'state-ok', address: '198.51.100.104', state: 'ok',
        status: {
          name: 'state-ok', connected: true, last_poll_success: true,
          last_poll_time: iso(-9000), next_poll_time: iso(21000),
          metrics_collected: 4242, consecutive_failures: 0, unreachable: false, poll_interval: '',
        },
      },
      {
        id: 5, name: 'state-unknown', address: '198.51.100.105', state: 'unknown',
        error: 'poller: status for device "state-unknown" is unavailable: connection refused',
      },
      {
        id: 6, name: 'state-unreach', address: '198.51.100.106', state: 'unreachable',
        status: {
          name: 'state-unreach', connected: false, last_poll_success: false,
          last_poll_time: iso(-600000), next_poll_time: iso(300000),
          last_error: 'poller: device state-unreach answered none of 4 SNMP operations',
          metrics_collected: 0, consecutive_failures: 8, unreachable: true, poll_interval: '',
        },
      },
    ]);
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.fleet-row')).toHaveCount(6, { timeout: 20000 });

    const wanted: [string, string, string][] = [
      ['state-ok', 'ok', 'Healthy'],
      ['state-failing', 'failing', 'Failing'],
      ['state-unreach', 'unreachable', 'Unreachable'],
      ['state-never', 'never_polled', 'Never polled'],
      ['state-notreg', 'not_registered', 'Not registered'],
      ['state-unknown', 'unknown', 'Unknown'],
    ];
    for (const [name, key, label] of wanted) {
      await expect(rowFor(page, name).locator('.fleet-state')).toHaveText(label);
      // Each state also earns a chip of its own, counted rather than ranked.
      const chip = page.locator(`.fleet-chip[data-fleet-state="${key}"]`);
      await expect(chip).toHaveCount(1);
      await expect(chip.locator('.fleet-chip-count')).toHaveText('1');
      await expect(chip.locator('.fleet-chip-label')).toHaveText(label);
    }

    // Six states, six distinct presentations. Nothing collapses into another.
    const tones = await page.evaluate(() => Array.from(document.querySelectorAll('.fleet-row'))
      .map(row => ({
        name: row.querySelector('.fleet-open')!.textContent,
        tone: Array.from(row.querySelector('.fleet-state')!.classList).find(c => c.startsWith('fleet-tone--')),
      })));
    expect(new Set(tones.map(t => t.tone)).size, `tones were ${JSON.stringify(tones)}`).toBe(6);

    // never_polled is not failing: no error, no failure count, and it says what
    // it is waiting for.
    const never = rowFor(page, 'state-never');
    await expect(never.locator('.badge.error')).toHaveCount(0);
    await expect(never.locator('.desc-trigger')).toHaveCount(0);
    await expect(never.locator('.fleet-reason-note')).toContainText('has not completed a cycle');

    // not_registered is a configuration fault, and says so rather than looking
    // like a device that is down.
    const notreg = rowFor(page, 'state-notreg');
    await expect(notreg.locator('.fleet-state')).toHaveClass(/fleet-tone--config/);
    await notreg.locator('.desc-trigger').click();
    await expect(page.locator('#descSheetMeta')).toContainText('configuration fault');
    await expect(page.locator('#descSheetBody')).toContainText('not registered with the poller');
    await page.keyboard.press('Escape');

    // unknown is neither healthy nor down, and carries the reason the server
    // gave for not knowing.
    const unknown = rowFor(page, 'state-unknown');
    await expect(unknown.locator('.fleet-state')).not.toHaveClass(/fleet-tone--(ok|down)/);
    await expect(unknown.locator('.desc-trigger')).toContainText('connection refused');

    // Healthy is quiet: no failure badge, no error line.
    await expect(rowFor(page, 'state-ok').locator('.badge.error')).toHaveCount(0);
    await expect(rowFor(page, 'state-ok').locator('.desc-trigger')).toHaveCount(0);
  });

  test('a never-polled device says never, not the year 1', async ({ page }) => {
    await serveWorkingTree(page);
    await serveFleet(page, [{
      id: 1, name: 'zero-time', address: '198.51.100.109', state: 'never_polled',
      status: {
        name: 'zero-time', connected: false, last_poll_success: false,
        last_poll_time: '0001-01-01T00:00:00Z', next_poll_time: '0001-01-01T00:00:00Z',
        metrics_collected: 0, consecutive_failures: 0, unreachable: false, poll_interval: '',
      },
    }]);
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });

    const row = rowFor(page, 'zero-time');
    await expect(row).toBeVisible({ timeout: 20000 });
    await expect(row.locator('.fleet-never').first()).toHaveText('never');
    await expect(row).toContainText('not scheduled');

    const text = await row.textContent();
    expect(text, 'the zero time must not leak through as a date').not.toMatch(/0001|1 AD|year 1/);
    // And "ago" is nowhere near it: a poll that never happened has no age.
    await expect(row.locator('.fleet-when')).toHaveCount(0);
  });

  // -------------------------------------------------------------------------
  // Scale
  // -------------------------------------------------------------------------

  test('two hundred devices stay one screen, searchable and paged', async ({ page }) => {
    const errors = consoleErrors(page);
    const NEEDLE = 'zzz-vault-probe-77';
    const fleet = syntheticFleet(200, NEEDLE);

    await serveWorkingTree(page);
    await serveFleet(page, fleet);

    const started = Date.now();
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.fleet-row').first()).toBeVisible({ timeout: 30000 });
    await expect(page.locator('.fleet-chip').first()).toBeVisible();
    const interactive = Date.now() - started;

    // The strip is the same six chips it is at four devices.
    const counts = await page.evaluate(() => Object.fromEntries(
      Array.from(document.querySelectorAll('.fleet-chip')).map(c => [
        (c as HTMLElement).dataset.fleetState || 'total',
        Number(c.querySelector('.fleet-chip-count')!.textContent!.replace(/,/g, '')),
      ])));
    expect(counts.total).toBe(200);
    expect(Object.values(counts).slice(1).reduce((a, b) => a + b, 0)).toBe(200);

    // A page at a time, the house size, with the position in the footer.
    await expect(page.locator('.fleet-row')).toHaveCount(50);
    await expect(page.locator('#fleetDeviceListStatus'))
      .toHaveText('Showing 1–50 of 200 devices');

    const overflow = await page.evaluate(() =>
      document.documentElement.scrollWidth - document.documentElement.clientWidth);
    const height = await page.evaluate(() => document.documentElement.scrollHeight);

    // Locating one named device among two hundred.
    const search = page.locator('#fleetDeviceListSearch');
    const findStarted = Date.now();
    await search.fill(NEEDLE);
    await expect(page.locator('.fleet-row')).toHaveCount(1);
    await expect(page.locator('.fleet-open')).toHaveText(NEEDLE);
    const located = Date.now() - findStarted;

    console.log(`200 devices: time to interactive ${interactive}ms, `
      + `horizontal overflow ${overflow}px, page height ${height}px, `
      + `time to locate "${NEEDLE}" ${located}ms`);

    expect(overflow, 'the list must not push the page sideways').toBe(0);
    expect(interactive, `took ${interactive}ms`).toBeLessThan(6000);
    expect(located, `took ${located}ms`).toBeLessThan(2000);

    // Paging on: four pages, reachable in either direction, and the last one
    // is exact about being partial rather than rounding up to a full page.
    await search.fill('');
    await expect(page.locator('.fleet-row')).toHaveCount(50);
    const nav = page.locator('#fleetDeviceListPager');
    await expect(nav.locator('.pager-page')).toHaveCount(4);

    await nav.getByRole('button', { name: 'Next page' }).click();
    await expect(page.locator('#fleetDeviceListStatus'))
      .toHaveText('Showing 51–100 of 200 devices');
    await expect(nav.getByRole('button', { name: 'Page 2 of 4' }))
      .toHaveAttribute('aria-current', 'page');

    await nav.getByRole('button', { name: 'Page 4 of 4' }).click();
    await expect(page.locator('#fleetDeviceListStatus'))
      .toHaveText('Showing 151–200 of 200 devices');
    await expect(nav.getByRole('button', { name: 'Next page' })).toBeDisabled();

    await nav.getByRole('button', { name: 'Previous page' }).click();
    await expect(page.locator('#fleetDeviceListStatus'))
      .toHaveText('Showing 101–150 of 200 devices');

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('a state chip narrows two hundred devices to the ones that need attention', async ({ page }) => {
    const fleet = syntheticFleet(200, 'zzz-vault-probe-77');
    const unreachable = fleet.filter(d => d.state === 'unreachable').length;
    expect(unreachable, 'the synthetic fleet should have unreachable devices').toBeGreaterThan(0);

    await serveWorkingTree(page);
    await serveFleet(page, fleet);
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.fleet-row').first()).toBeVisible({ timeout: 30000 });

    const chip = page.locator('.fleet-chip[data-fleet-state="unreachable"]');
    await chip.click();
    await expect(chip).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('.fleet-row')).toHaveCount(unreachable);
    await expect(page.locator('.fleet-state')).toHaveText(
      new Array(unreachable).fill('Unreachable'));
    await expect(page.locator('#fleetDeviceListStatus'))
      .toContainText('200 in total');

    // The order inside the filter is still the server's.
    const filtered = await page.locator('.fleet-open').allTextContents();
    expect(filtered).toEqual(fleet.filter(d => d.state === 'unreachable').map(d => d.name));

    // Search composes with the chip rather than replacing it.
    await page.locator('#fleetDeviceListSearch').fill(filtered[0]);
    await expect(page.locator('.fleet-row')).toHaveCount(1);

    // Clicking the chip again clears it.
    await page.locator('#fleetDeviceListSearch').fill('');
    await chip.click();
    await expect(chip).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('.fleet-row')).toHaveCount(50);
  });

  // -------------------------------------------------------------------------
  // When the endpoint itself is the thing that is broken
  // -------------------------------------------------------------------------

  test('a status endpoint that fails says so instead of showing an empty fleet', async ({ page }) => {
    let attempts = 0;
    await serveWorkingTree(page);
    await page.route('**/api/devices/status*', route => {
      attempts += 1;
      return route.fulfill({
        status: 503, contentType: 'application/json',
        body: JSON.stringify({ error: 'poller unavailable' }),
      });
    });
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });

    const header = page.locator('#fleetUpdated');
    await expect(header).toContainText('Could not read device status', { timeout: 20000 });
    await expect(header).toContainText('503');
    await expect(header).toHaveClass(/is-error/);
    expect(attempts).toBe(1);

    // Nothing is invented in its place: no rows, and no device presented as
    // healthy on the strength of a failed request. Nor is a fleet that could
    // not be read described as a fleet that has not been configured.
    await expect(page.locator('.fleet-row')).toHaveCount(0);
    await expect(page.locator('.fleet-chip')).toHaveCount(0);
    await expect(page.locator('#fleetDeviceList .empty-state')).toContainText('Device status is unavailable');
    await expect(page.locator('#fleetDeviceList')).not.toContainText('No devices configured');
    await expect(header).toContainText('No state has been read yet');
  });

  test('a malformed payload leaves the surface standing', async ({ page }) => {
    const errors = consoleErrors(page);
    await serveWorkingTree(page);
    await page.route('**/api/devices/status*', route => route.fulfill({
      status: 200, contentType: 'application/json', body: '{"devices": "not-a-list"',
    }));
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });

    await expect(page.locator('#fleetUpdated')).toContainText('Could not read device status', { timeout: 20000 });
    await expect(page.locator('#fleetOverview')).toBeVisible();
    // The rest of the app is untouched by it.
    await page.locator('#devicesBtn').click();
    await expect(page.locator('#devicesList table')).toBeVisible({ timeout: 20000 });

    // The parse failure is reported, and nothing else is.
    expect(errors.filter(e => !e.includes('Error loading fleet status')),
      `unexpected console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  // -------------------------------------------------------------------------
  // Refresh cadence
  // -------------------------------------------------------------------------

  test('the overview rides the dashboard refresh tick instead of adding one', async ({ page }) => {
    // Record every interval the page installs, so a second cadence competing
    // with the existing one shows up here rather than in the poller's logs.
    await page.addInitScript(() => {
      (window as any).__intervals = [];
      const real = window.setInterval;
      (window as any).setInterval = function (fn: any, delay?: any, ...rest: any[]) {
        (window as any).__intervals.push(delay);
        return real.call(window, fn, delay, ...rest);
      };
    });

    let statusRequests = 0;
    await serveWorkingTree(page);
    await page.route('**/api/devices/status*', async route => {
      statusRequests += 1;
      await route.continue();
    });

    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('.fleet-row').first()).toBeVisible({ timeout: 30000 });

    await page.waitForTimeout(10000);
    console.log(`/api/devices/status requests in the first ~10s on screen: ${statusRequests}`);
    expect(statusRequests, 'the overview must not poll the fan-out endpoint aggressively').toBe(1);

    const intervals = await page.evaluate(() => (window as any).__intervals as number[]);
    console.log(`intervals installed by the page: ${JSON.stringify(intervals)}`);
    expect(intervals.filter(d => d === 30000).length,
      'exactly one 30s dashboard refresh tick, shared by both surfaces').toBe(1);

    // With a device open the overview is not on screen, and the tick spends its
    // request on the device instead.
    const before = statusRequests;
    await rowFor(page, SEEDED_DEVICE).locator('.fleet-open').click();
    await expect(page.locator('#mibTabsContainer')).toBeVisible({ timeout: 45000 });
    await page.waitForTimeout(5000);
    expect(statusRequests, 'a hidden overview costs the poller nothing').toBe(before);
  });
});

// ---------------------------------------------------------------------------
// A synthesised fleet, over the same shape the endpoint really sends.
// ---------------------------------------------------------------------------

function syntheticFleet(count: number, needle: string): FleetEntry[] {
  const sites = ['ams', 'ber', 'chi', 'dfw', 'ewr', 'fra', 'gru', 'hkg', 'iad', 'jfk'];
  const roles = ['core', 'edge', 'dist', 'access', 'pdu'];
  const now = Date.now();
  const devices: FleetEntry[] = [];

  for (let i = 0; i < count - 1; i++) {
    const state = i % 17 === 3 ? 'failing'
      : i % 23 === 5 ? 'unreachable'
        : i % 29 === 7 ? 'never_polled'
          : i % 31 === 11 ? 'not_registered'
            : i % 37 === 13 ? 'unknown' : 'ok';
    const name = `${sites[i % sites.length]}-${roles[Math.floor(i / sites.length) % roles.length]}-`
      + String(Math.floor(i / (sites.length * roles.length)) + 1).padStart(2, '0');
    devices.push(entry(1000 + i, name, `10.${Math.floor(i / 254)}.${i % 254}.1`, state, i, now));
  }
  devices.push(entry(9999, needle, '203.0.113.99', 'ok', 0, now));

  // The server sorts; so does the fixture, so a client that re-sorts is caught
  // by the order assertions rather than hidden by them.
  return devices.sort((a, b) => a.name.localeCompare(b.name));
}

function entry(id: number, name: string, address: string, state: string, i: number, now: number): FleetEntry {
  if (state === 'not_registered') {
    return { id, name, address, state, error: `poller: device ${JSON.stringify(name)} is not registered with the poller` };
  }
  if (state === 'unknown') {
    return { id, name, address, state, error: `poller: status for device ${JSON.stringify(name)} is unavailable: connection refused` };
  }

  const failures = state === 'unreachable' ? 6 + (i % 5) : state === 'failing' ? 1 + (i % 3) : 0;
  const status: Record<string, unknown> = {
    name,
    connected: state === 'ok',
    last_poll_time: state === 'never_polled'
      ? '0001-01-01T00:00:00Z'
      : new Date(now - (10 + (i % 400)) * 1000).toISOString(),
    last_poll_success: state === 'ok',
    metrics_collected: state === 'ok' ? 900 + i * 37 : 0,
    poll_interval: '',
    unreachable: state === 'unreachable',
    consecutive_failures: failures,
    next_poll_time: new Date(now + (5 + (i % 60)) * 1000).toISOString(),
  };
  if (state !== 'ok' && state !== 'never_polled') {
    status.last_error = `poller: device ${name} answered none of 4 SNMP operations; `
      + 'skipped the remaining 41: snmp: GET of 20 OIDs failed: request timeout (after 3 retries)';
  }
  return { id, name, address, state, status };
}
