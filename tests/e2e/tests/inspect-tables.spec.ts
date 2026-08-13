import { test, expect } from '@playwright/test';
import * as fs from 'fs';

test('should inspect table widget rendering', async ({ page }) => {
  await page.goto('/');
  await page.waitForLoadState('networkidle');

  await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
  await page.waitForTimeout(3000);

  await page.locator('.tab-button:has-text("IF-MIB")').click();
  await page.waitForTimeout(2000);

  // Select an interface
  const dropdown = page.locator('select[id*="ifTable"]').first();
  await dropdown.selectOption({ index: 1 });
  await page.waitForTimeout(3000);

  console.log('\n=== Inspecting Table Widgets ===');

  // Get the full HTML of ifTable widget
  const ifTableWidget = page.locator('[id*="table-widget"][id*="ifTable"]').first();
  const widgetExists = await ifTableWidget.count() > 0;

  console.log(`ifTable widget exists: ${widgetExists}`);

  if (widgetExists) {
    const widgetHTML = await ifTableWidget.innerHTML();
    fs.writeFileSync('test-results/screenshots/iftable-widget.html', widgetHTML);
    console.log(`Widget HTML saved (${widgetHTML.length} chars)`);

    // Check structure
    const hasStatusSection = widgetHTML.includes('status');
    const hasTableTag = widgetHTML.includes('<table');
    const hasMetricsSection = widgetHTML.includes('metric');

    console.log(`\nWidget structure:`);
    console.log(`  Has status section: ${hasStatusSection}`);
    console.log(`  Has <table> tag: ${hasTableTag}`);
    console.log(`  Has metrics section: ${hasMetricsSection}`);

    // Count specific elements
    const statusFields = await ifTableWidget.locator('.status-field, [class*="status"]').count();
    const tables = await ifTableWidget.locator('table').count();
    const charts = await ifTableWidget.locator('canvas').count();

    console.log(`\nElement counts:`);
    console.log(`  Status fields: ${statusFields}`);
    console.log(`  Tables: ${tables}`);
    console.log(`  Charts: ${charts}`);
  }

  // Check all table widgets
  const allTableWidgets = page.locator('[id*="table-widget"]');
  const widgetCount = await allTableWidgets.count();

  console.log(`\n=== All Table Widgets (${widgetCount}) ===`);

  for (let i = 0; i < Math.min(widgetCount, 5); i++) {
    const widget = allTableWidgets.nth(i);
    const id = await widget.getAttribute('id');
    const visible = await widget.isVisible();
    const html = await widget.innerHTML();

    console.log(`\nWidget ${i}: ${id}`);
    console.log(`  Visible: ${visible}`);
    console.log(`  HTML length: ${html.length}`);
    console.log(`  Has <table>: ${html.includes('<table')}`);
    console.log(`  Has status fields: ${html.includes('status-field') || html.includes('status-value')}`);
  }

  // Check for data-table class specifically
  const dataTables = page.locator('.data-table, table[class*="data"]');
  const dataTableCount = await dataTables.count();
  console.log(`\n=== Data Tables Found: ${dataTableCount} ===`);

  // Look for any <table> tags
  const allTables = page.locator('table');
  const allTableCount = await allTables.count();
  console.log(`\n=== All <table> Tags Found: ${allTableCount} ===`);

  for (let i = 0; i < Math.min(allTableCount, 3); i++) {
    const table = allTables.nth(i);
    const id = await table.getAttribute('id');
    const className = await table.getAttribute('class');
    const visible = await table.isVisible();
    const rows = await table.locator('tr').count();

    console.log(`\nTable ${i}:`);
    console.log(`  ID: ${id}`);
    console.log(`  Class: ${className}`);
    console.log(`  Visible: ${visible}`);
    console.log(`  Rows: ${rows}`);
  }

  // Check status field structure
  console.log(`\n=== Status Field Structure ===`);

  const statusFields = page.locator('.status-field').first();
  if (await statusFields.count() > 0) {
    const statusHTML = await statusFields.innerHTML();
    console.log('First status field HTML:');
    console.log(statusHTML.substring(0, 500));

    // Check for value
    const statusValue = page.locator('.status-field .status-value, .status-field [class*="value"]').first();
    if (await statusValue.count() > 0) {
      const valueText = await statusValue.textContent();
      console.log(`\nStatus value text: "${valueText}"`);
    }
  }

  await page.screenshot({
    path: 'test-results/screenshots/inspect-tables-full.png',
    fullPage: true
  });
});

