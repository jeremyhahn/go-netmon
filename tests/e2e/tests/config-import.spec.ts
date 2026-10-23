import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

/**
 * Importing a configuration replaces every device on the page.
 *
 * The dashboard has to be put somewhere sane afterwards, and leaving that to
 * loadDevices' own restore does not do it: the restore replays the device
 * selected at STARTUP, not the one the reader is on, and it does nothing at all
 * when the imported configuration no longer has that device. Both cases left
 * currentDashboardDevice naming a device the rebuilt selector did not offer, so
 * fleetVisible() answered no to the refresh tick while the fleet was on screen,
 * and subscriptions for modules the import removed stayed alive.
 *
 * The MUTATING endpoint is mocked, always: nothing in this file may reach a real
 * configuration, because an import that got through would replace the device
 * inventory of whatever the suite is pointed at. The device and profile reads
 * are mocked too, since they are what the transition is judged against. The
 * rest -- the page itself, MIBs, fleet and device status -- is served by the
 * appliance, so these are integration tests of the handler and not unit tests
 * of a simulation of it.
 */

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

const KEPT = { id: 1, name: 'kept-01', address: '198.51.100.101', enabled: true };
const DROPPED = { id: 2, name: 'dropped-02', address: '198.51.100.102', enabled: true };

// afterImport is the device list the rebuilt selector is given, or the name of
// the read that should fail after the server has already applied the import.
type Unreadable = 'devices' | 'profiles' | 'mibs';
//
// The returned handle reports whether the APPLY call was made. The first
// version of these tests mocked a plan with no `changed` count, so
// importConfiguration answered "nothing to import" and returned before doing
// anything -- and one of the tests passed on that.
function mockConfig(page: Page, afterImport: object[] | Unreadable) {
  const calls = { plan: 0, apply: 0 };
  let imported = false;

  page.on('dialog', dialog => dialog.accept());

  const routes = (async () => {
  await page.route(/\/api\/config\/import/, route => {
    const apply = new URL(route.request().url()).searchParams.get('apply') === 'true';
    if (apply) { calls.apply++; imported = true; } else { calls.plan++; }
    return route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        changed: 2,
        counts: { devices_imported: 1, profiles_imported: 0, mibs_imported: 1 },
        safety_export: '/var/lib/go-netmon/config-before-import.tar.gz',
      }),
    });
  });
  // A 503 whose BODY is valid JSON: neither loader checked response.ok, so an
  // error page that happens to parse was accepted as an authoritative empty
  // list -- the failure mode that reads as "the import removed everything".
  const failed = (route: import('@playwright/test').Route) => route.fulfill({
    status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'unavailable' }),
  });
  const broken = (which: Unreadable) => imported && afterImport === which;

  await page.route(/\/api\/devices(\?|$)/, route => {
    if (broken('devices')) return failed(route);
    return route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        devices: imported && Array.isArray(afterImport) ? afterImport : [KEPT, DROPPED],
      }),
    });
  });
  await page.route(/\/api\/profiles(\?|$)/, route => {
    if (broken('profiles')) return failed(route);
    return route.fulfill({
      status: 200, contentType: 'application/json', body: JSON.stringify({ profiles: [] }),
    });
  });
  await page.route(/\/api\/mibs(\?|$)/, route => {
    if (broken('mibs')) return failed(route);
    return route.continue();
  });
  })();

  return { calls, ready: routes };
}

// runImport drives the handler directly. The file input is hidden and its
// change event is the only way in, so the file is handed over as a Blob.
async function runImport(page: Page) {
  await page.evaluate(async () => {
    const app = (window as any).app;
    const file = new File(['{}'], 'config.json', { type: 'application/json' });
    await app.importConfiguration(file, document.getElementById('importConfigBtn'));
  });
}

