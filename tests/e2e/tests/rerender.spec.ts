import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// A pane that is redrawn must not sit blank waiting for a change.
//
// A browser re-renders a tab for ordinary reasons -- a reload, a MIB newly
// producing data, a device the reader came back to -- and the markup it was
// drawing into is gone. If the server still believes that client holds the
// reading, it sends a heartbeat naming the query instead of the numbers, and on
// a query that is STEADY there is no next change to rescue it. The panel waits
// for one that may never come, for as long as the page is open.
//
// The scheme this replaced kept a per-client fingerprint that survived exactly
// that cycle. It is the failure this product exists not to have: not an error,
// not a blank screen, but a panel confidently showing nothing while the device
// is answering normally.

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

// The device these run against. A dashboard is only worth measuring if it has
// panels on it, and the first entry in the selector is whichever device sorts
// first -- on this fleet a KVM with one. Named rather than indexed, so a thin
// device cannot make either of these pass by measuring nothing.
const DEVICE = process.env.NETMON_DEVICE || 'dist-01';

async function selectDevice(page: Page) {
  const labels = await page.locator('#dashboardDeviceSelect option').allTextContents();
  const wanted = labels.find(l => l.trim() === DEVICE);
  expect(wanted,
    `no device named "${DEVICE}" in the selector; it offers ${labels.join(', ')}. ` +
    `Set NETMON_DEVICE to one that has panels.`).toBeTruthy();
  await page.selectOption('#dashboardDeviceSelect', { label: wanted as string });
}

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

test('a redrawn tab fills again rather than waiting for a change', async ({ page }) => {
  test.setTimeout(240_000);

  await serveWorkingTree(page);
  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  await page.click('#dashboardBtn');
  await page.waitForTimeout(700);
  await selectDevice(page);

  // Wait for the panels to fill rather than for a fixed 50 seconds. The point
  // of this test is what a RE-render does to a filled tab, so the fill is a
  // precondition, and timing it by the clock made the precondition itself the
  // flake: 4 of 5 panels had not arrived when the stopwatch ran out. The bound
  // is generous and still fails if they never fill.
  await page.waitForFunction(() => {
    const pane = document.querySelector('#mibTabContent .tab-pane.active');
    if (!pane) return false;
    const total = pane.querySelectorAll('.panel').length;
    if (total < 5) return false;
    const waiting = pane.querySelectorAll('.chart-container.is-empty').length +
      [...pane.querySelectorAll('.status-value')].filter(e => (e as HTMLElement).dataset.waiting).length;
    return waiting === 0;
  }, undefined, { timeout: 120000 }).catch(() => { /* assert on the detail below */ });

  // What is on screen, and how much of it is still waiting.
  const state = () => page.evaluate(() => {
    const pane = document.querySelector('#mibTabContent .tab-pane.active');
    if (!pane) return { total: 0, waiting: 0, tab: null as string | null };
    return {
      tab: pane.id,
      total: pane.querySelectorAll('.panel').length,
      waiting: pane.querySelectorAll('.chart-container.is-empty').length +
        [...pane.querySelectorAll('.status-value')].filter(e => (e as HTMLElement).dataset.waiting).length,
    };
  });

  const before = await state();
  expect(before.total,
    'no panels were drawn, so a re-render would prove nothing').toBeGreaterThan(4);
  expect(before.waiting,
    `${before.waiting} of ${before.total} panels never filled before the re-render`)
    .toBe(0);

  // Redraw the tab the reader is looking at, exactly as a reload does.
  await page.evaluate(() => {
    const app = (window as any).app;
    return app.loadMibCharts(app.currentDashboardDevice, app.activeMibTab);
  });

  const after = await state();
  expect(after.total, 'the redraw produced no panels at all').toBe(before.total);

  // Every panel has to come back. Three streaming rounds is generous: the
  // server resends whole readings to a client it no longer has a sequence for,
  // so this does not depend on any value happening to change.
  await expect.poll(async () => (await state()).waiting, {
    timeout: 45000,
    message: 'panels were still waiting after the redraw; a steady reading was ' +
      'announced as unchanged to a browser that no longer had it',
  }).toBe(0);
});