test('should check dashboard spec vs rendered output', async ({ page }) => {
  // fetch() inside page.evaluate resolves against the document URL, so the page
  // has to be on the app before a relative path means anything. On about:blank
  // there is no origin and the URL fails to parse.
  await page.goto('/');
  await page.waitForLoadState('networkidle');

  // Resolve IF-MIB by name. Every MIB id is renumbered when the database is
  // rebuilt, and the id this used to hardcode now belongs to a different
  // module — one with no tables at all, which turned the comparison below
  // into 0 === 0 and made the test unable to fail.
  const ifMibId = await page.evaluate(async () => {
    const res = await fetch(new URL('/api/mibs', location.origin).toString());
    if (!res.ok) {
      throw new Error(`GET /api/mibs failed: ${res.status}`);
    }
    const body = await res.json();
    const mib = (body.mibs || []).find((m: any) => m.name === 'IF-MIB');
    if (!mib) {
      throw new Error('fixture MIB IF-MIB is not imported on this stack');
    }
    return mib.id;
  });

  // The spec the page renders from, which is the device-scoped one.
  //
  // /api/mibs/{id}/dashboard on its own describes everything the module could
  // show. The dashboard asks for it with ?device=NAME, which narrows it to the
  // objects that device actually produces -- 12 of IF-MIB's 17 ifTable charts
  // on core-01. Comparing the rendered page against the unnarrowed spec
  // measured the availability filter rather than the renderer, and reported a
  // mismatch every time the device did not implement every object.
  const dashboardSpec = await page.evaluate(async (id) => {
    const url = new URL(`/api/mibs/${id}/dashboard`, location.origin);
    url.searchParams.set('device', 'core-01');
    const res = await fetch(url.toString());
    if (!res.ok) {
      throw new Error(`GET ${url.pathname}${url.search} failed: ${res.status}`);
    }
    return await res.json();
  }, ifMibId);

  expect(dashboardSpec.mib_name, 'resolved the wrong MIB by id').toBe('IF-MIB');
  expect(dashboardSpec.table_widgets?.length,
    'IF-MIB dashboard spec declared no table widgets to compare against').toBeGreaterThan(0);

  console.log('\n=== IF-MIB Dashboard Spec ===');
  console.log(`Scalar widgets: ${dashboardSpec.scalar_widgets?.length || 0}`);
  console.log(`Table widgets: ${dashboardSpec.table_widgets?.length || 0}`);

  if (dashboardSpec.table_widgets && dashboardSpec.table_widgets.length > 0) {
    console.log('\n=== Table Widget Specs ===');
    dashboardSpec.table_widgets.forEach((widget: any, i: number) => {
      console.log(`\n${i}. ${widget.table_name}:`);
      console.log(`  Status fields: ${widget.status_fields?.length || 0}`);
      console.log(`  Metric charts: ${widget.metric_charts?.length || 0}`);
      console.log(`  Instance selector: ${widget.instance_selector?.label_name || 'none'}`);

      if (widget.status_fields && widget.status_fields.length > 0) {
        console.log(`  First status field: ${widget.status_fields[0].name}`);
      }
    });
  }

  // Now compare against the rendered page (already loaded above).
  await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
  await page.waitForTimeout(3000);

  await page.locator('.tab-button:has-text("IF-MIB")').click();

  const ifMibPane = page.locator('#mib-tab-IF-MIB');
  await expect(ifMibPane, 'IF-MIB pane never became visible').toBeVisible({ timeout: 20000 });

  // Select interface
  const dropdown = ifMibPane.locator('select[id$="-IF-MIB-ifTable"]');
  await dropdown.selectOption({ index: 1 });
  await page.waitForTimeout(3000);

  console.log('\n=== Rendered Output ===');

  // Check what actually rendered. The renderer emits one
  // .table-widget-section per table widget in the spec; it does not put
  // "table-widget" in any element id, so the old id-substring locator matched
  // nothing on every run.
  const renderedWidgets = ifMibPane.locator('.table-widget-section');

  console.log(`Expected from spec: ${dashboardSpec.table_widgets?.length || 0}`);

  // This mismatch is the one thing the test exists to detect, so it has to
  // fail rather than print.
  await expect(renderedWidgets,
    'rendered table widgets do not match the dashboard spec returned by the API')
    .toHaveCount(dashboardSpec.table_widgets.length, { timeout: 20000 });

  console.log(`Rendered table widgets: ${await renderedWidgets.count()}`);

  // Check for specific expected elements
  for (const widget of dashboardSpec.table_widgets) {
    const section = ifMibPane.locator('.table-widget-section', {
      has: page.locator(`h4:text-is("${widget.table_name}")`),
    });

    await expect(section,
      `the ${widget.table_name} widget the spec declares was not rendered`).toHaveCount(1);

    const statusFieldsRendered = await section.locator('.status-item').count();
    const chartsRendered = await section.locator('.chart-container').count();

    console.log(`\n${widget.table_name}:`);
    console.log(`  Status fields rendered: ${statusFieldsRendered} (expected: ${widget.status_fields?.length || 0})`);
    console.log(`  Charts rendered: ${chartsRendered} (expected: ${widget.metric_charts?.length || 0})`);

    expect(statusFieldsRendered,
      `${widget.table_name} rendered a different number of status fields than the spec declares`)
      .toBe(widget.status_fields?.length || 0);
    expect(chartsRendered,
      `${widget.table_name} rendered a different number of charts than the spec declares`)
      .toBe(widget.metric_charts?.length || 0);
  }
});
