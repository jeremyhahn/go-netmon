import { test, expect, Page } from '@playwright/test';

// The dashboard is the view that broke: selecting a device rendered nothing
// because the MIB availability filter timed out. These tests drive the real
// page against a real server so a regression there fails here rather than in
// front of a user.

import { BASE } from '../target';
import { SEEDED_DEVICE, SEEDED_DEVICES } from '../stack';

// consoleErrors attaches a collector so a silently swallowed failure in the
// page still shows up in the test output.
function consoleErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('console', msg => {
    if (msg.type() === 'error') errors.push(msg.text());
  });
  page.on('pageerror', err => errors.push(`pageerror: ${err.message}`));
  return errors;
}

test.describe('device dashboard', () => {
  test('device list loads and shows the seeded devices', async ({ page }) => {
    const errors = consoleErrors(page);

    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('body')).toContainText(SEEDED_DEVICES[0], { timeout: 20000 });
    await expect(page.locator('body')).toContainText(SEEDED_DEVICES[1]);

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('selecting a device renders MIB tabs and collected data', async ({ page }) => {
    const errors = consoleErrors(page);

    await page.goto(BASE, { waitUntil: 'domcontentloaded' });

    // The Dashboard tab drives everything from this one dropdown, which is the
    // exact interaction that rendered nothing while the availability filter
    // was timing out.
    const selector = page.locator('#dashboardDeviceSelect');
    await expect(selector).toBeVisible({ timeout: 20000 });
    await expect(selector.locator('option', { hasText: SEEDED_DEVICE }))
      .toHaveCount(1, { timeout: 20000 });

    await selector.selectOption({ label: SEEDED_DEVICE });

    // The failure mode was an empty state showing for a device that does have
    // MIBs assigned, so assert it is not VISIBLE. The element stays in the DOM
    // and is merely hidden, so a text assertion against the body would pass
    // even when the page is broken.
    await expect(page.locator('#dashboardContent'))
      .toBeHidden({ timeout: 45000 });
    await expect(page.locator('#mibTabsContainer'))
      .toBeVisible({ timeout: 45000 });

    // One tab per MIB that has data flowing.
    const tabs = page.locator('#mibTabList .tab-button');
    await expect(tabs.first()).toBeVisible({ timeout: 45000 });
    expect(await tabs.count(), 'expected at least one MIB tab').toBeGreaterThan(0);

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('the rendered dashboard shows named MIBs and real values', async ({ page }) => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });

    const selector = page.locator('#dashboardDeviceSelect');
    await expect(selector).toBeVisible({ timeout: 20000 });
    await expect(selector.locator('option', { hasText: SEEDED_DEVICE }))
      .toHaveCount(1, { timeout: 20000 });
    await selector.selectOption({ label: SEEDED_DEVICE });

    const tabs = page.locator('#mibTabList .tab-button');
    await expect(tabs.first()).toBeVisible({ timeout: 45000 });

    // The tabs should be named after real modules the device polls, not
    // placeholders: the standard modules the seed assigns.
    const names = await tabs.allTextContents();
    expect(names.join(' '), `tabs were: ${names.join(', ')}`)
      .toMatch(/IF-MIB|SNMPv2-MIB|HOST-RESOURCES-MIB/);

    // And the active tab should end up with rendered content rather than an
    // empty pane.
    const content = page.locator('#mibTabContent');
    await expect(content).toBeVisible({ timeout: 45000 });
    await expect(content).not.toBeEmpty({ timeout: 45000 });
  });

  test('the MIB availability filter answers quickly', async ({ page }) => {
    // This endpoint previously took over 30 seconds and killed the dashboard.
    // A generous ceiling still catches a return to per-module scanning.
    const started = Date.now();
    const response = await page.request.get(`${BASE}/api/mibs?filter=available`, { timeout: 30000 });
    const elapsed = Date.now() - started;

    expect(response.ok(), `status ${response.status()}`).toBeTruthy();
    expect(elapsed, `took ${elapsed}ms`).toBeLessThan(10000);

    const body = await response.json();
    expect(Array.isArray(body.mibs)).toBeTruthy();
    expect(body.mibs.length, 'expected MIBs with data').toBeGreaterThan(0);
  });
});
