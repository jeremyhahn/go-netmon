import { test, expect, Page } from '@playwright/test';

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

  test('should select core-01 and show MIB tabs', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    const deviceSelect = page.locator('#dashboardDeviceSelect');
    const options = await deviceSelect.locator('option').all();

    // Find core-01
    let foundDevice = false;
    for (const option of options) {
      const text = await option.textContent();
      if (text && text.includes('core-01')) {
        await deviceSelect.selectOption({ label: text });
        foundDevice = true;
        console.log(`Selected device: ${text}`);
        break;
      }
    }

    // core-01 is a permanent fixture on this stack. Its absence is a
    // regression the suite must report, not step around.
    expect(foundDevice,
      'fixture device core-01 was not present in the dashboard device selector').toBe(true);

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
    expect(tabCount, 'no MIB tabs rendered for core-01').toBeGreaterThan(0);

    for (let i = 0; i < tabCount; i++) {
      const tabText = await mibTabs.nth(i).textContent();
      console.log(`  Tab ${i}: ${tabText}`);
    }
  });

  test('should test IF-MIB dropdowns', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Select device
    const deviceSelect = page.locator('#dashboardDeviceSelect');
    const options = await deviceSelect.locator('option').all();

    let foundDevice = false;
    for (const option of options) {
      const text = await option.textContent();
      if (text && text.includes('core-01')) {
        await deviceSelect.selectOption({ label: text });
        foundDevice = true;
        break;
      }
    }

    expect(foundDevice,
      'fixture device core-01 was not present in the dashboard device selector').toBe(true);

    await page.waitForTimeout(3000);

    // Click IF-MIB tab
    const ifMibTab = page.locator('.tab-button:has-text("IF-MIB")');
    await expect(ifMibTab,
      'IF-MIB tab not rendered for core-01').toBeVisible();

    await ifMibTab.click();

    await page.screenshot({
      path: 'test-results/screenshots/03-if-mib-loaded.png',
      fullPage: true
    });

    // Every MIB gets its own .tab-pane and they all stay in the DOM; only the
    // active one is visible. The panes are built in the order the server
    // returns the device's MIBs, and that order is not stable between page
    // loads, so a page-wide `.first()` lands on a hidden pane's selector at
    // random and any action on it times out. Scope to the pane under test.
    const ifMibPane = page.locator('#mib-tab-IF-MIB');
    await expect(ifMibPane, 'IF-MIB pane never became visible').toBeVisible({ timeout: 20000 });

    const dropdowns = ifMibPane.locator('select[id*="instance-selector"]');
    await expect(dropdowns.first(),
      'IF-MIB pane rendered no instance selector').toBeVisible({ timeout: 20000 });

    const dropdownCount = await dropdowns.count();
    console.log(`\nFound ${dropdownCount} instance selector dropdowns`);

    for (let i = 0; i < dropdownCount; i++) {
      const dropdown = dropdowns.nth(i);
      const id = await dropdown.getAttribute('id');
      console.log(`\nDropdown ${i} (id: ${id}):`);

      const opts = await dropdown.locator('option').all();
      console.log(`  Has ${opts.length} options:`);

      for (let j = 0; j < Math.min(opts.length, 10); j++) {
        const val = await opts[j].getAttribute('value');
        const txt = await opts[j].textContent();
        console.log(`    [${j}] value="${val}" text="${txt}"`);
      }

      if (opts.length > 10) {
        console.log(`    ... and ${opts.length - 10} more options`);
      }
    }

    // Select the first real instance. Guarding this with an `if` would let an
    // empty selector — the exact regression this test exists to catch — pass
    // silently, so the preconditions are assertions instead.
    const firstDropdown = dropdowns.first();
    const firstOptions = firstDropdown.locator('option');
    await expect
      .poll(() => firstOptions.count(), {
        timeout: 20000,
        message: 'the first IF-MIB instance selector offered no instance beyond the placeholder',
      })
      .toBeGreaterThan(1);

    const instanceValue = await firstOptions.nth(1).getAttribute('value');
    expect(instanceValue, 'the first real instance option carried no value').toBeTruthy();

    console.log(`\nSelecting second option (value="${instanceValue}")...`);
    await firstDropdown.selectOption({ index: 1 });

    // The selection has to stick: a selector that resets itself is what a
    // broken instance handler looks like from the outside.
    await expect(firstDropdown).toHaveValue(instanceValue!);
    await page.waitForTimeout(3000);

    await page.screenshot({
      path: 'test-results/screenshots/04-interface-selected.png',
      fullPage: true
    });

    // Check for data tables
    const tables = page.locator('table');
    const tableCount = await tables.count();
    console.log(`\nFound ${tableCount} tables after selection`);
  });

  test('should test BGP dashboard', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Select device
    const deviceSelect = page.locator('#dashboardDeviceSelect');
    const options = await deviceSelect.locator('option').all();

    let foundDevice = false;
    for (const option of options) {
      const text = await option.textContent();
      if (text && text.includes('core-01')) {
        await deviceSelect.selectOption({ label: text });
        foundDevice = true;
        break;
      }
    }

    expect(foundDevice,
      'fixture device core-01 was not present in the dashboard device selector').toBe(true);

    await page.waitForTimeout(3000);

    // Click CISCO-BGP4-MIB tab
    const bgpTab = page.locator('.tab-button:has-text("CISCO-BGP4-MIB")');
    await expect(bgpTab,
      'CISCO-BGP4-MIB tab not rendered for core-01').toBeVisible();

    await bgpTab.click();

    await page.screenshot({
      path: 'test-results/screenshots/05-bgp-loaded.png',
      fullPage: true
    });

    // Scoped to the BGP pane for the same reason as the IF-MIB test: the
    // hidden panes of the other MIBs also contain instance selectors.
    const bgpPane = page.locator('#mib-tab-CISCO-BGP4-MIB');
    await expect(bgpPane, 'CISCO-BGP4-MIB pane never became visible').toBeVisible({ timeout: 20000 });

    const dropdowns = bgpPane.locator('select[id*="instance-selector"]');
    await expect(dropdowns.first(),
      'CISCO-BGP4-MIB pane rendered no peer selector').toBeVisible({ timeout: 20000 });

    const dropdownCount = await dropdowns.count();
    console.log(`\nFound ${dropdownCount} BGP peer selector dropdowns`);

    for (let i = 0; i < dropdownCount; i++) {
      const dropdown = dropdowns.nth(i);
      const id = await dropdown.getAttribute('id');
      console.log(`\nBGP Dropdown ${i} (id: ${id}):`);

      const opts = await dropdown.locator('option').all();
      console.log(`  Has ${opts.length} options:`);

      for (let j = 0; j < Math.min(opts.length, 10); j++) {
        const val = await opts[j].getAttribute('value');
        const txt = await opts[j].textContent();
        console.log(`    [${j}] value="${val}" text="${txt}"`);
      }
    }
  });

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
