import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// An info metric with several readings on one series renders one row per reading; the working-tree UI is served via page.route().

import { BASE } from '../target';
import { SEEDED_DEVICES } from '../stack';
const STATIC = path.resolve(__dirname, '../../../internal/webui/static');
// The simulated router, whatever NETMON_DEVICE says: its sysDescr is the long
// one the clip tests need (public.snmprec).
const DEVICE = SEEDED_DEVICES[0];

// Route interception drops the metadata Chrome's Local Network Access check
// needs, and every /api call from an intercepted page is then blocked as
// cross-origin. The rest of the launch options match the project config.
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

// The system group is on the device's own tab, which sorts first and is
// therefore the tab a device opens on -- no click needed, and none wanted:
// the point is what a reader sees on arrival.
async function openDashboard(page: Page) {
  await serveWorkingTree(page);
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  const selector = page.locator('#dashboardDeviceSelect');
  await expect(selector.locator('option', { hasText: DEVICE })).toHaveCount(1, { timeout: 30000 });
  await selector.selectOption({ label: DEVICE });
  await expect(page.locator('#mibTabList .tab-button').first()).toBeVisible({ timeout: 45000 });
}

// systemCard is the snmp_system_info tile, matched by title.
function systemCard(page: Page) {
  return page.locator('.tab-pane.active .status-item')
    .filter({ has: page.locator('.panel-title', { hasText: /^system$/ }) })
    .first();
}

test.describe('an info metric carrying several readings', () => {
  test('renders one named row per reading instead of one joined string',
    async ({ page }) => {
      await openDashboard(page);

      const card = systemCard(page);
      const rows = card.locator('.panel-row.is-reading');
      await expect(rows.first()).toBeVisible({ timeout: 45000 });

      // Several readings, each one named by the label it arrived on. One row
      // would mean the join is back.
      const count = await rows.count();
      expect(count, 'the system group carries several readings and each is a row')
        .toBeGreaterThan(1);

      // toHaveCount first: not.toBeEmpty() is satisfied by an element that does
      // not exist, so on its own it would pass a row that lost its name half.
      for (let i = 0; i < count; i++) {
        const row = rows.nth(i);
        await expect(row.locator('.panel-row-name')).toHaveCount(1);
        await expect(row.locator('.panel-row-value')).toHaveCount(1);
        await expect(row.locator('.panel-row-name')).not.toBeEmpty();
        await expect(row.locator('.panel-row-value')).not.toBeEmpty();
      }

      // Assert on the whole card that sys_descr and sys_object_id are not joined into one reading.
      const whole = (await card.locator('.status-value').textContent()) || '';
      expect(whole, `the system group is still one joined string: ${whole}`)
        .not.toMatch(/sys_\w+=[\s\S]*,\s*sys_\w+=/);
    });

  test('clips the one long reading without cutting the card',
    async ({ page }) => {
      await openDashboard(page);

      const card = systemCard(page);
      await expect(card.locator('.panel-row.is-reading').first())
        .toBeVisible({ timeout: 45000 });

      const measured = await card.evaluate(el => {
        const value = el.querySelector('.status-value') as HTMLElement;
        const readings = [...el.querySelectorAll('.panel-row.is-reading .panel-row-value')]
          .map(node => ({
            clipped: node.scrollHeight > (node as HTMLElement).clientHeight + 1,
            chars: (node.textContent || '').length,
          }));
        return {
          readings,
          containerClipped: value.scrollHeight > value.clientHeight + 1,
        };
      });

      // The clamp is asserted through the geometry it produces, not through a
      // character count: how many characters take three lines is a function of
      // the card's width and the font, so a threshold here would be a second,
      // unmaintained copy of the layout that fails on a narrower tile.
      const longest = measured.readings.reduce((a, b) => (b.chars > a.chars ? b : a));
      expect(longest.clipped,
        'the longest reading is not clamped, so the clamp stopped applying').toBe(true);
      expect(measured.readings.filter(r => r.clipped),
        'only the reading too long to fit may be clipped').toHaveLength(1);

      // The tile itself must not be the thing doing the cutting. That was the
      // old failure: the container clamped at four lines and everything past
      // sysDescr was below it, unreachable.
      expect(measured.containerClipped,
        'the card clipped its own row list, hiding whole readings').toBe(false);
    });

  test('leaves a single-reading tile on the shared row layout',
    async ({ page }) => {
      await openDashboard(page);

      // IF-MIB's text tiles are one reading per interface -- the reading IS the
      // row, under the port's name -- so they must keep the layout every other
      // row uses. The reading-row treatment is only for a series that carries
      // several, and a regression here would re-align every list on the page.
      await page.locator('#mibTabList .tab-button', { hasText: 'IF-MIB' }).first().click();
      const pane = page.locator('.tab-pane.active');
      await expect(pane.locator('.status-item .panel-row').first())
        .toBeVisible({ timeout: 45000 });

      await expect(pane.locator('.status-item .panel-row.is-reading')).toHaveCount(0);
    });
});

test.describe('a reading the card had to clip', () => {
  test('opens the full text in the sheet, under its own name and not "Description"',
    async ({ page }) => {
      await openDashboard(page);

      const card = systemCard(page);
      const clipped = card.locator('.panel-row.is-reading .panel-row-value.is-clipped');
      await expect(clipped).toHaveCount(1, { timeout: 45000 });

      // Only the reading that was actually cut advertises the reveal. The two
      // that fit are still openable -- a value worth reading is worth copying
      // -- but must not carry the underline, or the affordance stops meaning
      // "there is more here than you can see".
      const marks = await card.evaluate(el =>
        [...el.querySelectorAll('.panel-row.is-reading .panel-row-value')].map(v => ({
          clipped: v.classList.contains('is-clipped'),
          underlined: getComputedStyle(v).textDecorationLine !== 'none',
        })));
      for (const mark of marks) {
        expect(mark.underlined,
          'the underline is the affordance and belongs only on a clipped reading')
          .toBe(mark.clipped);
      }

      // Read the clipped row's two halves together, off the row itself. A
      // filter({ has: ... }) on a locator already scoped to the card resolves
      // against the wrong root and matches nothing.
      const row = await card.evaluate(el => {
        const value = el.querySelector('.panel-row.is-reading .panel-row-value.is-clipped');
        return {
          name: value.closest('.panel-row').querySelector('.panel-row-name').textContent.trim(),
          full: value.textContent.trim(),
        };
      });
      await clipped.click();

      const sheet = page.locator('#descSheet');
      await expect(sheet).toBeVisible();

      // A reading must not open under a Description heading.
      await expect(page.locator('#descSheetTitle')).toHaveText(row.name);
      await expect(page.locator('#descSheetTitle')).not.toHaveText('Description');
      await expect(page.locator('#descSheetEyebrow')).toHaveText('Reading');

      // The sheet shows the reading uncut, compared on collapsed whitespace since the sheet reflows it.
      const collapse = (s: string) => s.replace(/\s+/g, ' ').trim();
      const revealed = await page.locator('#descSheetBody').evaluate(el => ({
        text: el.textContent || '',
        clipped: el.scrollHeight > el.clientHeight + 1,
      }));
      expect(collapse(revealed.text), 'the sheet did not carry the whole reading')
        .toBe(collapse(row.full));
      expect(revealed.clipped,
        'the sheet clipped the very reading it exists to reveal').toBe(false);
    });
});
