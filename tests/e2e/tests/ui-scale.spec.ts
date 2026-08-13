import { test, expect, Page, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// Scale behaviour of the three surfaces a large MIB library reaches first: the
// dashboard's module navigation, the long lists, and the DESCRIPTIONs those
// lists have to clip.
//
// The UI under test is the working tree, served over the live API by
// page.route(), so these run against a deployment that has not shipped the
// files yet and keep passing once it has.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';
const STATIC = path.resolve(__dirname, '../../../internal/webui/static');
const DEVICE = 'core-01';

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

// serveWorkingTree swaps the static bundle for the one on disk. The document is
// fetched for real and only its body replaced, because a wholly synthesised
// response loses the headers that let the page talk to the API at all.
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

// consoleErrors collects what the page complains about, so a surface that only
// half rendered fails here rather than looking fine in a screenshot.
function consoleErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('console', msg => { if (msg.type() === 'error') errors.push(msg.text()); });
  page.on('pageerror', err => errors.push(`pageerror: ${err.message}`));
  return errors;
}

async function openDashboard(page: Page) {
  await serveWorkingTree(page);
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  const selector = page.locator('#dashboardDeviceSelect');
  await expect(selector.locator('option', { hasText: DEVICE })).toHaveCount(1, { timeout: 30000 });
  await selector.selectOption({ label: DEVICE });
  await expect(page.locator('#mibTabList .tab-button').first()).toBeVisible({ timeout: 45000 });
}

async function openView(page: Page, buttonId: string) {
  await serveWorkingTree(page);
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#dashboardDeviceSelect option')).not.toHaveCount(1, { timeout: 30000 });
  await page.locator(buttonId).click();
}

// horizontalOverflow is the measurement the old tab strip failed: a navigation
// that needs more width than the window has pushes the whole page sideways.
async function horizontalOverflow(page: Page): Promise<number> {
  return page.evaluate(() =>
    document.documentElement.scrollWidth - document.documentElement.clientWidth);
}

// ---------------------------------------------------------------------------
// A device carrying ~200 modules, synthesised over the real payload.
// ---------------------------------------------------------------------------

const SYNTHETIC_COUNT = 200;
const NEEDLE = 'ZEBRA-PATH-TRACE-MIB';

function syntheticMibs() {
  const vendors = ['ACME', 'ALPHA', 'BRAVO', 'DELTA', 'ECHO', 'FOXTROT', 'GOLF', 'HOTEL',
    'INDIA', 'JULIET', 'KILO', 'LIMA', 'MIKE', 'NOVEMBER', 'OSCAR', 'PAPA', 'QUEBEC',
    'ROMEO', 'SIERRA', 'TANGO', 'UNIFORM', 'VICTOR', 'WHISKEY', 'XRAY', 'YANKEE'];
  const facets = ['ENTITY', 'IF', 'POWER', 'QOS', 'ROUTE', 'SENSOR', 'STACK', 'TUNNEL'];
  const mibs: { id: number; name: string; object_count: number; assignment: { enabled: boolean } }[] = [];
  for (let i = 0; i < SYNTHETIC_COUNT - 1; i++) {
    const name = `${vendors[i % vendors.length]}-${facets[Math.floor(i / vendors.length) % facets.length]}`
      + `-${Math.floor(i / (vendors.length * facets.length)) + 1}-MIB`;
    mibs.push({ id: 900000 + i, name, object_count: 40, assignment: { enabled: true } });
  }
  mibs.push({ id: 909999, name: NEEDLE, object_count: 12, assignment: { enabled: true } });
  return mibs.sort((a, b) => a.name.localeCompare(b.name));
}

const SYNTHETIC_SPEC = {
  scalar_widgets: [{
    name: 'deviceUptime', type: 'status', oid: '1.3.6.1.2.1.1.3.9',
    metric: 'snmp_synthetic_deviceUptime',
    description: 'How long the managed entity has been running since it was last re-initialised.',
  }],
  table_widgets: [],
};

async function serveManyModules(page: Page, mibs: ReturnType<typeof syntheticMibs>) {
  await page.route('**/api/devices/*/mibs', async route => {
    if (route.request().method() !== 'GET') return route.continue();
    const response = await route.fetch();
    const body = await response.json();
    const merged = [...(body.all_mibs || []), ...mibs].sort((a, b) => a.name.localeCompare(b.name));
    await route.fulfill({ response, body: JSON.stringify({ ...body, all_mibs: merged }) });
  });
  // The availability check carries the device it is asking about
  // (?filter=available&device=NAME), so the pattern has to admit a query
  // string rather than pin the one it had when this was written.
  await page.route(/\/api\/mibs\?filter=available/, async route => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, body: JSON.stringify({ mibs: [...(body.mibs || []), ...mibs] }) });
  });
  await page.route(/\/api\/mibs\/9\d{5}\/dashboard/, route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify(SYNTHETIC_SPEC),
  }));
}

