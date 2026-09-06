import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// Lazy per-tab subscription (issue #104).
//
// A device used to subscribe every module's panels the moment it was opened --
// on a switch with a full catalog that is ~200 queries and a multi-megabyte
// snapshot burst, most of it for tabs nobody had opened. Now a device opens by
// loading only the tab the reader lands on; each other tab loads on its first
// activation and stays subscribed after, so returning to it is instant.
//
// This asserts the wire behavior against the deployed backend: one tab
// subscribed on open, a fresh subscribe on first activation of another, and
// NOTHING on return to a tab already open.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';
const STATIC = path.resolve(__dirname, '../../../internal/webui/static');
const DEVICE = 5; // dist-01: a switch with a full catalog and many module tabs.

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

async function openDeviceById(page: Page, deviceId: number) {
  await expect(
    page.locator(`#dashboardDeviceSelect option[value="${deviceId}"]`),
    `device ${deviceId} never appeared in the selector`,
  ).toHaveCount(1, { timeout: 30000 });
  await page.evaluate(id => (window as any).app.openFleetDevice(id), deviceId);
}

// watchSubscribes records every subscribe/unsubscribe the browser SENDS, with a
// timestamp, so a phase of the test can count what crossed the wire during it.
type SentFrame = { t: number; type: string; queries: number };
function watchSubscribes(page: Page, sent: SentFrame[]) {
  page.on('websocket', ws => {
    ws.on('framesent', f => {
      let msg: any;
      try { msg = JSON.parse(f.payload.toString()); } catch { return; }
      if (msg.type === 'subscribe' || msg.type === 'unsubscribe') {
        sent.push({ t: Date.now(), type: msg.type, queries: (msg.queries || []).length });
      }
    });
  });
}

const subscribesSince = (sent: SentFrame[], since: number) =>
  sent.filter(f => f.type === 'subscribe' && f.t >= since);

async function setup(page: Page, sent: SentFrame[]) {
  watchSubscribes(page, sent);
  await serveWorkingTree(page);
  await page.addInitScript(() => { try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ } });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });
}

const activeSubCount = (page: Page) =>
  page.evaluate(() => (window as any).app.activeSubscriptions.size as number);

const tabCount = (page: Page) =>
  page.locator('#mibTabList .tab-button').count();

test('opening a device subscribes only the active tab, not every module', async ({ page }) => {
  test.setTimeout(120_000);

  const sent: SentFrame[] = [];
  await setup(page, sent);

  await openDeviceById(page, DEVICE);
  // Let the active tab render and its subscription land.
  await expect.poll(() => activeSubCount(page), { timeout: 30000 }).toBeGreaterThan(0);
  await page.waitForTimeout(3000);

  const tabs = await tabCount(page);
  const subscribedTabs = await activeSubCount(page);

  console.log(`\n=== open device ${DEVICE}: ${tabs} module tabs, ${subscribedTabs} subscribed ===`);

  expect(tabs, 'the device rendered no module tabs to be lazy about').toBeGreaterThan(1);
  // The whole point: opening the device does not subscribe every tab. Only the
  // one the reader is on -- one, or the couple an empty active tab falls
  // through to -- is subscribed, which must be well short of the full rail.
  expect(subscribedTabs,
    `${subscribedTabs} of ${tabs} tabs subscribed on open; lazy loading must leave the unopened tabs unsubscribed`)
    .toBeLessThan(tabs);
  expect(subscribedTabs, 'the active tab was never subscribed').toBeGreaterThan(0);
});

test('a tab subscribes on first activation and stays subscribed on return', async ({ page }) => {
  test.setTimeout(120_000);

  const sent: SentFrame[] = [];
  await setup(page, sent);

  await openDeviceById(page, DEVICE);
  await expect.poll(() => activeSubCount(page), { timeout: 30000 }).toBeGreaterThan(0);
  await page.waitForTimeout(2000);

  // Pick two module tabs that are not the one open on load.
  const others = await page.evaluate(() => {
    const active = (window as any).app.activeMibTab;
    return [...document.querySelectorAll('#mibTabList .tab-button')]
      .map(b => (b.textContent || '').trim())
      .filter(name => name && name !== active)
      .slice(0, 2);
  });
  expect(others.length, 'the device did not have two other tabs to exercise').toBe(2);
  const [first, second] = others;

  const clickTab = (name: string) => page.evaluate(n => {
    const b = [...document.querySelectorAll('#mibTabList .tab-button')]
      .find(x => (x.textContent || '').trim() === n) as HTMLElement | undefined;
    if (b) b.click();
  }, name);

  // First activation of `first`: a fresh subscribe for that tab.
  const beforeFirst = Date.now();
  const subsBeforeFirst = await activeSubCount(page);
  await clickTab(first);
  await expect.poll(() => activeSubCount(page), { timeout: 20000 }).toBe(subsBeforeFirst + 1);
  await page.waitForTimeout(1500);
  expect(subscribesSince(sent, beforeFirst).length,
    `activating ${first} sent no subscribe; a tab must subscribe on first open`).toBeGreaterThan(0);

  // First activation of `second`: another fresh subscribe.
  const beforeSecond = Date.now();
  const subsBeforeSecond = await activeSubCount(page);
  await clickTab(second);
  await expect.poll(() => activeSubCount(page), { timeout: 20000 }).toBe(subsBeforeSecond + 1);
  await page.waitForTimeout(1500);
  expect(subscribesSince(sent, beforeSecond).length,
    `activating ${second} sent no subscribe`).toBeGreaterThan(0);

  // Return to `first`, already open: no subscribe, no unsubscribe -- keep-alive.
  const beforeReturn = Date.now();
  const subsBeforeReturn = await activeSubCount(page);
  await clickTab(first);
  await page.waitForTimeout(3000);

  expect(await activeSubCount(page),
    'returning to an open tab changed the subscription set').toBe(subsBeforeReturn);
  const onReturn = sent.filter(f => f.t >= beforeReturn);
  expect(onReturn.length,
    `returning to ${first} sent ${onReturn.length} sub/unsub frames; an open tab is kept, not re-subscribed`)
    .toBe(0);
});
