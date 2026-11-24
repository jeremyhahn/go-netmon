import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// The Generate dashboard button opens the device dashboard and reports which MIBs produced panels; API responses are intercepted.

import { BASE } from '../target';
const STATIC = path.resolve(__dirname, '../../../internal/webui/static');

test.use({
  launchOptions: {
    executablePath: process.env.CHROME_PATH || '/usr/bin/google-chrome',
    args: [
      '--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu',
      '--disable-features=LocalNetworkAccessChecks,PrivateNetworkAccessChecks',
    ],
  },
});

const ASSETS: [string, string, string][] = [
  ['**/js/app.js*', 'js/app.js', 'text/javascript'],
  ['**/js/chart-theme.js*', 'js/chart-theme.js', 'text/javascript'],
  ['**/css/style.css*', 'css/style.css', 'text/css'],
];

async function serveWorkingTree(page: Page) {
  for (const [glob, file, type] of ASSETS) {
    await page.route(glob, route => route.fulfill({
      status: 200, contentType: type, body: fs.readFileSync(path.join(STATIC, file), 'utf8'),
    }));
  }
  await page.route(url => url.pathname === '/', async route => {
    const response = await route.fetch();
    await route.fulfill({ response, body: fs.readFileSync(path.join(STATIC, 'index.html'), 'utf8') });
  });
}

// The device under test: one assigned MIB produces data, one never will.
const DEVICE = {
  id: 4,
  name: 'bastion',
  address: '198.51.100.14',
  snmp_version: 'v1',
  community: 'public',
  port: 161,
  created_at: '2026-08-09T21:36:41Z',
};

// The generate endpoint's answer, in the shape the server produces.
const SYS_UPTIME_PANEL = {
  id: 'SNMPv2-MIB_snmp_sysUpTime',
  title: 'SNMPv2-MIB - sysUpTime',
  type: 'line',
  metric: 'snmp_sysUpTime',
  query: 'snmp_sysUpTime{device="bastion"}',
  module: 'SNMPv2-MIB',
  object: 'sysUpTime',
  oid: '1.3.6.1.2.1.1.3',
  kind: 'gauge',
  description: 'Time since re-initialization.',
};

const NOT_REPORTING_NOTE =
  'ATEN-PRODUCTS-MIB is reporting nothing, so it adds no panels. A panel appears '
  + 'once the device has been polled and answered — wait one poll interval and '
  + 'generate again. If it stays empty, the device is not answering those OIDs.';

const GENERATED = {
  device: 'bastion',
  title: 'bastion - ATEN-PRODUCTS-MIB, SNMPv2-MIB',
  panels: [SYS_UPTIME_PANEL],
  mibs: [
    { mib: 'ATEN-PRODUCTS-MIB', reporting: 0, charts: 0, note: NOT_REPORTING_NOTE },
    { mib: 'SNMPv2-MIB', reporting: 1, charts: 1 },
  ],
  warnings: [NOT_REPORTING_NOTE],
};

// stubDevice answers the reads the device page and the dashboard need.
async function stubDevice(page: Page, generated: any = GENERATED) {
  await page.route(url => url.pathname === '/api/devices' && url.search === '',
    route => route.fulfill({
      status: 200, contentType: 'application/json', body: JSON.stringify({ devices: [DEVICE] }),
    }));

  await page.route(url => url.pathname === '/api/devices/4', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify(DEVICE),
  }));

  await page.route(url => url.pathname === '/api/devices/4/mibs', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({
      direct_mibs: [
        { id: 172, name: 'ATEN-PRODUCTS-MIB', object_count: 6, assignment: { enabled: true } },
        { id: 7, name: 'SNMPv2-MIB', object_count: 70, assignment: { enabled: true } },
      ],
      all_mibs: [
        { id: 172, name: 'ATEN-PRODUCTS-MIB', object_count: 6, assignment: { enabled: true } },
        { id: 7, name: 'SNMPv2-MIB', object_count: 70, assignment: { enabled: true } },
      ],
    }),
  }));

  await page.route(url => url.pathname === '/api/devices/4/dashboard', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify(generated),
  }));
}

async function pressGenerate(page: Page) {
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => (window as any).app.showDeviceDetails(4));
  await page.locator('#generateDashboardBtn').click();
}

test('the button leaves the dashboard showing the device, not an empty frame', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDevice(page);
  await pressGenerate(page);

  // The regression: the view switched and then the renderer threw, so the device
  // was never selected and no MIB was ever shown.
  await expect.poll(
    () => page.evaluate(() => (document.getElementById('dashboardDeviceSelect') as HTMLSelectElement)?.value),
    { timeout: 15000, message: 'the dashboard must end up pointed at the device the button was pressed on' },
  ).toBe('4');

  await expect(page.locator('#mibTabsContainer')).toContainText('SNMPv2-MIB', { timeout: 15000 });

  // Nothing may have thrown: the failure notification is how the old bug
  // announced itself, once the view had already changed.
  await expect(page.locator('#notificationContainer')).not.toContainText('Failed to generate dashboard');
});

test('a MIB contributing nothing says so, and does not claim it never will', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDevice(page);
  await pressGenerate(page);

  const notifications = page.locator('#notificationContainer');
  await expect(notifications).toContainText('ATEN-PRODUCTS-MIB is reporting nothing',
    { timeout: 15000 });
  // The actionable half: this may resolve itself, so wait rather than go
  // hunting for a fault.
  await expect(notifications).toContainText('wait one poll interval');

  // And it must NOT claim permanence. That claim needed the MIB parsed, which
  // is the coupling the catalog removed; asserting it from Prometheus alone
  // would be a guess presented as fact.
  await expect(notifications).not.toContainText('never');

  // The warning is several lines of explanation, so it must outlast the 3s a
  // one-line success message gets.
  await page.waitForTimeout(5000);
  await expect(notifications).toContainText('ATEN-PRODUCTS-MIB', { timeout: 1000 });
});

test('a dashboard with nothing to explain reports its panel count instead', async ({ page }) => {
  await serveWorkingTree(page);
  await stubDevice(page, {
    ...GENERATED,
    panels: [
      SYS_UPTIME_PANEL,
      { ...SYS_UPTIME_PANEL, id: 'SNMPv2-MIB_snmp_sysDescr', metric: 'snmp_sysDescr',
        object: 'sysDescr', title: 'SNMPv2-MIB - sysDescr' },
    ],
    mibs: [{ mib: 'SNMPv2-MIB', reporting: 2, charts: 2 }],
    warnings: [],
  });
  await pressGenerate(page);

  const notifications = page.locator('#notificationContainer');
  await expect(notifications).toContainText('Dashboard generated: 2 panels', { timeout: 15000 });
  // A warning on a healthy dashboard would train the reader to ignore all of them.
  await expect(notifications).not.toContainText('no pollable objects');
});
