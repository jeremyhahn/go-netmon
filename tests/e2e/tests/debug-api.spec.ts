import { test, expect, Page } from '@playwright/test';

test.describe('API Debugging - Network Requests', () => {
  test('should capture all API requests and responses', async ({ page }) => {
    const apiCalls: any[] = [];

    // Intercept all API requests
    page.on('request', request => {
      if (request.url().includes('/api/')) {
        apiCalls.push({
          method: request.method(),
          url: request.url(),
          headers: request.headers(),
          postData: request.postData()
        });
      }
    });

    // Intercept all API responses
    page.on('response', async response => {
      if (response.url().includes('/api/')) {
        let body = null;
        try {
          body = await response.json();
        } catch (e) {
          try {
            body = await response.text();
          } catch (e2) {
            body = '<binary data>';
          }
        }

        console.log(`\n=== API Response ===`);
        console.log(`Status: ${response.status()} ${response.statusText()}`);
        console.log(`URL: ${response.url()}`);
        console.log(`Body:`, JSON.stringify(body, null, 2));
      }
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    console.log('\n=== Initial Page Load ===');
    console.log(`API calls made: ${apiCalls.length}`);
    apiCalls.forEach((call, i) => {
      console.log(`  ${i+1}. ${call.method} ${call.url}`);
    });

    // Select device
    console.log('\n=== Selecting Device core-01 ===');
    const deviceSelect = page.locator('#dashboardDeviceSelect');
    const options = await deviceSelect.locator('option').all();

    for (const option of options) {
      const text = await option.textContent();
      if (text && text.includes('core-01')) {
        await deviceSelect.selectOption({ label: text });
        break;
      }
    }

    // Wait for any API calls triggered by device selection
    await page.waitForTimeout(5000);

    console.log('\n=== After Device Selection ===');
    console.log(`Total API calls: ${apiCalls.length}`);
    apiCalls.forEach((call, i) => {
      console.log(`  ${i+1}. ${call.method} ${call.url}`);
    });

    await page.screenshot({
      path: 'test-results/screenshots/debug-01-after-device-selection.png',
      fullPage: true
    });
  });

  test('should check for MIB-related API calls', async ({ page }) => {
    const mibApiCalls: any[] = [];

    page.on('response', async response => {
      const url = response.url();
      if (url.includes('/api/devices/') && url.includes('/mibs')) {
        const body = await response.json().catch(() => null);
        mibApiCalls.push({
          url,
          status: response.status(),
          body
        });

        console.log(`\n=== MIB API Call ===`);
        console.log(`URL: ${url}`);
        console.log(`Status: ${response.status()}`);
        console.log(`Body:`, JSON.stringify(body, null, 2));
      }

      if (url.includes('/api/mibs/') && url.includes('/dashboard')) {
        const body = await response.json().catch(() => null);
        console.log(`\n=== Dashboard API Call ===`);
        console.log(`URL: ${url}`);
        console.log(`Status: ${response.status()}`);
        if (body) {
          console.log(`Scalar widgets: ${body.scalar_widgets?.length || 0}`);
          console.log(`Table widgets: ${body.table_widgets?.length || 0}`);
        }
      }
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    const deviceSelect = page.locator('#dashboardDeviceSelect');
    await deviceSelect.selectOption({ label: 'core-01' });
    await page.waitForTimeout(5000);

    console.log(`\n=== Summary ===`);
    console.log(`MIB API calls made: ${mibApiCalls.length}`);
  });

  test('should manually call MIB APIs and verify data', async ({ page }) => {
    await page.goto('/');

    // Call APIs directly using page.evaluate
    const devicesResponse = await page.evaluate(async () => {
      const res = await fetch('/api/devices');
      return await res.json();
    });

    console.log('\n=== /api/devices ===');
    console.log(JSON.stringify(devicesResponse, null, 2));

    const deviceMibsResponse = await page.evaluate(async () => {
      const res = await fetch('/api/devices/1/mibs');
      return await res.json();
    });

    console.log('\n=== /api/devices/1/mibs ===');
    console.log(JSON.stringify(deviceMibsResponse, null, 2));

    const ifMibDashboard = await page.evaluate(async () => {
      const res = await fetch('/api/mibs/2/dashboard');
      return await res.json();
    });

    console.log('\n=== /api/mibs/2/dashboard (IF-MIB) ===');
    console.log(`Scalar widgets: ${ifMibDashboard.scalar_widgets?.length || 0}`);
    console.log(`Table widgets: ${ifMibDashboard.table_widgets?.length || 0}`);
    if (ifMibDashboard.table_widgets && ifMibDashboard.table_widgets.length > 0) {
      console.log('\nFirst table widget:');
      console.log(JSON.stringify(ifMibDashboard.table_widgets[0], null, 2));
    }

    const bgpDashboard = await page.evaluate(async () => {
      const res = await fetch('/api/mibs/3/dashboard');
      return await res.json();
    });

    console.log('\n=== /api/mibs/3/dashboard (CISCO-BGP4-MIB) ===');
    console.log(`Scalar widgets: ${bgpDashboard.scalar_widgets?.length || 0}`);
    console.log(`Table widgets: ${bgpDashboard.table_widgets?.length || 0}`);
  });

  test('should check JavaScript execution and DOM state', async ({ page }) => {
    const consoleLogs: string[] = [];

    page.on('console', msg => {
      consoleLogs.push(`[${msg.type()}] ${msg.text()}`);
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Check if key JavaScript functions exist
    const jsState = await page.evaluate(() => {
      return {
        hasFetchDevices: typeof (window as any).fetchDevices === 'function',
        hasLoadDashboard: typeof (window as any).loadDashboard === 'function',
        hasGenerateMibTabs: typeof (window as any).generateMibTabs === 'function',
        windowKeys: Object.keys(window).filter(k => k.includes('device') || k.includes('mib') || k.includes('dashboard')).slice(0, 20)
      };
    });

    console.log('\n=== JavaScript State ===');
    console.log(JSON.stringify(jsState, null, 2));

    // Select device and check DOM
    const deviceSelect = page.locator('#dashboardDeviceSelect');
    await deviceSelect.selectOption({ label: 'core-01' });
    await page.waitForTimeout(3000);

    // Check for MIB-related DOM elements
    const domState = await page.evaluate(() => {
      return {
        mibTabsContainer: {
          exists: !!document.getElementById('mibTabsContainer'),
          visible: document.getElementById('mibTabsContainer')?.style.display !== 'none',
          childCount: document.getElementById('mibTabsContainer')?.children.length || 0,
          innerHTML: document.getElementById('mibTabsContainer')?.innerHTML.substring(0, 500)
        },
        mibContentContainer: {
          exists: !!document.getElementById('mibContentContainer'),
          visible: document.getElementById('mibContentContainer')?.style.display !== 'none',
          childCount: document.getElementById('mibContentContainer')?.children.length || 0
        },
        dashboardView: {
          exists: !!document.getElementById('dashboardView'),
          classes: document.getElementById('dashboardView')?.className,
          visible: document.getElementById('dashboardView')?.style.display !== 'none'
        },
        allMibElements: Array.from(document.querySelectorAll('[id*="mib"], [class*="mib"]')).map(el => ({
          id: el.id,
          class: el.className,
          tag: el.tagName
        })).slice(0, 20)
      };
    });

    console.log('\n=== DOM State After Device Selection ===');
    console.log(JSON.stringify(domState, null, 2));

    // Log recent console messages
    console.log('\n=== Console Messages (last 20) ===');
    consoleLogs.slice(-20).forEach(log => console.log(log));

    await page.screenshot({
      path: 'test-results/screenshots/debug-02-dom-state.png',
      fullPage: true
    });
  });

  test('should trace loadDashboard function execution', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Inject tracer
    await page.evaluate(() => {
      // Store original functions if they exist
      const original = {
        fetchDevices: (window as any).fetchDevices,
        loadDashboard: (window as any).loadDashboard,
        generateMibTabs: (window as any).generateMibTabs
      };

      // Wrap functions with logging
      if ((window as any).fetchDevices) {
        (window as any).fetchDevices = function(...args: any[]) {
          console.log('[TRACE] fetchDevices called with:', args);
          const result = original.fetchDevices?.apply(this, args);
          console.log('[TRACE] fetchDevices returned:', result);
          return result;
        };
      }

      if ((window as any).loadDashboard) {
        (window as any).loadDashboard = function(...args: any[]) {
          console.log('[TRACE] loadDashboard called with:', args);
          const result = original.loadDashboard?.apply(this, args);
          console.log('[TRACE] loadDashboard returned:', result);
          return result;
        };
      }

      if ((window as any).generateMibTabs) {
        (window as any).generateMibTabs = function(...args: any[]) {
          console.log('[TRACE] generateMibTabs called with:', args);
          const result = original.generateMibTabs?.apply(this, args);
          console.log('[TRACE] generateMibTabs returned:', result);
          return result;
        };
      }
    });

    const traces: string[] = [];
    page.on('console', msg => {
      if (msg.text().includes('[TRACE]')) {
        traces.push(msg.text());
      }
    });

    // Select device
    const deviceSelect = page.locator('#dashboardDeviceSelect');
    await deviceSelect.selectOption({ label: 'core-01' });
    await page.waitForTimeout(5000);

    console.log('\n=== Function Call Traces ===');
    traces.forEach(trace => console.log(trace));
  });

  test('should check event listeners on device select', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Check what event listeners are attached
    const eventListeners = await page.evaluate(() => {
      const select = document.getElementById('dashboardDeviceSelect');
      if (!select) return null;

      // Get event listener info (Chrome DevTools Protocol)
      return {
        hasOnChange: !!(select as any).onchange,
        hasEventListener: !!(select as any).addEventListener,
        value: (select as HTMLSelectElement).value,
        selectedIndex: (select as HTMLSelectElement).selectedIndex
      };
    });

    console.log('\n=== Device Select Event Listeners ===');
    console.log(JSON.stringify(eventListeners, null, 2));

    // Manually trigger change event and observe
    const deviceSelect = page.locator('#dashboardDeviceSelect');

    console.log('\n=== Before Selection ===');
    const beforeValue = await deviceSelect.inputValue();
    console.log(`Current value: ${beforeValue}`);

    await deviceSelect.selectOption({ label: 'core-01' });

    console.log('\n=== After Selection ===');
    const afterValue = await deviceSelect.inputValue();
    console.log(`New value: ${afterValue}`);

    await page.waitForTimeout(3000);

    // Check if any dashboards were created
    const dashboardElements = await page.evaluate(() => {
      return {
        charts: document.querySelectorAll('.chart-container').length,
        tables: document.querySelectorAll('table').length,
        mibTabs: document.querySelectorAll('.mib-tab-button').length,
        canvases: document.querySelectorAll('canvas').length
      };
    });

    console.log('\n=== Dashboard Elements Created ===');
    console.log(JSON.stringify(dashboardElements, null, 2));
  });

  test('should examine the full app.js code structure', async ({ page }) => {
    await page.goto('/');

    // Get all globally defined functions
    const globalFunctions = await page.evaluate(() => {
      const functions: string[] = [];
      for (const key in window) {
        try {
          if (typeof (window as any)[key] === 'function' && key !== 'constructor') {
            functions.push(key);
          }
        } catch (e) {
          // Skip inaccessible properties
        }
      }
      return functions.filter(f =>
        !f.startsWith('_') &&
        (f.includes('dashboard') || f.includes('device') || f.includes('mib') || f.includes('load'))
      ).sort();
    });

    console.log('\n=== Global Functions (dashboard/device/mib related) ===');
    globalFunctions.forEach(fn => console.log(`  - ${fn}`));

    // Try to get function source
    for (const fnName of globalFunctions.slice(0, 5)) {
      const fnSource = await page.evaluate((name) => {
        try {
          const fn = (window as any)[name];
          return fn ? fn.toString().substring(0, 300) : null;
        } catch (e) {
          return null;
        }
      }, fnName);

      if (fnSource) {
        console.log(`\n=== ${fnName} (first 300 chars) ===`);
        console.log(fnSource);
      }
    }
  });
});