// ---------------------------------------------------------------------------

test.describe('dashboard MIB navigation', () => {
  // Nothing in this file writes, but the fixture device the rest of the suite
  // mutates is checked back to the state it was found in either way.
  let raritanId: string;
  let baseline: string[];

  async function directMibIds(request: APIRequestContext, deviceId: string): Promise<string[]> {
    const response = await request.get(`${BASE}/api/devices/${deviceId}/mibs`);
    expect(response.ok(), `device MIBs status ${response.status()}`).toBeTruthy();
    const body = await response.json();
    return (body.direct_mibs || []).map((m: { id: number }) => String(m.id)).sort();
  }

  test.beforeAll(async ({ request }) => {
    const response = await request.get(`${BASE}/api/devices`);
    expect(response.ok(), `device list status ${response.status()}`).toBeTruthy();
    const devices = (await response.json()).devices || [];
    const raritan = devices.find((d: { name: string }) => d.name === 'pdu-03');
    expect(raritan, 'fixture device pdu-03 is missing from this stack').toBeTruthy();
    raritanId = String(raritan.id);
    baseline = await directMibIds(request, raritanId);
  });

  test.afterAll(async ({ request }) => {
    expect(await directMibIds(request, raritanId),
      'pdu-03 did not end this file with the direct assignments it started with')
      .toEqual(baseline);
  });

  test('the rail lists modules vertically and asks for no horizontal room', async ({ page }) => {
    const errors = consoleErrors(page);
    await openDashboard(page);

    const tabs = page.locator('#mibTabList .tab-button');
    const count = await tabs.count();
    expect(count, 'no modules listed for the fixture device').toBeGreaterThan(0);

    // A tablist, not a row of anonymous buttons: the rail has to be navigable
    // by a screen reader as the tab set it is.
    await expect(page.locator('#mibTabList')).toHaveAttribute('role', 'tablist');
    await expect(page.locator('#mibTabList')).toHaveAttribute('aria-orientation', 'vertical');
    await expect(tabs.first()).toHaveAttribute('role', 'tab');
    await expect(tabs.first()).toHaveAttribute('aria-selected', 'true');

    // The measurement the horizontal strip failed.
    expect(await horizontalOverflow(page), 'the page scrolls sideways').toBe(0);

    const geometry = await page.evaluate(() => {
      const buttons = [...document.querySelectorAll('#mibTabList .tab-button')];
      return {
        widestRight: Math.max(...buttons.map(b => b.getBoundingClientRect().right)),
        viewport: window.innerWidth,
        stacked: buttons.length < 2
          || buttons[1].getBoundingClientRect().top > buttons[0].getBoundingClientRect().top,
      };
    });
    expect(geometry.stacked, 'modules are still laid out in a row').toBe(true);
    expect(geometry.widestRight, 'a module runs off the right edge')
      .toBeLessThanOrEqual(geometry.viewport);

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('filtering reaches a module without disturbing the selection', async ({ page }) => {
    await openDashboard(page);

    const selectedBefore = await page.locator('#mibTabList .tab-button.active').textContent();
    const paneBefore = await page.locator('.tab-pane.active').getAttribute('id');

    await page.fill('#mibTabSearch', 'bgp');
    const visible = page.locator('#mibTabList .tab-button:not(.is-filtered-out)');
    await expect(visible).toHaveCount(1);
    await expect(visible.first()).toHaveText('CISCO-BGP4-MIB');
    await expect(page.locator('#mibTabCount')).toContainText('match');

    // Hiding a row is a way to reach another module, not a way to close the
    // one being read: the selection and the pane are untouched.
    await expect(page.locator('.tab-pane.active')).toHaveAttribute('id', paneBefore!);
    expect(await page.locator('#mibTabList .tab-button.active').textContent()).toBe(selectedBefore);

    // A filter that matches nothing says so rather than showing an empty rail.
    await page.fill('#mibTabSearch', 'no-such-module');
    await expect(page.locator('#mibTabNoMatch')).toBeVisible();

    await page.fill('#mibTabSearch', '');
    await expect(page.locator('#mibTabNoMatch')).toBeHidden();
    await expect(page.locator('#mibTabList .tab-button:not(.is-filtered-out)'))
      .toHaveCount(await page.locator('#mibTabList .tab-button').count());
  });

  test('the rail is operable from the keyboard alone', async ({ page }) => {
    await openDashboard(page);
    const names = await page.locator('#mibTabList .tab-button').allTextContents();
    expect(names.length, 'need at least two modules to move between').toBeGreaterThan(1);

    await page.locator('#mibTabSearch').focus();
    await page.keyboard.press('ArrowDown');
    await expect(page.locator('#mibTabList .tab-button').first()).toBeFocused();

    await page.keyboard.press('ArrowDown');
    await expect(page.locator('#mibTabList .tab-button').nth(1)).toBeFocused();
    // Focus and selection are separate: opening a module costs a dashboard
    // fetch, so scanning the list must not trigger one per keypress.
    await expect(page.locator('#mibTabList .tab-button').first()).toHaveClass(/active/);

    await page.keyboard.press('Enter');
    await expect(page.locator('#mibTabList .tab-button').nth(1)).toHaveClass(/active/);
    await expect(page.locator('#mibTabList .tab-button').nth(1))
      .toHaveAttribute('aria-selected', 'true');
    await expect(page.locator(`#mib-tab-${names[1]}`)).toBeVisible();

    await page.keyboard.press('Home');
    await expect(page.locator('#mibTabList .tab-button').first()).toBeFocused();
  });

  test('two hundred modules stay reachable, in order, on one screen', async ({ page }) => {
    test.setTimeout(180000);
    const mibs = syntheticMibs();

    await serveWorkingTree(page);
    await serveManyModules(page, mibs);
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    const selector = page.locator('#dashboardDeviceSelect');
    await expect(selector.locator('option', { hasText: DEVICE })).toHaveCount(1, { timeout: 30000 });

    const started = Date.now();
    await selector.selectOption({ label: DEVICE });
    await expect
      .poll(() => page.locator('#mibTabList .tab-button').count(), { timeout: 90000 })
      .toBeGreaterThanOrEqual(SYNTHETIC_COUNT);
    const railReadyMs = Date.now() - started;
    console.log(`rail ready with ${await page.locator('#mibTabList .tab-button').count()} modules `
      + `in ${railReadyMs}ms`);

    expect(await horizontalOverflow(page), 'the page scrolls sideways at 200 modules').toBe(0);

    // The rail holds its width and hands the overflow to its own scrollbar.
    const rail = await page.evaluate(() => {
      const list = document.getElementById('mibTabList')!;
      return {
        width: Math.round(list.getBoundingClientRect().width),
        right: Math.round(list.getBoundingClientRect().right),
        scrollHeight: list.scrollHeight,
        clientHeight: list.clientHeight,
        viewport: window.innerWidth,
      };
    });
    expect(rail.right, 'the rail runs off screen').toBeLessThan(rail.viewport);
    expect(rail.scrollHeight, 'the rail is not scrolling its overflow')
      .toBeGreaterThan(rail.clientHeight);

    // Server order, unchanged. Sorting client-side would make a module's
    // position depend on the browser rather than on the payload.
    const rendered = await page.locator('#mibTabList .tab-button').allTextContents();
    const synthetic = rendered.filter(name => name.endsWith('-MIB') && name.includes('-'));
    const expectedOrder = mibs.map(m => m.name);
    expect(rendered.filter(name => expectedOrder.includes(name)),
      'the rail re-sorted the modules the server sent').toEqual(expectedOrder);
    expect(synthetic.length).toBeGreaterThanOrEqual(SYNTHETIC_COUNT);

    // Time to find a named module among 200 by typing part of its name.
    const findStarted = Date.now();
    await page.locator('#mibTabSearch').fill('zebra');
    const visible = page.locator('#mibTabList .tab-button:not(.is-filtered-out)');
    await expect(visible).toHaveCount(1);
    await page.locator('#mibTabSearch').press('Enter');
    await expect(page.locator('#mibTabList .tab-button.active')).toHaveText(NEEDLE);
    const findMs = Date.now() - findStarted;
    console.log(`found ${NEEDLE} among ${SYNTHETIC_COUNT} modules in ${findMs}ms`);
    expect(findMs, 'finding a named module took too long').toBeLessThan(5000);

    await expect(page.locator(`#mib-tab-${NEEDLE}`)).toBeVisible();
    expect(await horizontalOverflow(page), 'the page scrolls sideways after selecting').toBe(0);
  });
});

test.describe('paged list surfaces', () => {
  test('the MIB library table searches and pages the whole library', async ({ page }) => {
    const errors = consoleErrors(page);
    await openView(page, '#mibManagerBtn');

    const search = page.locator('#storedMibsListSearch');
    await expect(search).toBeVisible({ timeout: 30000 });

    const rows = page.locator('#storedMibsList tbody tr');
    await expect(rows).toHaveCount(50);
    const footer = page.locator('#storedMibsListFooter');
    const status = page.locator('#storedMibsListStatus');
    await expect(status).toContainText('Showing 1–50 of');

    // Real pagination: a page forward, and the position moves with it.
    await footer.getByRole('button', { name: 'Next page' }).click();
    await expect(rows).toHaveCount(50);
    await expect(status).toContainText('Showing 51–100 of');
    await expect(footer.getByRole('button', { name: /^Page 2 of \d+$/ }))
      .toHaveAttribute('aria-current', 'page');

    // How many modules are in the library is a property of the deployment, not
    // of the pager, and it grows with every import. The pager has to report it
    // correctly; pinning the number here only dates the test.
    const total = (await status.textContent() || '').match(/of ([\d,]+) modules/)?.[1];
    expect(total, `the pager did not state a total: ${await status.textContent()}`).toBeTruthy();

    // A search covers the whole library, not just the rows on screen, and it
    // re-pages from the first page rather than leaving the reader on a page
    // the narrower set no longer has.
    await search.fill('cisco-entity-vendortype');
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText('CISCO-ENTITY-VENDORTYPE-OID-MIB');
    await expect(status).toHaveText(`Showing 1 of 1 modules matching, ${total} in total`);

    // Tags are searchable too, and an unmatched term says so rather than
    // leaving an empty table.
    await search.fill('zzz-no-such-module');
    await expect(page.locator('#storedMibsListBody .empty-state')).toBeVisible();
    await expect(status).toContainText(`${total} in total`);

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('a module with thousands of objects is paged, and the pager stays a fixed size',
    async ({ page }) => {
      test.setTimeout(180000);
      const errors = consoleErrors(page);
      await openView(page, '#mibBrowserBtn');

      await page.fill('#mibSearchInput', 'CISCO-ENTITY-VENDORTYPE-OID-MIB');
      const item = page.locator('.mib-accordion-item:not(.hidden)');
      await expect(item).toHaveCount(1, { timeout: 30000 });
      await item.locator('.mib-accordion-header').click();

      const rows = item.locator('.mib-object-item');
      const nav = item.locator('.pager-nav');
      const status = item.locator('.pager-status');
      await expect(rows.first()).toBeVisible({ timeout: 120000 });

      // 10,539 objects at 200 a page is 53 pages, and the pager says so
      // without printing 53 buttons: first, last, a window of five around the
      // page you are on, and a gap marker for the runs it left out.
      await expect(rows).toHaveCount(200);
      await expect(status).toHaveText('Showing 1–200 of 10,539 objects');
      await expect(nav.locator('.pager-page')).toHaveCount(7);
      await expect(nav.locator('.pager-page').last()).toHaveText('53');
      await expect(nav.locator('.pager-gap')).toHaveCount(1);
      await expect(nav.getByRole('button', { name: 'Previous page' })).toBeDisabled();

      // The nav is a landmark with a name of its own, because several modules
      // can be expanded at once and each carries a pager.
      await expect(nav).toHaveAttribute(
        'aria-label', 'CISCO-ENTITY-VENDORTYPE-OID-MIB objects pagination');

      // A page in the middle, reached directly rather than by 26 clicks.
      await nav.locator('.pager-jump-input').fill('27');
      await nav.getByRole('button', { name: 'Go' }).click();
      await expect(status).toHaveText('Showing 5,201–5,400 of 10,539 objects');
      await expect(rows).toHaveCount(200);
      // Deep in the set the strip is no bigger than it was at the start.
      await expect(nav.locator('.pager-page')).toHaveCount(7);
      await expect(nav.locator('.pager-gap')).toHaveCount(2);

      // The last page is partial, and the statement is exact about it.
      await nav.locator('.pager-page').last().click();
      await expect(status).toHaveText('Showing 10,401–10,539 of 10,539 objects');
      await expect(rows).toHaveCount(139);
      await expect(nav.getByRole('button', { name: 'Next page' })).toBeDisabled();
      await expect(nav.getByRole('button', { name: 'Page 53 of 53' }))
        .toHaveAttribute('aria-current', 'page');

      // Searching re-pages from the first page and re-counts against the
      // matches, naming the set behind them.
      const objectSearch = item.locator('.paged-list-search');
      await objectSearch.fill('cevChassisAsr9006');
      await expect
        .poll(() => rows.count(), { timeout: 20000, message: 'the object search matched nothing' })
        .toBeGreaterThan(0);
      expect(await rows.count(), 'the object search did not narrow the list').toBeLessThan(200);
      await expect(status).toContainText('Showing 1–');
      await expect(status).toContainText('10,539 in total');

      expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
    });

  test('paging forward and back lands on the same rows, and rows-per-page keeps your place',
    async ({ page }) => {
      test.setTimeout(180000);
      await openView(page, '#mibBrowserBtn');

      await page.fill('#mibSearchInput', 'CISCO-ENTITY-VENDORTYPE-OID-MIB');
      const item = page.locator('.mib-accordion-item:not(.hidden)');
      await expect(item).toHaveCount(1, { timeout: 30000 });
      await item.locator('.mib-accordion-header').click();

      const names = item.locator('.mib-object-name');
      const nav = item.locator('.pager-nav');
      const status = item.locator('.pager-status');
      await expect(names.first()).toBeVisible({ timeout: 120000 });

      // The reveal this replaced could only go forward. A page is a place:
      // leaving it and coming back has to put the same rows on screen.
      await nav.locator('.pager-jump-input').fill('10');
      await nav.getByRole('button', { name: 'Go' }).click();
      await expect(status).toHaveText('Showing 1,801–2,000 of 10,539 objects');
      const onTen = await names.allTextContents();
      expect(onTen).toHaveLength(200);

      await nav.getByRole('button', { name: 'Next page' }).click();
      await expect(status).toHaveText('Showing 2,001–2,200 of 10,539 objects');
      const onEleven = await names.allTextContents();
      expect(onEleven, 'the next page repeated the previous one').not.toEqual(onTen);

      await nav.getByRole('button', { name: 'Previous page' }).click();
      await expect(status).toHaveText('Showing 1,801–2,000 of 10,539 objects');
      expect(await names.allTextContents(),
        'stepping back did not return to the same rows').toEqual(onTen);

      // Server order is the payload's: page 10 followed by page 11 is one
      // unbroken run, so nothing was sorted or skipped.
      await nav.getByRole('button', { name: 'Next page' }).click();
      await expect(status).toHaveText('Showing 2,001–2,200 of 10,539 objects');
      expect(new Set([...onTen, ...onEleven]).size,
        'the two pages overlap').toBe(onTen.length + onEleven.length);

      // Rows per page re-cuts the set without moving the reader: the first row
      // of the page they were on is still on the page they land on.
      await nav.getByRole('button', { name: 'Previous page' }).click();
      await item.locator('.pager-size-select').selectOption('400');
      await expect(status).toHaveText('Showing 1,601–2,000 of 10,539 objects');
      expect(await names.allTextContents(), 'the wider page lost the rows it was showing')
        .toEqual(expect.arrayContaining(onTen));
    });

  test('the pager is announced and keeps the keyboard where it left it', async ({ page }) => {
    test.setTimeout(180000);
    await openView(page, '#mibBrowserBtn');

    await page.fill('#mibSearchInput', 'CISCO-ENTITY-VENDORTYPE-OID-MIB');
    const item = page.locator('.mib-accordion-item:not(.hidden)');
    await expect(item).toHaveCount(1, { timeout: 30000 });
    await item.locator('.mib-accordion-header').click();

    const nav = item.locator('.pager-nav');
    const status = item.locator('.pager-status');
    await expect(item.locator('.mib-object-item').first()).toBeVisible({ timeout: 120000 });

    // The position statement is a live region, so a page change is heard and
    // not merely seen. It is written in place rather than replaced, because a
    // region inserted at the same moment as its text is not reliably announced.
    await expect(status).toHaveAttribute('role', 'status');
    await expect(status).toHaveAttribute('aria-live', 'polite');
    const statusNode = await status.elementHandle();

    // Every control is a real button: focused from the keyboard and fired with
    // Enter, with no key handling of our own standing in for that.
    const next = nav.getByRole('button', { name: 'Next page' });
    await next.focus();
    await page.keyboard.press('Enter');
    await expect(status).toHaveText('Showing 201–400 of 10,539 objects');

    // The repaint destroys the button that was pressed, so focus is put on its
    // replacement instead of being dropped at the top of the document.
    expect(await page.evaluate(
      () => (document.activeElement as HTMLElement)?.getAttribute('data-pager-rel'),
    ), 'focus was lost when the pager repainted').toBe('next');

    // Space works too, and repeated presses keep stepping without the reader
    // having to find the control again.
    await page.keyboard.press('Space');
    await expect(status).toHaveText('Showing 401–600 of 10,539 objects');

    // The live region survived both repaints as the same node.
    expect(await status.elementHandle().then(h => h!.evaluate(
      (el, old) => el === old, statusNode)),
      'the status was replaced rather than written in place').toBe(true);

    // At the end of the set the step it came from is disabled, so focus lands
    // on the page the reader is now on rather than on a refused control.
    await nav.locator('.pager-page').last().click();
    await expect(nav.getByRole('button', { name: 'Next page' })).toBeDisabled();
    await nav.getByRole('button', { name: 'Previous page' }).focus();
    await page.keyboard.press('Enter');
    await expect(status).toHaveText('Showing 10,201–10,400 of 10,539 objects');
    expect(await page.evaluate(
      () => (document.activeElement as HTMLElement)?.getAttribute('aria-label'),
    )).toBe('Previous page');
  });

  test('the assign-MIB modal searches the library and the bulk picker keeps its ticks',
    async ({ page }) => {
      await openView(page, '#devicesBtn');
      await page.locator('#devicesList tr', { hasText: DEVICE })
        .getByRole('button', { name: 'View' }).first().click();
      await expect(page.locator('#deviceDetailsTitle')).toHaveText(DEVICE, { timeout: 30000 });

      await page.locator('#assignMibBtn').click();
      const modalSearch = page.locator('#assignModalBodySearch');
      await expect(modalSearch).toBeVisible({ timeout: 20000 });
      const offered = page.locator('#assignModalBody .list-item');
      await expect(offered).toHaveCount(50);
      await modalSearch.fill('cisco-envmon');
      await expect(offered).toHaveCount(1);
      await page.locator('#assignModal .close-modal').click();
      await expect(page.locator('#assignModal')).toBeHidden();

      // The bulk picker pages, so a tick has to survive the search that pages
      // it away — otherwise the reader silently loses selections.
      await page.locator('#bulkAssignMibBtn').click();
      const bulkSearch = page.locator('#bulkAssignMibListSearch');
      await expect(bulkSearch).toBeVisible({ timeout: 20000 });

      await bulkSearch.fill('atm-forum');
      const boxes = page.locator('#bulkAssignMibList .checkbox-row input');
      await expect(boxes).toHaveCount(1);
      await boxes.first().check();
      await expect(page.locator('#bulkAssignMibCount')).toHaveText('1 module selected');

      await bulkSearch.fill('bgp4-mib');
      await expect(page.locator('#bulkAssignMibCount')).toHaveText('1 module selected');
      await expect(page.locator('#bulkAssignMibList .checkbox-row input').first()).not.toBeChecked();

      await bulkSearch.fill('atm-forum');
      await expect(page.locator('#bulkAssignMibList .checkbox-row input').first()).toBeChecked();
      await page.locator('#bulkAssignMibList .checkbox-row input').first().uncheck();
      await expect(page.locator('#bulkAssignMibCount')).toHaveText('No modules selected');

      // ... and it has to survive being paged away too, which is the case the
      // "show more" reveal never had to answer because it could not go back.
      await bulkSearch.fill('');
      const bulkNav = page.locator('#bulkAssignMibListPager');
      const bulkStatus = page.locator('#bulkAssignMibListStatus');
      await expect(bulkStatus).toContainText('Showing 1–50 of');

      const rows = page.locator('#bulkAssignMibList .checkbox-row');
      const names = () => rows.locator('span:not(.muted)').allTextContents();
      const onPageOne = await names();
      expect(onPageOne).toHaveLength(50);

      await rows.first().locator('input').check();
      await expect(page.locator('#bulkAssignMibCount')).toHaveText('1 module selected');

      await bulkNav.getByRole('button', { name: 'Next page' }).click();
      await expect(bulkStatus).toContainText('Showing 51–100 of');
      const onPageTwo = await names();
      expect(onPageTwo, 'the second page repeated the first').not.toEqual(onPageOne);
      await expect(rows.first().locator('input'),
        'a different page arrived pre-ticked').not.toBeChecked();
      await expect(page.locator('#bulkAssignMibCount')).toHaveText('1 module selected');

      await bulkNav.getByRole('button', { name: 'Previous page' }).click();
      await expect(bulkStatus).toContainText('Showing 1–50 of');
      expect(await names(), 'coming back landed on different rows').toEqual(onPageOne);
      await expect(rows.first().locator('input'),
        'the tick was lost by paging away from it').toBeChecked();
      await expect(page.locator('#bulkAssignMibCount')).toHaveText('1 module selected');

      // Nothing is assigned: the dialog is dismissed without submitting.
      await page.locator('#modalOverlay .close-modal').click();
      await expect(page.locator('#modalOverlay')).toBeHidden();
    });

  test('the applied-module list on a device page is searchable', async ({ page }) => {
    await openView(page, '#devicesBtn');
    await page.locator('#devicesList tr', { hasText: DEVICE })
      .getByRole('button', { name: 'View' }).first().click();
    await expect(page.locator('#deviceDetailsTitle')).toHaveText(DEVICE, { timeout: 30000 });

    const search = page.locator('#deviceAllMibsSearch');
    await expect(search).toBeVisible({ timeout: 20000 });
    const rows = page.locator('#deviceAllMibs .mib-row');
    const before = await rows.count();
    expect(before, 'the fixture device should have applied modules').toBeGreaterThan(1);

    await search.fill('snmpv2');
    await expect
      .poll(() => rows.count(), { timeout: 10000 })
      .toBeLessThan(before);
    await expect(rows.first()).toContainText('SNMPv2');
  });

  // The object picker holds edits in a draft rather than in the checkboxes, so
  // paging away from a row it has just changed must not take the change with
  // it. This is the one place a paging bug loses the reader's input silently.
  test('an object picker edit survives being paged away and back', async ({ page }) => {
    // Read-only: core-01 is polling production routers, so the assignment
    // PUT is blocked at the network layer and the dialog is cancelled. Nothing
    // below saves, but a regression that tried to would fail here rather than
    // reconfigure a live device.
    const writes: string[] = [];
    await page.route('**/api/devices/*/mibs/*/assignment', async route => {
      if (route.request().method() === 'PUT') {
        writes.push(route.request().url());
        await route.abort();
        return;
      }
      await route.fallback();
    });

    await openView(page, '#devicesBtn');
    await page.locator('#devicesList tr', { hasText: DEVICE })
      .getByRole('button', { name: 'View' }).first().click();
    await expect(page.locator('#deviceDetailsTitle')).toHaveText(DEVICE, { timeout: 30000 });

    await page.locator('#deviceAllMibs .mib-row', { hasText: 'IP-MIB' }).first()
      .getByRole('button', { name: 'Objects' }).click();
    await expect(page.locator('#objPickerList .obj-row').first()).toBeVisible({ timeout: 30000 });

    // IP-MIB's 293 objects fold into 100 top-level rows; every one of them is
    // shown, which is two pages.
    await page.locator('#objPickerShow').selectOption('all');
    const nav = page.locator('#objPickerPager');
    const status = page.locator('#objPickerStatus');
    await expect(status).toHaveText('Showing 1–50 of 100 rows');
    await expect(nav.locator('.pager-page')).toHaveCount(2);

    // Untick the first row that carries a real control, and record which it is.
    const box = page.locator('#objPickerList .obj-row input[type=checkbox]').first();
    const oid = await box.evaluate(el => (el as HTMLInputElement).dataset.objectOid!);
    const totals = page.locator('#objPickerTotal');
    const before = await totals.textContent();
    await box.uncheck();
    await expect(totals, 'unticking did not change the running total').not.toHaveText(before!);
    const edited = await totals.textContent();

    // Page away. The row and its checkbox are gone from the document entirely.
    await nav.getByRole('button', { name: 'Next page' }).click();
    await expect(status).toHaveText('Showing 51–100 of 100 rows');
    await expect(page.locator(`#objPickerList .obj-row[data-oid="${oid}"]`)).toHaveCount(0);
    await expect(totals, 'the edit was dropped when its row was paged away')
      .toHaveText(edited!);

    // ... and back. The tick is off because the draft says so, and the row is
    // still marked as pending an unsaved change.
    await nav.getByRole('button', { name: 'Previous page' }).click();
    await expect(status).toHaveText('Showing 1–50 of 100 rows');
    const again = page.locator(`#objPickerList .obj-row[data-oid="${oid}"]`);
    await expect(again).toHaveCount(1);
    await expect(again.locator('input[type=checkbox]'),
      'the edit was lost by paging away from it').not.toBeChecked();
    await expect(again).toHaveClass(/is-pending/);
    await expect(totals).toHaveText(edited!);

    // Searching re-pages from the first page and counts the matches, and the
    // edit is still there when the search is cleared.
    await page.locator('#objPickerSearch').fill('ipNetToPhysical');
    await expect(status).toContainText('Showing 1–');
    await expect(status).toContainText('100 in total');
    await page.locator('#objPickerSearch').fill('');
    await expect(status).toHaveText('Showing 1–50 of 100 rows');
    await expect(again.locator('input[type=checkbox]')).not.toBeChecked();

    await page.locator('#objPickerCancelBtn').click();
    await expect(page.locator('#modalOverlay')).toBeHidden();
    expect(writes, `the picker wrote to ${DEVICE}: ${writes.join(', ')}`).toHaveLength(0);
  });
});

test.describe('descriptions', () => {
  test('a clipped object description is read in full in the sheet', async ({ page }) => {
    const errors = consoleErrors(page);
    await openView(page, '#mibBrowserBtn');

    await page.fill('#mibSearchInput', 'HOST-RESOURCES-MIB');
    const item = page.locator('.mib-accordion-item:not(.hidden)');
    await expect(item).toHaveCount(1, { timeout: 30000 });
    await item.locator('.mib-accordion-header').click();

    const trigger = item.locator('.mib-object-description.desc-trigger').first();
    await expect(trigger).toBeVisible({ timeout: 30000 });

    // Clipped in the row: the column stays a column.
    expect(await trigger.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('nowrap');
    const clipped = await trigger.evaluate(el => ({
      shown: el.clientWidth, full: el.scrollWidth, text: el.textContent!.trim(),
    }));
    expect(clipped.full, 'pick a row whose description actually overflows')
      .toBeGreaterThan(clipped.shown);

    await trigger.click();
    const sheet = page.locator('#descSheet');
    await expect(sheet).toBeVisible();
    await expect(page.locator('#descSheetTitle')).not.toBeEmpty();
    await expect(page.locator('#descSheetMeta')).toContainText('OID');

    // The same DESCRIPTION, laid out so all of it can actually be read: no
    // clipping, wrapped over several lines, and selectable.
    const body = page.locator('#descSheetBody');
    const rendered = await body.evaluate(el => ({
      text: el.textContent!.replace(/\s+/g, ' ').trim(),
      whiteSpace: getComputedStyle(el).whiteSpace,
      userSelect: getComputedStyle(el).userSelect,
      clipped: el.scrollWidth > el.clientWidth,
      height: el.getBoundingClientRect().height,
      lineHeight: parseFloat(getComputedStyle(el).lineHeight),
    }));
    expect(rendered.text).toBe(clipped.text.replace(/\s+/g, ' ').trim());
    expect(rendered.whiteSpace, 'the sheet clipped the text too').not.toBe('nowrap');
    expect(rendered.clipped, 'the sheet clipped the text too').toBe(false);
    expect(rendered.height, 'the sheet still showed only one line')
      .toBeGreaterThan(rendered.lineHeight * 1.5);
    expect(rendered.userSelect, 'the text cannot be selected or copied').toBe('text');

    // Esc closes it and gives focus back to the row it was opened from.
    await page.keyboard.press('Escape');
    await expect(sheet).toBeHidden();
    await expect(trigger).toBeFocused();

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('a chart card keeps its one-line caption and still gives up the full text',
    async ({ page }) => {
      await openDashboard(page);

      const caption = page.locator('.tab-pane.active .chart-description').first();
      await expect(caption).toBeVisible({ timeout: 45000 });

      // The single line is load-bearing: it is what makes every plot in a row
      // start at the same height. It must survive this change.
      expect(await caption.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('nowrap');
      const lineHeight = await caption.evaluate(el => el.getBoundingClientRect().height);

      await caption.click();
      await expect(page.locator('#descSheet')).toBeVisible();
      const full = await page.locator('#descSheetBody').textContent();
      expect(full!.trim().length, 'the sheet was empty').toBeGreaterThan(0);

      await page.keyboard.press('Escape');
      await expect(page.locator('#descSheet')).toBeHidden();
      expect(await caption.evaluate(el => el.getBoundingClientRect().height),
        'the caption changed height').toBe(lineHeight);
    });
});
