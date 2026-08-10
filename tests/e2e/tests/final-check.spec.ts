import { test } from '@playwright/test';

test('FINAL CHECK: What data is actually showing', async ({ page }) => {
  await page.goto('/');
  await page.waitForLoadState('networkidle');

  console.log('\n========================================');
  console.log('FINAL COMPREHENSIVE DATA CHECK');
  console.log('========================================\n');

  await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
  await page.waitForTimeout(3000);

  await page.locator('.tab-button:has-text("IF-MIB")').click();
  await page.waitForTimeout(2000);

  console.log('=== BEFORE Selection ===\n');

  // Check initial state of a status field
  const ifTableSection = page.locator('.table-widget-section:has(h4:text("ifTable"))').first();
  const statusFields = ifTableSection.locator('.status-value');
  const firstStatus = statusFields.first();

  if (await firstStatus.count() > 0) {
    const beforeText = await firstStatus.textContent();
    console.log(`First status field says: "${beforeText}"`);
  }

  // Select an interface
  console.log('\n=== SELECTING Interface "Gi0/0/0 (3)" ===\n');

  const dropdown = page.locator('#master-instance-selector-1-IF-MIB-ifTable');
  await dropdown.selectOption({ label: 'GigabitEthernet0/0/0 (3)' });

  console.log('Waiting 5 seconds for data to load...\n');
  await page.waitForTimeout(5000);

  console.log('=== AFTER Selection ===\n');

  // Check ALL status fields
  const allStatusFields = await ifTableSection.locator('.status-item').all();

  console.log(`Found ${allStatusFields.length} status fields:\n`);

  for (let i = 0; i < allStatusFields.length; i++) {
    const item = allStatusFields[i];
    const label = await item.locator('label').textContent();
    const value = await item.locator('.status-value').textContent();

    const hasData = value && value !== 'Select an instance above' && value !== 'N/A' && value !== '-' && value.trim() !== '';

    console.log(`${i+1}. ${label}`);
    console.log(`   Value: "${value}"`);
    console.log(`   Has data: ${hasData ? '✓ YES' : '✗ NO'}\n`);
  }

  // Check charts
  const charts = await ifTableSection.locator('.chart-container').all();
  console.log(`\n=== Charts (${charts.length}) ===\n`);

  for (let i = 0; i < Math.min(charts.length, 5); i++) {
    const chart = charts[i];
    const title = await chart.locator('h4').textContent();
    const canvas = chart.locator('canvas');
    const hasCanvas = await canvas.count() > 0;

    console.log(`${i+1}. ${title}`);
    console.log(`   Canvas exists: ${hasCanvas ? 'YES' : 'NO'}\n`);
  }

  await page.screenshot({
    path: 'test-results/screenshots/FINAL-if-table-with-data.png',
    fullPage: true
  });

  // Now check BGP
  console.log('\n========================================');
  console.log('BGP DATA CHECK');
  console.log('========================================\n');

  await page.locator('.tab-button:has-text("CISCO-BGP4-MIB")').click();
  await page.waitForTimeout(2000);

  const bgpDropdown = page.locator('#master-instance-selector-1-CISCO-BGP4-MIB-cbgpPeer2Table');
  const bgpOpts = await bgpDropdown.locator('option').all();

  if (bgpOpts.length > 1) {
    const selectedOpt = await bgpOpts[1].textContent();
    console.log(`Selecting BGP peer: ${selectedOpt}\n`);

    await bgpDropdown.selectOption({ index: 1 });
    await page.waitForTimeout(5000);

    const bgpSection = page.locator('.table-widget-section:has(h4:text("cbgpPeer2Table"))').first();
    const bgpStatusFields = await bgpSection.locator('.status-item').all();

    console.log(`Found ${bgpStatusFields.length} BGP status fields:\n`);

    for (let i = 0; i < Math.min(bgpStatusFields.length, 10); i++) {
      const item = bgpStatusFields[i];
      const label = await item.locator('label').textContent();
      const value = await item.locator('.status-value').textContent();

      const hasData = value && value !== 'Select an instance above' && value !== 'N/A' && value !== '-' && value.trim() !== '';

      console.log(`${i+1}. ${label}`);
      console.log(`   Value: "${value}"`);
      console.log(`   Has data: ${hasData ? '✓ YES' : '✗ NO'}\n`);
    }

    await page.screenshot({
      path: 'test-results/screenshots/FINAL-bgp-peer-with-data.png',
      fullPage: true
    });
  }

  console.log('\n========================================');
  console.log('SUMMARY');
  console.log('========================================\n');

  const totalStatusFields = await page.locator('.status-value').all();
  const filledFields = [];

  for (const field of totalStatusFields) {
    const text = await field.textContent();
    if (text && text !== 'Select an instance above' && text !== 'N/A' && text !== '-' && text.trim() !== '') {
      filledFields.push(text);
    }
  }

  console.log(`Total status fields: ${totalStatusFields.length}`);
  console.log(`Fields with data: ${filledFields.length}`);
  console.log(`Fields empty/waiting: ${totalStatusFields.length - filledFields.length}`);

  if (filledFields.length > 0) {
    console.log('\n✓ DATA IS DISPLAYING!');
    console.log('\nSample values:');
    filledFields.slice(0, 5).forEach((val, i) => {
      console.log(`  ${i+1}. "${val.substring(0, 50)}${val.length > 50 ? '...' : ''}"`);
    });
  } else {
    console.log('\n✗ NO DATA IS DISPLAYING - ISSUE CONFIRMED!');
  }
});
