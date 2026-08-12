import { test, expect } from '@playwright/test';

// Does an open dashboard stay put?
//
// The 30-second tick used to tear the dashboard down and rebuild it every time,
// because it compared a fleet-wide answer with a per-device one. That is fixed,
// and this watches for whatever is left: any redraw, any refetch of the spec,
// any DOM replacement, over several ticks.
//
// Deliberately no route interception. The point is what the deployed container
// serves, which is what the operator's browser runs -- an earlier check of the
// working tree would have missed a stale image entirely.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';

test('an open dashboard is not redrawn while it sits there', async ({ page }) => {
  test.setTimeout(300_000);

  const requests: string[] = [];
  page.on('request', r => {
    const url = r.url();
    if (url.includes('/api/')) requests.push(url.replace(BASE, ''));
  });

  const consoleErrors: string[] = [];
  page.on('console', m => { if (m.type() === 'error') consoleErrors.push(m.text()); });
  page.on('pageerror', e => consoleErrors.push('pageerror: ' + e.message));

  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

  // The fleet overview first: it is the landing surface, and its refresh
  // re-renders on every tick.
  //
  // The search is typed and allowed to settle *before* the rows are stamped:
  // filtering is supposed to redraw, and stamping first would measure that
  // rather than the tick.
  const search = page.locator('#fleetDeviceList input').first();
  await search.fill('core').catch(() => {});
  await page.waitForTimeout(2000);

  await page.evaluate(() => {
    let n = 0;
    for (const el of Array.from(document.querySelectorAll('.fleet-row'))) {
      (el as HTMLElement).dataset.fleetProbe = 'original';
      n++;
    }
    (window as any).__fleetStamped = n;
  });

  // One full tick with nobody touching anything.
  await page.waitForTimeout(45000);

  const fleet = await page.evaluate(() => ({
    stamped: (window as any).__fleetStamped,
    survivors: document.querySelectorAll('[data-fleet-probe="original"]').length,
    searchText: (document.querySelector('#fleetDeviceList input') as HTMLInputElement)?.value ?? '(none)',
  }));
  console.log('\n=== fleet overview across one idle tick ===');
  console.log('  rows stamped :', fleet.stamped, '-> surviving:', fleet.survivors);
  console.log('  search box   :', JSON.stringify(fleet.searchText));

  expect(fleet.stamped, 'no fleet rows to watch').toBeGreaterThan(0);
  expect(fleet.survivors,
    'the fleet table was rebuilt on an idle refresh, which is the flash a reader sees')
    .toBe(fleet.stamped);

  // core-01: the heaviest dashboard, so the most to lose to a redraw.
  await page.evaluate(() => (window as any).app.openFleetDevice(1));
  await page.waitForTimeout(15000);

  // Count the rebuild paths and stamp the DOM, then leave it alone.
  await page.evaluate(() => {
    const app = (window as any).app;
    (window as any).__counts = { loadDeviceDashboards: 0, loadMibCharts: 0, createMibTabs: 0 };

    for (const name of ['loadDeviceDashboards', 'loadMibCharts', 'createMibTabs']) {
      const real = app[name].bind(app);
      app[name] = function (...args: unknown[]) {
        (window as any).__counts[name]++;
        return real(...args);
      };
    }

    let stamped = 0;
    for (const el of Array.from(document.querySelectorAll('[id^="chart-"], [id^="status-"]'))) {
      (el as HTMLElement).dataset.stableProbe = 'original';
      stamped++;
    }
    (window as any).__stamped = stamped;
  });

  const before = await page.evaluate(() => ({
    stamped: (window as any).__stamped,
    charts: Object.keys((window as any).app.charts || {}).length,
    apiCalls: 0,
  }));
  const callsBefore = requests.length;

  expect(before.stamped, 'nothing was rendered to watch').toBeGreaterThan(20);

  // Three refresh ticks at 30s, plus room either side.
  await page.waitForTimeout(100_000);

  const after = await page.evaluate(() => ({
    counts: (window as any).__counts,
    survivors: document.querySelectorAll('[data-stable-probe="original"]').length,
    stamped: (window as any).__stamped,
    charts: Object.keys((window as any).app.charts || {}).length,
  }));

  const during = requests.slice(callsBefore);
  const specRefetches = during.filter(u => /\/dashboard|filter=available/.test(u));

  console.log('\n=== over ~3 refresh ticks ===');
  console.log('  rebuild calls        :', JSON.stringify(after.counts));
  console.log('  stamped nodes        :', after.stamped, '-> surviving:', after.survivors);
  console.log('  chart instances      :', before.charts, '->', after.charts);
  console.log('  API calls            :', during.length);
  console.log('  spec refetches       :', specRefetches.length,
    specRefetches.length ? '(' + specRefetches.slice(0, 4).join(', ') + ')' : '');
  console.log('  console errors       :', consoleErrors.length,
    consoleErrors.slice(0, 3).join(' | '));

  expect(after.counts.loadDeviceDashboards,
    'the dashboard was rebuilt while it sat idle').toBe(0);
  expect(after.counts.createMibTabs,
    'the module rail was rebuilt while the dashboard sat idle').toBe(0);
  expect(after.survivors,
    'rendered nodes were replaced, so the dashboard was redrawn').toBe(after.stamped);
  // The availability check runs on every tick by design: it is how a MIB that
  // has just started producing data earns its tab without a manual reload. What
  // it must not do is answer "something is new" every time and rebuild -- which
  // is what the counters above are for. One cheap request per tick is the
  // intended cost.
  expect(specRefetches.length,
    `the tick should cost about one availability check, not ${specRefetches.length}`)
    .toBeLessThanOrEqual(4);
  const rebuilds = specRefetches.filter(u => /\/dashboard/.test(u));
  expect(rebuilds.length,
    `the dashboard specification was refetched, so the cards were rebuilt: ${rebuilds.slice(0, 3).join(', ')}`)
    .toBe(0);
  expect(consoleErrors, consoleErrors.slice(0, 3).join(' | ')).toHaveLength(0);
});
