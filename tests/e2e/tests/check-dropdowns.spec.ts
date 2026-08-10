import { test } from '@playwright/test';

test('Check dropdown population', async ({ page }) => {
  await page.goto('/');
  await page.waitForLoadState('networkidle');

  await page.locator('#dashboardDeviceSelect').selectOption({ label: 'core-01' });
  await page.waitForTimeout(3000);

  console.log('\n========================================');
  console.log('DROPDOWN POPULATION CHECK');
  console.log('========================================\n');

  // Check IF-MIB ifTable
  console.log('=== IF-MIB ifTable ===');
  await page.locator('.tab-button:has-text("IF-MIB")').click();
  await page.waitForTimeout(2000);

  const ifTableDropdown = page.locator('#master-instance-selector-1-IF-MIB-ifTable');
  const ifTableOptions = await ifTableDropdown.locator('option').allTextContents();
  console.log(`ifTable dropdown has ${ifTableOptions.length} options:`);
  for (let i = 0; i < Math.min(5, ifTableOptions.length); i++) {
    console.log(`  ${i+1}. "${ifTableOptions[i]}"`);
  }

  // Check ifXTable
  console.log('\n=== IF-MIB ifXTable ===');
  const ifXTableDropdown = page.locator('#master-instance-selector-1-IF-MIB-ifXTable');
  const ifXTableOptions = await ifXTableDropdown.locator('option').allTextContents();
  console.log(`ifXTable dropdown has ${ifXTableOptions.length} options:`);
  for (let i = 0; i < Math.min(5, ifXTableOptions.length); i++) {
    console.log(`  ${i+1}. "${ifXTableOptions[i]}"`);
  }

  // Check CISCO-BGP4-MIB cbgpPeer2Table
  console.log('\n=== CISCO-BGP4-MIB cbgpPeer2Table ===');
  await page.locator('.tab-button:has-text("CISCO-BGP4-MIB")').click();
  await page.waitForTimeout(2000);

  const bgpDropdown = page.locator('#master-instance-selector-1-CISCO-BGP4-MIB-cbgpPeer2Table');
  const bgpOptions = await bgpDropdown.locator('option').allTextContents();
  console.log(`cbgpPeer2Table dropdown has ${bgpOptions.length} options:`);
  for (let i = 0; i < Math.min(10, bgpOptions.length); i++) {
    console.log(`  ${i+1}. "${bgpOptions[i]}"`);
  }

  // Check cbgpRouteTable
  console.log('\n=== CISCO-BGP4-MIB cbgpRouteTable ===');
  const routeDropdown = page.locator('#master-instance-selector-1-CISCO-BGP4-MIB-cbgpRouteTable');
  const routeOptions = await routeDropdown.locator('option').allTextContents();
  console.log(`cbgpRouteTable dropdown has ${routeOptions.length} options:`);
  for (let i = 0; i < Math.min(10, routeOptions.length); i++) {
    console.log(`  ${i+1}. "${routeOptions[i]}"`);
  }
});
