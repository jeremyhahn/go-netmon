import { test, expect, Page } from '@playwright/test';

test.describe('User Workflow - Exact Steps', () => {
  test('should show what user sees step by step', async ({ page }) => {
    // Enable slow motion to see what happens
    await page.goto('/', { waitUntil: 'networkidle' });

    console.log('\n=== STEP 1: Page Loaded ===');
    await page.screenshot({ path: 'test-results/screenshots/user-01-page-loaded.png', fullPage: true });

    // What does user see? The redesign replaced the old <h1> with a sticky app
    // bar carrying the brand mark and the primary nav, so the landing view is
    // identified by those rather than by a heading element.
    await expect(page).toHaveTitle(/Network Monitor/);
    const brand = page.locator('header.appbar .brand-name');
    await expect(brand, 'app bar brand mark is missing').toBeVisible();
    await expect(brand).toHaveText('Network Monitor');
    await expect(page.locator('header.appbar nav.nav button.active'),
      'Dashboard should be the active view on load').toHaveText('Dashboard');

    // Check device dropdown
    const deviceDropdown = page.locator('#dashboardDeviceSelect');
    const deviceOptions = await deviceDropdown.locator('option').count();
    console.log(`Device dropdown has ${deviceOptions} options`);

    console.log('\n=== STEP 2: User Selects Device ===');
    await deviceDropdown.selectOption({ label: 'core-01' });
    console.log('Selected core-01');

    // Wait longer to see what loads
    await page.waitForTimeout(5000);
    await page.screenshot({ path: 'test-results/screenshots/user-02-device-selected.png', fullPage: true });

    // What tabs appear?
    const tabs = page.locator('.tab-button, .mib-tab-button, button[class*="tab"]');
    const tabCount = await tabs.count();
    console.log(`\nVisible tabs: ${tabCount}`);

    for (let i = 0; i < tabCount; i++) {
      const text = await tabs.nth(i).textContent();
      const visible = await tabs.nth(i).isVisible();
      console.log(`  Tab ${i}: "${text}" (visible: ${visible})`);
    }

    // Check what's visible on screen
    const visibleText = await page.locator('body').textContent();
    console.log('\n=== Visible MIB Names on Page ===');
    ['SNMPv2-MIB', 'IF-MIB', 'CISCO-BGP4-MIB'].forEach(mib => {
      console.log(`  ${mib}: ${visibleText?.includes(mib) ? 'YES' : 'NO'}`);
    });

    console.log('\n=== STEP 3: User Tries to Click IF-MIB ===');

    // Try multiple selectors
    const ifMibSelectors = [
      '.tab-button:has-text("IF-MIB")',
      'button:has-text("IF-MIB")',
      '[class*="tab"]:has-text("IF-MIB")',
      'text=IF-MIB'
    ];

    let clickedTab = false;
    for (const selector of ifMibSelectors) {
      const element = page.locator(selector).first();
      const count = await element.count();
      const visible = count > 0 ? await element.isVisible() : false;
      console.log(`  Selector "${selector}": found=${count}, visible=${visible}`);

      if (visible && !clickedTab) {
        console.log(`  → Clicking this one!`);
        await element.click();
        clickedTab = true;
        await page.waitForTimeout(2000);
      }
    }

    // Returning here would let the test pass while proving nothing, which is
    // the same silent skip the project rules forbid.
    expect(clickedTab,
      'no selector matched a visible IF-MIB tab for core-01').toBe(true);

    await page.screenshot({ path: 'test-results/screenshots/user-03-if-mib-clicked.png', fullPage: true });

    console.log('\n=== STEP 4: Looking for Dropdowns ===');

    // Check what's in the IF-MIB tab pane
    const ifMibPane = page.locator('#mib-tab-IF-MIB');
    await expect(ifMibPane,
      'clicking the IF-MIB tab did not reveal the IF-MIB pane').toBeVisible({ timeout: 20000 });
    const paneVisible = await ifMibPane.isVisible();
    console.log(`IF-MIB pane visible: ${paneVisible}`);

    if (paneVisible) {
      const paneHTML = await ifMibPane.innerHTML();
      console.log(`Pane HTML length: ${paneHTML.length} chars`);

      // Look for dropdowns
      const dropdowns = ifMibPane.locator('select');
      const dropdownCount = await dropdowns.count();
      console.log(`\nDropdowns in IF-MIB pane: ${dropdownCount}`);

      for (let i = 0; i < dropdownCount; i++) {
        const dd = dropdowns.nth(i);
        const id = await dd.getAttribute('id');
        const visible = await dd.isVisible();
        const enabled = await dd.isEnabled();
        const optCount = await dd.locator('option').count();

        console.log(`\n  Dropdown ${i}:`);
        console.log(`    ID: ${id}`);
        console.log(`    Visible: ${visible}`);
        console.log(`    Enabled: ${enabled}`);
        console.log(`    Options: ${optCount}`);

        if (optCount > 0) {
          const firstOpt = await dd.locator('option').first().textContent();
          console.log(`    First option: "${firstOpt}"`);

          if (optCount > 1) {
            const secondOpt = await dd.locator('option').nth(1).textContent();
            console.log(`    Second option: "${secondOpt}"`);
          }
        }

        // Take screenshot of this dropdown
        if (visible) {
          await dd.scrollIntoViewIfNeeded();
          await page.screenshot({
            path: `test-results/screenshots/user-04-dropdown-${i}.png`,
            fullPage: true
          });
        }
      }
    }

    console.log('\n=== STEP 5: Check for "No Data" or Error Messages ===');

    const errorPatterns = [
      'No data',
      'No instances',
      'Loading...',
      'Error',
      'Failed',
      'not found',
      'empty'
    ];

    const bodyText = await page.locator('body').textContent();
    for (const pattern of errorPatterns) {
      if (bodyText?.toLowerCase().includes(pattern.toLowerCase())) {
        console.log(`  ⚠️  Found: "${pattern}"`);
      }
    }

    console.log('\n=== STEP 6: Check Table Rendering ===');

    // Try selecting first populated dropdown.
    //
    // Only the IF-MIB pane is on screen; the other MIBs' panes are in the DOM
    // but hidden, and the order the panes are created in is whatever order
    // the server listed the device's MIBs, which varies per page load. A
    // page-wide locator therefore picked a hidden pane's selector at random
    // and scrollIntoViewIfNeeded/selectOption timed out on it.
    const allDropdowns = ifMibPane.locator('select[id*="instance"]');
    await expect(allDropdowns.first(),
      'IF-MIB pane rendered no instance selector').toBeVisible({ timeout: 20000 });

    const allDDCount = await allDropdowns.count();
    console.log(`\nTrying to select from ${allDDCount} instance dropdowns...`);

    let exercisedDropdown = false;
    for (let i = 0; i < allDDCount; i++) {
      const dd = allDropdowns.nth(i);
      const opts = await dd.locator('option').count();

      if (opts > 1) {
        const id = await dd.getAttribute('id');
        console.log(`\n  Testing dropdown: ${id}`);
        exercisedDropdown = true;

        await dd.scrollIntoViewIfNeeded();
        await dd.selectOption({ index: 1 });

        console.log('  Waiting for data to load...');
        await page.waitForTimeout(3000);

        await page.screenshot({
          path: `test-results/screenshots/user-05-after-selection-${i}.png`,
          fullPage: true
        });

        // Check what appeared
        const tables = page.locator('table');
        const tableCount = await tables.count();
        console.log(`  Tables visible: ${tableCount}`);

        const charts = page.locator('canvas, [id*="chart"]');
        const chartCount = await charts.count();
        console.log(`  Charts visible: ${chartCount}`);

        // Look for data
        const statusFields = ifMibPane.locator('.status-field, [class*="status"]');
        const statusCount = await statusFields.count();
        console.log(`  Status fields: ${statusCount}`);

        // The whole point of the workflow is that selecting an instance shows
        // something; logging the failure would let a blank pane pass.
        expect(tableCount + chartCount + statusCount,
          'IF-MIB pane displayed no tables, charts or status fields after selecting an instance').toBeGreaterThan(0);

        // Only test first working dropdown
        break;
      }
    }

    // Without this the loop finding nothing to click would end the test green.
    expect(exercisedDropdown,
      'no IF-MIB instance selector offered an instance to select').toBe(true);
  });

  // Selecting an interface has to turn into a live subscription: the page
  // sends a `subscribe` frame naming that interface's row and the server
  // streams `metric_update` frames back for it. Counting frames and printing
  // the total, which is all this test used to do, cannot fail — so the frames
  // are matched against the instance that was actually selected.
  test('should check WebSocket subscriptions', async ({ page }) => {
    const wsUrls: string[] = [];
    const sentFrames: string[] = [];
    const receivedFrames: any[] = [];

    // Intercept WebSocket
    page.on('websocket', ws => {
      console.log(`\n=== WebSocket opened: ${ws.url()} ===`);
      wsUrls.push(ws.url());

      ws.on('framesent', frame => {
        const raw = frame.payload.toString();
        sentFrames.push(raw);
        console.log('→ SENT:', raw.substring(0, 200));
      });

      ws.on('framereceived', frame => {
        try {
          const payload = JSON.parse(frame.payload.toString());
          receivedFrames.push(payload);
          console.log('← RECEIVED:', JSON.stringify(payload).substring(0, 200));
        } catch (e) {}
      });
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });

    // Click IF-MIB
    await page.locator('.tab-button:has-text("IF-MIB")').click();

    const ifMibPane = page.locator('#mib-tab-IF-MIB');
    await expect(ifMibPane, 'IF-MIB pane never became visible').toBeVisible({ timeout: 20000 });

    // The interface table's own selector, addressed by id suffix rather than
    // by DOM position: the hidden panes of the other MIBs carry instance
    // selectors too and the pane order is not stable between page loads, so
    // `select[id*="instance"]` first-match used to land on a hidden BGP peer
    // selector and time out.
    const dropdown = ifMibPane.locator('select[id$="-IF-MIB-ifTable"]');
    await expect(dropdown, 'the IF-MIB ifTable instance selector is missing')
      .toBeVisible({ timeout: 20000 });

    const options = dropdown.locator('option');
    await expect
      .poll(() => options.count(), {
        timeout: 20000,
        message: 'the ifTable instance selector listed no interface to subscribe to',
      })
      .toBeGreaterThan(1);

    const ifIndex = await options.nth(1).getAttribute('value');
    expect(ifIndex, 'the first real ifTable option carried no ifIndex').toBeTruthy();
    console.log(`\nSubscribing by selecting ifIndex ${ifIndex}`);

    await dropdown.selectOption({ index: 1 });

    // The page must have asked for that row...
    const subscribed = () => sentFrames.some(raw => {
      try {
        const frame = JSON.parse(raw);
        return frame.type === 'subscribe'
          && (frame.queries || []).some((q: string) => q.includes(`ifindex="${ifIndex}"`));
      } catch (e) {
        return false;
      }
    });
    await expect
      .poll(subscribed, {
        timeout: 20000,
        message: `no subscribe frame requested ifindex="${ifIndex}"`,
      })
      .toBe(true);

    // ...and the server must have answered for it.
    const updatesForInstance = () => receivedFrames.filter(frame =>
      frame.type === 'metric_update'
      && typeof frame.query === 'string'
      && frame.query.includes('device="core-01"')
      && frame.query.includes(`ifindex="${ifIndex}"`)).length;

    await expect
      .poll(updatesForInstance, {
        timeout: 30000,
        message: `no metric_update frame arrived for core-01 ifindex="${ifIndex}"`,
      })
      .toBeGreaterThan(0);

    expect(wsUrls.length, 'the page never opened a WebSocket').toBeGreaterThan(0);
    expect(wsUrls[0]).toMatch(/\/ws$/);

    console.log(`\n=== Total WebSocket messages: ${receivedFrames.length} ===`);
  });

  test('should check what happens on dropdown change', async ({ page }) => {
    const logs: string[] = [];

    page.on('console', msg => {
      logs.push(`[${msg.type()}] ${msg.text()}`);
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
    await page.waitForTimeout(3000);

    await page.locator('.tab-button:has-text("IF-MIB")').click();

    const tabPane = page.locator('#mib-tab-IF-MIB');
    await expect(tabPane, 'IF-MIB pane never became visible').toBeVisible({ timeout: 20000 });

    console.log('\n=== Before Dropdown Selection ===');
    console.log(`Console logs: ${logs.length}`);

    const beforeCount = logs.length;

    // Select interface. Scoped to the pane: the hidden panes belonging to the
    // device's other MIBs are still in the DOM.
    const dropdown = tabPane.locator('select[id$="-IF-MIB-ifTable"]');
    await expect(dropdown, 'the IF-MIB ifTable instance selector is missing')
      .toBeVisible({ timeout: 20000 });

    const options = dropdown.locator('option');
    await expect
      .poll(() => options.count(), {
        timeout: 20000,
        message: 'the ifTable instance selector listed no interface',
      })
      .toBeGreaterThan(1);

    console.log('\n=== Selecting Interface ===');
    const id = await dropdown.getAttribute('id');
    const selectedText = await options.nth(1).textContent();
    console.log(`Dropdown: ${id}`);
    console.log(`Selecting: ${selectedText}`);

    await dropdown.selectOption({ index: 1 });

    await page.waitForTimeout(5000);

    console.log('\n=== After Dropdown Selection ===');
    console.log(`New console logs: ${logs.length - beforeCount}`);

    // Show new logs
    logs.slice(beforeCount).forEach(log => {
      if (log.includes('ERROR') || log.includes('error') || log.includes('fail')) {
        console.log(`  ❌ ${log}`);
      } else if (log.includes('success') || log.includes('loaded') || log.includes('data')) {
        console.log(`  ✓ ${log}`);
      }
    });

    // Check DOM for data
    const tables = await tabPane.locator('table').count();
    const statusDivs = await tabPane.locator('[class*="status"]').count();

    console.log(`\n=== DOM After Selection ===`);
    console.log(`Tables: ${tables}`);
    console.log(`Status divs: ${statusDivs}`);

    // Changing the instance has to leave the pane showing that instance's
    // fields; an empty pane here is the regression the test is for.
    expect(tables + statusDivs,
      'the IF-MIB pane showed neither a table nor a status field after changing the instance')
      .toBeGreaterThan(0);

    await page.screenshot({
      path: 'test-results/screenshots/user-06-final-state.png',
      fullPage: true
    });
  });
});
