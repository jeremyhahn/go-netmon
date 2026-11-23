import { test, expect } from '@playwright/test';

// Every configured device reaches the screen.
//
// The API listing every device is not the same as the operator seeing them: the
// fleet table pages, and the dashboard's device picker is built separately from
// it. This opens the deployed app in a browser and counts what is actually
// there, against what the API says exists.
//
// No route interception: the point is what the running container serves.

import { BASE } from '../target';

test('every configured device appears on the fleet overview and in the picker', async ({ page }) => {
  test.setTimeout(180_000);

  const configured: string[] = await page.evaluate(async base => {
    const response = await fetch(base + '/api/devices');
    const body = await response.json();
    return (body.devices || []).map((d: { name: string }) => d.name);
  }, BASE).catch(async () => {
    // Before the page is loaded there is no fetch context; go there first.
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    return page.evaluate(async () => {
      const response = await fetch('/api/devices');
      const body = await response.json();
      return (body.devices || []).map((d: { name: string }) => d.name);
    });
  });

  await page.addInitScript(() => {
    try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
  });
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });
  await page.waitForTimeout(6000);

  // The picker carries every device regardless of paging, so it is the
  // stricter of the two checks.
  const inPicker: string[] = await page.evaluate(() =>
    Array.from(document.querySelectorAll('#dashboardDeviceSelect option'))
      .map(o => (o as HTMLOptionElement).textContent?.trim() || '')
      .filter(name => name && !/fleet overview|all devices/i.test(name)));

  // The fleet table pages, so a large fleet is read across pages. The pager
  // states the total, which is what has to match.
  const fleetTotal = await page.evaluate(() => {
    const status = document.querySelector('#fleetDeviceListStatus')?.textContent || '';
    const rows = document.querySelectorAll('.fleet-row').length;
    return { status, rows };
  });

  console.log('\n=== fleet completeness ===');
  console.log('  configured (API) :', configured.length, configured.join(', '));
  console.log('  device picker    :', inPicker.length, inPicker.join(', '));
  console.log('  fleet rows shown :', fleetTotal.rows, '| pager:', JSON.stringify(fleetTotal.status.trim()));

  expect(configured.length, 'no devices are configured at all').toBeGreaterThan(0);

  const missing = configured.filter(name => !inPicker.includes(name));
  expect(missing,
    `configured devices absent from the picker: ${missing.join(', ')}`).toHaveLength(0);

  // Either every device is on the page, or the pager accounts for the rest.
  if (fleetTotal.rows < configured.length) {
    expect(fleetTotal.status,
      `only ${fleetTotal.rows} of ${configured.length} rows are shown and the pager does not say so`)
      .toContain(String(configured.length));
  }
});
