import { test } from '@playwright/test';

test('Detailed field check - identify all empty fields', async ({ page }) => {
  await page.goto('/');
  await page.waitForLoadState('networkidle');

  await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
  await page.waitForTimeout(3000);

  console.log('\n========================================');
  console.log('DETAILED FIELD ANALYSIS');
  console.log('========================================\n');

  // Check IF-MIB
  await page.locator('.tab-button:has-text("IF-MIB")').click();
  await page.waitForTimeout(2000);

  console.log('=== IF-MIB ifTable ===\n');

  const ifTableDropdown = page.locator('#master-instance-selector-1-IF-MIB-ifTable');
  await ifTableDropdown.selectOption({ label: 'GigabitEthernet0/0/0 (3)' });
  await page.waitForTimeout(5000);

  const ifTableSection = page.locator('.table-widget-section:has(h4:text("ifTable"))').first();
  const ifTableFields = await ifTableSection.locator('.status-item').all();

  console.log(`ifTable has ${ifTableFields.length} status fields:\n`);

  for (let i = 0; i < ifTableFields.length; i++) {
    const item = ifTableFields[i];
    const label = await item.locator('label').textContent();
    const value = await item.locator('.status-value').textContent();

    const isEmpty = !value || value === 'Select an instance above' || value === 'N/A' || value === '-' || value.trim() === '';

    console.log(`  ${i+1}. ${label}: "${value}" ${isEmpty ? '✗ EMPTY' : '✓'}`);
  }

  // Check ifXTable if it exists
  const ifXTableSection = page.locator('.table-widget-section:has(h4:text("ifXTable"))').first();
  const ifXTableExists = await ifXTableSection.count() > 0;

  if (ifXTableExists) {
    console.log('\n=== IF-MIB ifXTable ===\n');

    const ifXTableDropdown = page.locator('#master-instance-selector-1-IF-MIB-ifXTable');
    if (await ifXTableDropdown.count() > 0) {
      const opts = await ifXTableDropdown.locator('option').count();
      if (opts > 1) {
        await ifXTableDropdown.selectOption({ index: 1 });
        await page.waitForTimeout(5000);

        const ifXTableFields = await ifXTableSection.locator('.status-item').all();
        console.log(`ifXTable has ${ifXTableFields.length} status fields:\n`);

        for (let i = 0; i < ifXTableFields.length; i++) {
          const item = ifXTableFields[i];
          const label = await item.locator('label').textContent();
          const value = await item.locator('.status-value').textContent();

          const isEmpty = !value || value === 'Select an instance above' || value === 'N/A' || value === '-' || value.trim() === '';

          console.log(`  ${i+1}. ${label}: "${value}" ${isEmpty ? '✗ EMPTY' : '✓'}`);
        }
      }
    }
  }

  // Check CISCO-BGP4-MIB
  console.log('\n=== CISCO-BGP4-MIB cbgpPeer2Table ===\n');

  await page.locator('.tab-button:has-text("CISCO-BGP4-MIB")').click();
  await page.waitForTimeout(2000);

  const bgpDropdown = page.locator('#master-instance-selector-1-CISCO-BGP4-MIB-cbgpPeer2Table');
  const bgpOpts = await bgpDropdown.locator('option').count();

  if (bgpOpts > 1) {
    await bgpDropdown.selectOption({ index: 1 });
    await page.waitForTimeout(5000);

    const bgpSection = page.locator('.table-widget-section:has(h4:text("cbgpPeer2Table"))').first();
    const bgpFields = await bgpSection.locator('.status-item').all();

    console.log(`cbgpPeer2Table has ${bgpFields.length} status fields:\n`);

    for (let i = 0; i < bgpFields.length; i++) {
      const item = bgpFields[i];
      const label = await item.locator('label').textContent();
      const value = await item.locator('.status-value').textContent();

      const isEmpty = !value || value === 'Select an instance above' || value === 'N/A' || value === '-' || value.trim() === '';

      console.log(`  ${i+1}. ${label}: "${value}" ${isEmpty ? '✗ EMPTY' : '✓'}`);
    }
  }

  // Check other BGP tables
  const allBgpSections = await page.locator('.table-widget-section').all();

  for (const section of allBgpSections) {
    const heading = await section.locator('h4').first().textContent();
    if (heading && heading.includes('cbgp') && !heading.includes('cbgpPeer2Table')) {
      console.log(`\n=== CISCO-BGP4-MIB ${heading} ===\n`);

      const dropdown = section.locator('select').first();
      if (await dropdown.count() > 0) {
        const opts = await dropdown.locator('option').count();
        if (opts > 1) {
          await dropdown.selectOption({ index: 1 });
          await page.waitForTimeout(3000);

          const fields = await section.locator('.status-item').all();
          console.log(`${heading} has ${fields.length} status fields:\n`);

          for (let i = 0; i < Math.min(fields.length, 10); i++) {
            const item = fields[i];
            const label = await item.locator('label').textContent();
            const value = await item.locator('.status-value').textContent();

            const isEmpty = !value || value === 'Select an instance above' || value === 'N/A' || value === '-' || value.trim() === '';

            console.log(`  ${i+1}. ${label}: "${value}" ${isEmpty ? '✗ EMPTY' : '✓'}`);
          }
        }
      }
    }
  }

  await page.screenshot({
    path: 'test-results/screenshots/detailed-field-check.png',
    fullPage: true
  });
});
