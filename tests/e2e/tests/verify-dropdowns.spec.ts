import { test, expect, Page } from '@playwright/test';

test.describe('Verify Dropdowns and Data Rendering', () => {
  test('should test IF-MIB dropdowns with correct selectors', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Select device
    const deviceSelect = page.locator('#dashboardDeviceSelect');
    await deviceSelect.selectOption({ label: 'core-01' });
    await page.waitForTimeout(3000);

    await page.screenshot({
      path: 'test-results/screenshots/verify-01-device-selected.png',
      fullPage: true
    });

    // Find MIB tabs with correct selector
    const tabs = page.locator('.tab-button');
    const tabCount = await tabs.count();
    console.log(`\nFound ${tabCount} MIB tabs`);

    for (let i = 0; i < tabCount; i++) {
      const tabText = await tabs.nth(i).textContent();
      console.log(`  Tab ${i}: ${tabText}`);
    }

    // Click IF-MIB tab
    const ifMibTab = page.locator('.tab-button:has-text("IF-MIB")');
    await expect(ifMibTab).toBeVisible();
    await ifMibTab.click();
    await page.waitForTimeout(2000);

    await page.screenshot({
      path: 'test-results/screenshots/verify-02-if-mib-tab-clicked.png',
      fullPage: true
    });

    // Check for tab pane
    const tabPane = page.locator('#mib-tab-IF-MIB');
    await expect(tabPane).toBeVisible();

    // Find ALL dropdowns/selects on the page
    const allSelects = page.locator('select');
    const selectCount = await allSelects.count();
    console.log(`\nFound ${selectCount} total select elements`);

    for (let i = 0; i < selectCount; i++) {
      const select = allSelects.nth(i);
      const id = await select.getAttribute('id');
      const name = await select.getAttribute('name');
      console.log(`\n  Select ${i}:`);
      console.log(`    ID: ${id}`);
      console.log(`    Name: ${name}`);

      const options = await select.locator('option').all();
      console.log(`    Options: ${options.length}`);

      // Log first few options
      for (let j = 0; j < Math.min(options.length, 5); j++) {
        const val = await options[j].getAttribute('value');
        const txt = await options[j].textContent();
        console.log(`      [${j}] "${txt}" (value: ${val})`);
      }
    }

    // Look for any elements with "instance" in their ID
    const instanceElements = page.locator('[id*="instance"]');
    const instCount = await instanceElements.count();
    console.log(`\nFound ${instCount} elements with "instance" in ID`);

    for (let i = 0; i < Math.min(instCount, 10); i++) {
      const elem = instanceElements.nth(i);
      const id = await elem.getAttribute('id');
      const tag = await elem.evaluate(el => el.tagName);
      console.log(`  ${i}. <${tag}> id="${id}"`);
    }

    await page.screenshot({
      path: 'test-results/screenshots/verify-03-dropdown-search.png',
      fullPage: true
    });
  });

  test('should find and test table widget dropdowns', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
    await page.waitForTimeout(3000);

    // Click IF-MIB tab
    await page.locator('.tab-button:has-text("IF-MIB")').click();
    await page.waitForTimeout(2000);

    // Search for table widgets
    const tableWidgets = page.locator('.table-widget');
    const widgetCount = await tableWidgets.count();
    console.log(`\nFound ${widgetCount} table widgets`);

    // Look for any dropdowns within the IF-MIB tab pane
    const tabPane = page.locator('#mib-tab-IF-MIB');
    const selectsInTab = tabPane.locator('select');
    const tabSelectCount = await selectsInTab.count();
    console.log(`\nFound ${tabSelectCount} select elements in IF-MIB tab`);

    for (let i = 0; i < tabSelectCount; i++) {
      const select = selectsInTab.nth(i);
      const id = await select.getAttribute('id');
      console.log(`\n  Dropdown ${i}: ${id}`);

      const options = await select.locator('option').all();
      console.log(`    Options count: ${options.length}`);

      if (options.length > 0) {
        // Log all options
        for (let j = 0; j < options.length; j++) {
          const val = await options[j].getAttribute('value');
          const txt = await options[j].textContent();
          console.log(`      [${j}] "${txt}" (value: ${val})`);
        }

        // Try selecting second option if available
        if (options.length > 1) {
          console.log(`\n  Selecting option 1...`);
          await select.selectOption({ index: 1 });
          await page.waitForTimeout(3000);

          await page.screenshot({
            path: `test-results/screenshots/verify-04-if-mib-option-${i}-selected.png`,
            fullPage: true
          });

          // Check for data tables
          const tables = page.locator('table');
          const tableCount = await tables.count();
          console.log(`    Found ${tableCount} tables after selection`);
        }
      }
    }
  });

  test('should test BGP dropdowns', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
    await page.waitForTimeout(3000);

    // Click CISCO-BGP4-MIB tab
    const bgpTab = page.locator('.tab-button:has-text("CISCO-BGP4-MIB")');
    await expect(bgpTab).toBeVisible();
    await bgpTab.click();
    await page.waitForTimeout(2000);

    await page.screenshot({
      path: 'test-results/screenshots/verify-05-bgp-tab-clicked.png',
      fullPage: true
    });

    // Look for dropdowns in BGP tab
    const tabPane = page.locator('#mib-tab-CISCO-BGP4-MIB');
    const selectsInTab = tabPane.locator('select');
    const tabSelectCount = await selectsInTab.count();
    console.log(`\nFound ${tabSelectCount} select elements in CISCO-BGP4-MIB tab`);

    for (let i = 0; i < Math.min(tabSelectCount, 5); i++) {
      const select = selectsInTab.nth(i);
      const id = await select.getAttribute('id');
      console.log(`\n  BGP Dropdown ${i}: ${id}`);

      const options = await select.locator('option').all();
      console.log(`    Options count: ${options.length}`);

      // Log options
      for (let j = 0; j < Math.min(options.length, 10); j++) {
        const val = await options[j].getAttribute('value');
        const txt = await options[j].textContent();
        console.log(`      [${j}] "${txt}" (value: ${val})`);
      }

      // Check if any options contain IP addresses
      if (options.length > 1) {
        const firstOptionText = await options[1].textContent();
        const hasIP = firstOptionText && /\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/.test(firstOptionText);
        console.log(`    Contains IP address: ${hasIP}`);
      }
    }
  });

  test('should capture full page HTML for analysis', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
    await page.waitForTimeout(3000);

    await page.locator('.tab-button:has-text("IF-MIB")').click();
    await page.waitForTimeout(2000);

    // Get the full HTML of the IF-MIB tab
    const tabHTML = await page.locator('#mib-tab-IF-MIB').innerHTML();

    // Save to file for analysis
    const fs = require('fs');
    fs.writeFileSync('test-results/screenshots/if-mib-tab-html.txt', tabHTML);

    console.log('\nIF-MIB tab HTML saved to test-results/screenshots/if-mib-tab-html.txt');
    console.log(`HTML length: ${tabHTML.length} characters`);

    // Search for key patterns
    const patterns = [
      { name: 'select elements', pattern: /<select/gi },
      { name: 'dropdown references', pattern: /dropdown/gi },
      { name: 'instance selector', pattern: /instance.*selector/gi },
      { name: 'table-widget', pattern: /table-widget/gi },
      { name: 'ifIndex mentions', pattern: /ifindex/gi },
    ];

    console.log('\nPattern matches in HTML:');
    for (const {name, pattern} of patterns) {
      const matches = tabHTML.match(pattern);
      console.log(`  ${name}: ${matches ? matches.length : 0} matches`);
    }
  });
});
