import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// Editing a device through its form.
//
// Every edit failed with "invalid request body" whatever field was changed. The
// form read the id back out of FormData, which always yields a string, and
// storage.Device.ID is an int64 — so the JSON never decoded. It surfaced while
// changing the SNMP version, which made it look like a problem with the new
// version rather than with the id.
//
// Nothing here writes to the server: the PUT is intercepted, inspected and
// answered locally, so the assertions are about what the page sends.

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

// The device the form is opened on. Served in place of the real record so the
// test does not depend on what the deployment currently holds.
const DEVICE = {
  id: 4,
  name: 'bastion',
  address: '198.51.100.14',
  snmp_version: 'v2c',
  community: 'public',
  port: 161,
  created_at: '2026-08-09T21:36:41Z',
};

// captureSave answers the stored-device read and traps the write, returning
// what the page sent.
async function captureSave(page: Page): Promise<() => any> {
  let sent: any = null;

  await page.route(url => url.pathname === '/api/devices/4' && url.searchParams.get('stored') === 'true',
    async route => {
      if (route.request().method() === 'GET') {
        await route.fulfill({
          status: 200, contentType: 'application/json', body: JSON.stringify(DEVICE),
        });
        return;
      }
      sent = route.request().postDataJSON();
      await route.fulfill({
        status: 200, contentType: 'application/json', body: JSON.stringify({ ...DEVICE, ...sent }),
      });
    });

  return () => sent;
}

async function openEditForm(page: Page) {
  await serveWorkingTree(page);
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });

  // The modal is opened directly. Reaching it by clicking through the device
  // list would make this a test of the list as well, and the list is covered
  // elsewhere.
  await page.evaluate(() => (window as any).app.showEditDeviceModal(4));
  await expect(page.locator('#deviceModal')).toHaveClass(/active/, { timeout: 15000 });
}

test('an edit sends the device id as a number, not a string', async ({ page }) => {
  const sent = await captureSave(page);
  await openEditForm(page);

  await page.locator('#deviceForm button[type="submit"]').click();
  await expect.poll(() => sent() !== null, { timeout: 10000 }).toBe(true);

  const body = sent();
  expect(typeof body.id,
    'storage.Device.ID is an int64; a quoted id fails to decode and the server answers "invalid request body"')
    .toBe('number');
  expect(body.id).toBe(4);

  // The name field is read-only while editing, not disabled: a disabled input
  // is omitted from FormData entirely, so the name arrived null and the server
  // refused the edit with "name is required". Asserting only on the id missed
  // this the first time.
  expect(body.name, 'the read-only name must still be submitted').toBe('bastion');
  expect(body.address).toBe('198.51.100.14');
  expect(body.port).toBe(161);
});

test('changing the SNMP version to v1 sends v1 and a community string', async ({ page }) => {
  const sent = await captureSave(page);
  await openEditForm(page);

  await page.locator('#deviceSNMPVersion').selectOption('v1');
  await page.locator('#deviceForm button[type="submit"]').click();
  await expect.poll(() => sent() !== null, { timeout: 10000 }).toBe(true);

  const body = sent();
  expect(body.snmp_version).toBe('v1');
  // v1 authenticates with a community string, exactly as v2c does.
  expect(body.community).toBe('public');
  expect(body.username, 'v1 has no user').toBeUndefined();
});

test('selecting SNMPv3 swaps the community field for the credentials', async ({ page }) => {
  await captureSave(page);
  await openEditForm(page);

  const community = page.locator('#deviceCommunityFields');
  const v3 = page.locator('#deviceV3Fields');

  await expect(community).toBeVisible();
  await expect(v3).toBeHidden();

  await page.locator('#deviceSNMPVersion').selectOption('v3');
  await expect(community).toBeHidden();
  await expect(v3).toBeVisible();

  // Back again, because a reader who changes their mind must not be left with
  // a form that asks for nothing usable.
  await page.locator('#deviceSNMPVersion').selectOption('v2c');
  await expect(community).toBeVisible();
  await expect(v3).toBeHidden();
});

test('a v3 edit sends the credentials and no community string', async ({ page }) => {
  const sent = await captureSave(page);
  await openEditForm(page);

  await page.locator('#deviceSNMPVersion').selectOption('v3');
  await page.locator('#deviceForm input[name="username"]').fill('monitor-ro');
  await page.locator('#deviceForm select[name="auth_protocol"]').selectOption('SHA');
  await page.locator('#deviceForm input[name="auth_password"]').fill('auth-secret');
  await page.locator('#deviceForm select[name="priv_protocol"]').selectOption('AES');
  await page.locator('#deviceForm input[name="priv_password"]').fill('priv-secret');

  await page.locator('#deviceForm button[type="submit"]').click();
  await expect.poll(() => sent() !== null, { timeout: 10000 }).toBe(true);

  const body = sent();
  expect(body.snmp_version).toBe('v3');
  expect(body.username).toBe('monitor-ro');
  expect(body.auth_protocol).toBe('SHA');
  expect(body.priv_protocol).toBe('AES');
  expect(body.auth_password).toBe('auth-secret');
  expect(body.priv_password).toBe('priv-secret');

  // A v3 device authenticates with a user. Sending a community string as well
  // would store a secret nothing reads and that an export would carry around.
  expect(body.community, 'v3 does not use a community string').toBeUndefined();
});

test('a v3 edit with blank passwords leaves the stored ones alone', async ({ page }) => {
  const sent = await captureSave(page);
  await openEditForm(page);

  await page.locator('#deviceSNMPVersion').selectOption('v3');
  await page.locator('#deviceForm input[name="username"]').fill('monitor-ro');
  // The password fields are left empty, which is how the form always opens:
  // the stored secret is never rendered into the page.
  await page.locator('#deviceForm button[type="submit"]').click();
  await expect.poll(() => sent() !== null, { timeout: 10000 }).toBe(true);

  const body = sent();
  expect(body.username).toBe('monitor-ro');
  expect(body.auth_password,
    'a blank password means unchanged; sending "" would clear the stored one')
    .toBeUndefined();
  expect(body.priv_password).toBeUndefined();
});