async function openPage(
  page: Page,
  afterImport: object[] | Unreadable,
  startedOn: string | null = null,
) {
  await serveWorkingTree(page);
  const config = mockConfig(page, afterImport);
  await config.ready;
  // startedOn is what the SESSION began with -- the value loadState reads back
  // into restoredDeviceId, which is what loadDevices' restore replays. Without
  // it there is nothing to restore and a test of the restore proves nothing.
  await page.addInitScript(id => {
    try {
      if (id === null) localStorage.removeItem('go-netmon-state');
      else localStorage.setItem('go-netmon-state', JSON.stringify({
        currentView: 'dashboard', currentTimeRange: '6h', selectedDeviceId: id,
      }));
    } catch (e) { /* private mode */ }
  }, startedOn);
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#dashboardDeviceSelect option[value="2"]'))
    .toHaveCount(1, { timeout: 30000 });
  // The fleet has a device 2 of its own, so the fixture is confirmed by NAME.
  await expect(page.locator('#dashboardDeviceSelect option[value="2"]'))
    .toHaveText('dropped-02');
  return config;
}

test('an import that keeps the open device leaves it open', async ({ page }) => {
  test.setTimeout(120_000);
  // The session began on device 1 and the reader moved to 2 -- the case
  // loadDevices' restore cannot reproduce, since restoredDeviceId still holds
  // the device the session began with.
  const config = await openPage(page, [KEPT, DROPPED], '1');

  await page.evaluate(() => (window as any).app.openFleetDevice(2));
  await page.waitForFunction(() => String((window as any).app.currentDashboardDevice) === '2',
    undefined, { timeout: 30000 });

  // Which devices the page asks about from here on. The import must load the
  // device the reader is ON, and only that one: leaving the restore enabled
  // also loaded device 1, whose paint the generation guard then threw away.
  // continue(), not fetch()+fulfill(): fetching the response into the test
  // leaves a request in flight that fails the run when the page closes under
  // it ("Target page, context or browser has been closed"). The URL is all this
  // needs, and continue() reads it without owning the response.
  const loaded: string[] = [];
  await page.route(/\/api\/devices\/(\d+)\/dashboard$/, route => {
    loaded.push(new URL(route.request().url()).pathname.split('/')[3]);
    return route.continue();
  });

  await runImport(page);
  await expect.poll(async () => page.evaluate(() =>
    document.getElementById('configTransferStatus')?.textContent ?? ''),
    { timeout: 30000 }).toContain('Imported');
  await page.waitForTimeout(2000);

  const state = await page.evaluate(() => {
    const app = (window as any).app;
    const select = document.getElementById('dashboardDeviceSelect') as HTMLSelectElement;
    return { current: String(app.currentDashboardDevice), selected: select.value };
  });

  expect(config.calls.apply, 'no configuration was applied, so nothing was tested').toBe(1);
  expect(state.current, 'the import moved the reader off the device they were on').toBe('2');
  expect(state.selected, 'the selector disagrees with the dashboard').toBe('2');
  // WHICH devices, not how many times. The refresh tick re-reads the open
  // device's dashboard on its own schedule, so a second load of device 2 is
  // ordinary and asserting "exactly one" measured that timer instead -- it
  // passed alone and failed inside the suite. Loading device 1 is the defect,
  // and that is what leaving the restore on produces: 2, 1, 2.
  expect([...new Set(loaded)],
    `the import loaded ${loaded.join(', ')}; only the open device should have been loaded`)
    .toEqual(['2']);
});

test('an import that removes the open device returns to the fleet', async ({ page }) => {
  test.setTimeout(120_000);
  const config = await openPage(page, [KEPT]);

  await page.evaluate(() => (window as any).app.openFleetDevice(2));
  await page.waitForTimeout(2000);

  const before = await page.evaluate(() =>
    (window as any).app.activeSubscriptions.size);

  await runImport(page);
  await page.waitForTimeout(2000);

  const state = await page.evaluate(() => {
    const app = (window as any).app;
    const select = document.getElementById('dashboardDeviceSelect') as HTMLSelectElement;
    return {
      current: app.currentDashboardDevice,
      selected: select.value,
      fleetVisible: app.fleetVisible(),
      subscriptions: app.activeSubscriptions.size,
      offered: select.querySelectorAll('option[value="2"]').length,
    };
  });

  expect(config.calls.apply, 'no configuration was applied, so nothing was tested').toBe(1);
  expect(state.offered, 'the fixture kept the device, so this proves nothing').toBe(0);
  expect(state.current,
    'the dashboard still names a device the selector no longer offers').toBeFalsy();
  expect(state.selected, 'the selector was left on the removed device').toBe('');
  expect(state.fleetVisible,
    'the fleet is on screen but fleetVisible() says otherwise, so it never refreshes')
    .toBe(true);
  expect(state.subscriptions,
    `${before} subscriptions before the import, ${state.subscriptions} after: ` +
    `the removed device is still being streamed`).toBe(0);
});


