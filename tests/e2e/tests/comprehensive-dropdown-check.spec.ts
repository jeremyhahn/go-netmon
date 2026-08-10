import { test } from '@playwright/test';

test('Comprehensive dropdown check - all tables', async ({ page }) => {
  await page.goto('/');
  await page.waitForLoadState('networkidle');

  await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
  await page.waitForTimeout(3000);

  console.log('\n========================================');
  console.log('COMPREHENSIVE DROPDOWN CHECK');
  console.log('========================================\n');

  // Check IF-MIB tables
  console.log('=== IF-MIB ===');
  await page.locator('.tab-button:has-text("IF-MIB")').click();
  await page.waitForTimeout(2000);

  const ifMibTables = ['ifTable', 'ifXTable'];
  for (const tableName of ifMibTables) {
    const selector = `#master-instance-selector-1-IF-MIB-${tableName}`;
    if (await page.locator(selector).count() > 0) {
      const options = await page.locator(selector).locator('option').allTextContents();
      console.log(`\n${tableName}: ${options.length} options`);
      for (let i = 0; i < Math.min(5, options.length); i++) {
        console.log(`  ${i+1}. "${options[i]}"`);
      }
    }
  }

  // Check CISCO-BGP4-MIB tables
  console.log('\n\n=== CISCO-BGP4-MIB ===');
  await page.locator('.tab-button:has-text("CISCO-BGP4-MIB")').click();
  await page.waitForTimeout(2000);

  // Get all table sections
  const bgpSections = await page.locator('.table-widget-section').all();
  
  for (const section of bgpSections) {
    const heading = await section.locator('h4').first().textContent();
    if (heading && heading.includes('Table')) {
      const tableName = heading.trim();
      const dropdown = section.locator('select').first();
      
      if (await dropdown.count() > 0) {
        const options = await dropdown.locator('option').allTextContents();
        console.log(`\n${tableName}: ${options.length} options`);
        
        // Show first 5 options
        for (let i = 0; i < Math.min(5, options.length); i++) {
          console.log(`  ${i+1}. "${options[i]}"`);
        }
        
        // Flag if only showing numbers (no friendly names)
        if (options.length > 1) {
          const secondOption = options[1]; // Skip "Select instance..."
          const isOnlyNumbers = /^[\d.:]+$/.test(secondOption);
          if (isOnlyNumbers) {
            console.log(`  ⚠️  WARNING: Only showing numbers, no friendly names`);
          }
        }
      }
    }
  }
});
