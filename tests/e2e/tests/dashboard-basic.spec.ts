import { test, expect, Page } from '@playwright/test';
import { SEEDED_DEVICE } from '../stack';

test.describe('go-netmon WebUI - Basic Dashboard Tests', () => {
  test('should load main dashboard and verify basic UI', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Take initial screenshot
    await page.screenshot({
      path: 'test-results/screenshots/01-initial-load.png',
      fullPage: true
    });

    // Check page title
    const title = await page.title();
    console.log(`Page title: ${title}`);

    // Check header
    const header = page.locator('header h1');
    if (await header.count() > 0) {
      const headerText = await header.textContent();
      console.log(`Header text: ${headerText}`);
    }

    // Check device dropdown
    const deviceSelect = page.locator('#dashboardDeviceSelect');
    if (await deviceSelect.count() > 0) {
      await expect(deviceSelect).toBeVisible();
      const options = await deviceSelect.locator('option').all();
      console.log(`Found ${options.length} device options`);

      for (let i = 0; i < options.length; i++) {
        const value = await options[i].getAttribute('value');
        const text = await options[i].textContent();
        console.log(`  Device ${i}: "${text}" (value: ${value})`);
      }
    } else {
      console.log('Device dropdown not found');
    }
  });

  test(`should select ${SEEDED_DEVICE} and show MIB tabs`, async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    const deviceSelect = page.locator('#dashboardDeviceSelect');
    const options = await deviceSelect.locator('option').all();

    // Find the seeded device
    let foundDevice = false;
    for (const option of options) {
      const text = await option.textContent();
      if (text && text.includes(SEEDED_DEVICE)) {
        await deviceSelect.selectOption({ label: text });
        foundDevice = true;
        console.log(`Selected device: ${text}`);
        break;
      }
    }

    // The e2e seed restores this device on every stack (stack.ts). Its
    // absence is a regression the suite must report, not step around.
    expect(foundDevice,
      `seeded device ${SEEDED_DEVICE} was not present in the dashboard device selector`).toBe(true);

    // Wait for MIB tabs to load
    await page.waitForTimeout(3000);

    await page.screenshot({
      path: 'test-results/screenshots/02-device-selected.png',
      fullPage: true
    });

    // Check for MIB tabs
    const mibTabs = page.locator('.tab-button');
    const tabCount = await mibTabs.count();
    console.log(`Found ${tabCount} MIB tabs`);
    expect(tabCount, `no MIB tabs rendered for ${SEEDED_DEVICE}`).toBeGreaterThan(0);

    for (let i = 0; i < tabCount; i++) {
      const tabText = await mibTabs.nth(i).textContent();
      console.log(`  Tab ${i}: ${tabText}`);
    }
  });

  // Removed: "should test IF-MIB dropdowns" and "should test BGP dashboard".
  //
  // Both existed to exercise the per-table instance selector -- the dropdown
  // that chose which row of a table the single-series chart was showing. A
  // panel draws every row of its table at once now, so there is no dropdown to
  // select from and no row to choose; the surface those two tests asserted on
  // does not exist.
  //
  // What they were really protecting -- that a MIB's pane renders and carries
  // readings -- is asserted by render-audit.spec.ts, which classifies every
  // card on every device, and by widget-values.spec.ts, which checks that the
  // readings are the right SHAPE: a state by name, a counter as a rate, a unit
  // on the axis.

  test('should capture console errors', async ({ page }) => {
    const errors: string[] = [];

    page.on('console', msg => {
      if (msg.type() === 'error') {
        errors.push(msg.text());
      }
    });

    page.on('pageerror', error => {
      errors.push(`Page error: ${error.message}`);
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(5000);

    console.log(`\n=== Console Errors (${errors.length}) ===`);
    errors.forEach(err => console.log(`  ${err}`));

    // Filter out non-critical errors
    const criticalErrors = errors.filter(err =>
      !err.includes('favicon') &&
      !err.includes('WebSocket') &&
      !err.toLowerCase().includes('network')
    );

    console.log(`\n=== Critical Errors (${criticalErrors.length}) ===`);
    criticalErrors.forEach(err => console.log(`  ${err}`));
  });
});