test('an import does not reopen the device the session started on', async ({ page }) => {
  test.setTimeout(120_000);
  // The session STARTED on device 2 -- which is what restoredDeviceId holds --
  // and the reader has since gone back to the fleet. loadDevices' restore
  // replays that startup device on every call, so the import used to load and
  // subscribe to it while it was deciding where the reader should land.
  const config = await openPage(page, [KEPT, DROPPED], '2');

  await page.waitForFunction(() => (window as any).app.currentDashboardDevice, undefined,
    { timeout: 30000 });
  await page.evaluate(() => {
    const app = (window as any).app;
    app.unsubscribeFromAllDashboards(app.currentDashboardDevice);
    app.currentDashboardDevice = null;
    (document.getElementById('dashboardDeviceSelect') as HTMLSelectElement).value = '';
    app.showFleetOverview();
  });

  await runImport(page);
  await expect.poll(async () => page.evaluate(() =>
    document.getElementById('configTransferStatus')?.textContent ?? ''),
    { timeout: 30000 }).toContain('Imported');

  const state = await page.evaluate(() => {
    const app = (window as any).app;
    const select = document.getElementById('dashboardDeviceSelect') as HTMLSelectElement;
    return {
      current: app.currentDashboardDevice,
      selected: select.value,
      fleetVisible: app.fleetVisible(),
      subscriptions: app.activeSubscriptions.size,
    };
  });

  expect(config.calls.apply, 'no configuration was applied, so nothing was tested').toBe(1);
  expect(state.current, 'the import reopened the device the session began on').toBeFalsy();
  expect(state.selected, 'the selector was moved off the fleet').toBe('');
  expect(state.fleetVisible, 'the fleet is on screen but fleetVisible() says otherwise').toBe(true);
  expect(state.subscriptions,
    'the startup device was subscribed while the import was deciding where to land').toBe(0);
});

for (const which of ['devices', 'profiles', 'mibs'] as const) {
test(`an import the page cannot refresh says so, and does not guess (${which})`, async ({ page }) => {
  test.setTimeout(120_000);
  const config = await openPage(page, which);

  await page.evaluate(() => (window as any).app.openFleetDevice(2));
  await page.waitForFunction(() => (window as any).app.currentDashboardDevice, undefined,
    { timeout: 30000 });

  await runImport(page);

  // The server applied it. Only the page failed -- so the message must not say
  // the import failed, and the selector must not be believed about whether the
  // open device survived, because it still lists the replaced configuration.
  await expect.poll(async () => page.evaluate(() =>
    document.getElementById('configTransferStatus')?.textContent ?? ''),
    { timeout: 30000 }).toContain('could not be refreshed');

  const state = await page.evaluate(() => {
    const app = (window as any).app;
    const select = document.getElementById('dashboardDeviceSelect') as HTMLSelectElement;
    return {
      status: document.getElementById('configTransferStatus')?.textContent ?? '',
      current: app.currentDashboardDevice,
      selected: select.value,
      fleetVisible: app.fleetVisible(),
    };
  });

  expect(config.calls.apply, 'no configuration was applied, so nothing was tested').toBe(1);
  expect(state.status, 'a successful import was reported as a failure').not.toContain('Import failed');
  expect(state.current, 'the page kept a device it could not verify still exists').toBeFalsy();
  expect(state.selected).toBe('');
  expect(state.fleetVisible, 'the fleet is on screen but fleetVisible() says otherwise').toBe(true);
});
}
