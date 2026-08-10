import { test, expect, Page, APIRequestContext } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// Per-object polling controls: the "Objects" action on a device's MIB row
// opens a picker that ticks a box for every object the device polls, explains
// every box it does not tick, and edits the module's filter patterns.
//
// The contract this spec defends is that the ticks come from the server. GET
// /api/devices/{id}/mibs/{mibId}/objects runs the poller's own selection code,
// so a box is ticked if and only if that endpoint said `polled`, both when the
// picker opens and again after every save. Anything the UI worked out for
// itself could drift from what is actually collected, which is the bug the
// endpoint exists to prevent.
//
// The second contract is that every control in the list is a control. An
// unfiltered module has every pollable object polled, so most of a table-heavy
// module is columns arriving on a table's walk and objects the agent can never
// return — 269 of IP-MIB's 293 on a live router. Those are shown as the states
// they are in, never as checkboxes that refuse to be clicked, and the list
// opens on the rows that can be operated with the rest a click away.
//
// The device under test is "pdu-03": it carries no profile and no MIB
// assignments, so nothing here can disturb the "Core Routers" profile that the
// live-monitored core-01/02 inherit. The one test that needs a
// profile-sourced module opens core-01 read-only, and physically blocks the
// assignment PUT at the network layer so a regression cannot write to it.
//
// No id is hardcoded: the database is rebuilt and renumbered regularly, so the
// device and the MIB are resolved by name before the first test, the direct
// assignment is created if it is not already there, and teardown puts back
// exactly what was found.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';
const DEVICE_NAME = 'pdu-03';
const PROFILE_DEVICE_NAME = 'core-01';

// UDP-MIB is small (31 objects) and always present, and it carries at least
// one table, so every reason this picker renders — polled, not-accessible and
// covered-by-table-walk — is reachable without a large module.
const MIB_NAME = 'UDP-MIB';

// Serving the working tree instead of the deployed bundle. The stack is
// redeployed on its own schedule, so NETMON_STATIC=/path/to/static runs this
// spec against unshipped changes; unset, it tests what is deployed.
const STATIC_DIR = process.env.NETMON_STATIC || '';

let deviceId = 0;
let profileDeviceId = 0;
let mibId = 0;

// preexistingAssignment records what was there before this spec ran: null
// means the MIB was not directly assigned and teardown has to remove it again.
let preexistingAssignment: Record<string, unknown> | null = null;

async function serveWorkingTree(page: Page) {
  if (!STATIC_DIR) return;
  const assets: Array<[string, string, string]> = [
    ['**/js/app.js*', 'js/app.js', 'text/javascript'],
    ['**/js/chart-theme.js*', 'js/chart-theme.js', 'text/javascript'],
    ['**/css/style.css*', 'css/style.css', 'text/css'],
  ];
  for (const [glob, file, contentType] of assets) {
    const full = path.join(STATIC_DIR, file);
    await page.route(glob, route =>
      route.fulfill({ status: 200, contentType, body: fs.readFileSync(full, 'utf8') }));
  }
}

function consoleErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('console', msg => { if (msg.type() === 'error') errors.push(msg.text()); });
  page.on('pageerror', err => errors.push(`pageerror: ${err.message}`));
  return errors;
}

// recordAssignmentPuts collects the body of every assignment write the page
// makes, which is how "Cancel wrote nothing" is proved rather than assumed.
function recordAssignmentPuts(page: Page): string[] {
  const puts: string[] = [];
  page.on('request', req => {
    if (req.method() === 'PUT' && req.url().includes('/assignment')) puts.push(req.url());
  });
  return puts;
}

async function resolveId(
  request: APIRequestContext, listPath: string, key: string, name: string,
): Promise<number> {
  const response = await request.get(`${BASE}${listPath}`);
  expect(response.ok(), `GET ${listPath} status ${response.status()}`).toBeTruthy();

  const body = await response.json();
  const records: Array<{ id: number; name: string }> = body[key] || [];
  const match = records.find(record => record.name === name);
  expect(match,
    `fixture "${name}" was not found in ${listPath}; available: ${records.map(r => r.name).join(', ')}`,
  ).toBeTruthy();
  return match!.id;
}

async function directAssignment(request: APIRequestContext): Promise<Record<string, unknown> | null> {
  const response = await request.get(`${BASE}/api/devices/${deviceId}/mibs/${mibId}/assignment`);
  if (response.status() === 404) return null;
  expect(response.ok(), `assignment fetch status ${response.status()}`).toBeTruthy();
  return response.json();
}

async function getAssignment(request: APIRequestContext): Promise<Record<string, unknown>> {
  const assignment = await directAssignment(request);
  expect(assignment, `${MIB_NAME} is no longer directly assigned to ${DEVICE_NAME}`).not.toBeNull();
  return assignment!;
}

// resetAssignment forces the fixture back to enabled with no filters, which is
// the state every test starts from and the state the suite must leave behind.
async function resetAssignment(request: APIRequestContext) {
  const response = await request.put(`${BASE}/api/devices/${deviceId}/mibs/${mibId}/assignment`, {
    data: { enabled: true }
  });
  expect(response.ok(), `reset status ${response.status()}`).toBeTruthy();
}

async function openDevice(page: Page, name: string) {
  await serveWorkingTree(page);
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });

  // The dashboard's device selector is filled during boot, so more than the
  // placeholder option means the app is up and its nav buttons are wired —
  // clicking "Devices" any earlier can land on markup with no handler yet.
  await page.waitForFunction(
    () => document.querySelectorAll('#dashboardDeviceSelect option').length > 1,
    null, { timeout: 30000 });

  await page.locator('#devicesBtn').click();
  const row = page.locator('#devicesList tr', { hasText: name });
  const view = row.getByRole('button', { name: 'View' }).first();
  await expect(view).toBeVisible({ timeout: 25000 });
  await view.click();
  await expect(page.locator('#deviceDetailsTitle')).toHaveText(name, { timeout: 25000 });
}

// openPicker clicks the Objects action for one MIB and waits for the plan.
async function openPicker(page: Page, container: string, mibName: string) {
  await expect(page.locator(`${container} .mib-row`, { hasText: mibName }).first())
    .toBeVisible({ timeout: 25000 });
  await page.locator(`${container} .mib-row`, { hasText: mibName })
    .first().getByRole('button', { name: 'Objects' }).click();

  await expect(page.locator('#modalOverlay')).toBeVisible({ timeout: 15000 });
  await expect(page.locator('#modalOverlayTitle')).toHaveText(`Objects: ${mibName}`);
  await expect(page.locator('#objPickerList .obj-row').first()).toBeVisible({ timeout: 25000 });
}

// plan reads the payload the picker is rendering from, so assertions compare
// the DOM against the server's answer rather than against another guess.
async function plan(page: Page) {
  return page.evaluate(() => {
    const state = (window as any).app.objectPicker;
    return {
      source: state.plan.source,
      total: state.plan.total,
      polled: state.plan.polled,
      objects: state.plan.objects.map((o: any) => ({
        oid: o.oid, name: o.name, kind: o.kind, polled: o.polled, reason: o.reason, series: o.series,
      })),
    };
  });
}

function objectRow(page: Page, oid: string) {
  return page.locator(`#objPickerList .obj-row[data-oid="${oid}"]`);
}

// auditRows reports, for every row the list has rendered, what the server said
// about it and what the DOM offers for it. Every assertion about controls is
// made against this rather than against a second guess at the verdicts.
type RowAudit = {
  oid: string; name: string; reason: string; polled: boolean; child: boolean;
  hasBox: boolean; checked: boolean | null; disabled: boolean | null; marker: string;
};

async function auditRows(page: Page): Promise<RowAudit[]> {
  return page.evaluate(() => {
    const state = (window as any).app.objectPicker;
    return [...document.querySelectorAll('#objPickerList .obj-row')].map(row => {
      const oid = (row as HTMLElement).dataset.oid!;
      const object = state.index.get(oid);
      const box = row.querySelector('input[type=checkbox]') as HTMLInputElement | null;
      return {
        oid,
        name: object.name,
        reason: object.reason,
        polled: !!object.polled,
        child: row.classList.contains('is-child'),
        hasBox: !!box,
        checked: box ? box.checked : null,
        disabled: box ? box.disabled : null,
        marker: (row.querySelector('.obj-state') as HTMLElement | null)?.className || '',
      };
    });
  });
}

// showAllObjects reveals the reference-only rows the default view holds back.
async function showAllObjects(page: Page) {
  await page.locator('#objPickerShowAllBtn').click();
  await expect(page.locator('#objPickerShowActionableBtn')).toBeVisible();
}

test.describe('per-object polling picker', () => {
  test.beforeAll(async ({ request }) => {
    deviceId = await resolveId(request, '/api/devices', 'devices', DEVICE_NAME);
    profileDeviceId = await resolveId(request, '/api/devices', 'devices', PROFILE_DEVICE_NAME);
    mibId = await resolveId(request, '/api/mibs', 'mibs', MIB_NAME);

    preexistingAssignment = await directAssignment(request);
    if (preexistingAssignment === null) {
      const created = await request.post(`${BASE}/api/devices/${deviceId}/mibs`, {
        data: { mib_id: mibId }
      });
      expect(created.ok(), `assigning ${MIB_NAME} to ${DEVICE_NAME} returned ${created.status()}`).toBeTruthy();
    }
    await resetAssignment(request);
  });

  test.beforeEach(async ({ request }) => {
    // Each test starts from the same known state, so an earlier failure cannot
    // silently change what a later one is asserting against.
    await resetAssignment(request);
  });

  test.afterAll(async ({ request }) => {
    // Runs whatever happened above: the fixture device is left exactly as it
    // was found, and the live routers are confirmed untouched. Nothing is
    // touched when setup never got as far as resolving the fixture, so a
    // stack that was down cannot turn into a DELETE against id 0.
    if (!deviceId || !mibId) return;

    // Restore first and assert afterwards. A failed expectation aborts the
    // hook, so anything that must happen has to happen before the first one —
    // otherwise one bad assertion leaves the fixture device reconfigured.
    let removeStatus = 0;
    if (preexistingAssignment === null) {
      removeStatus = (await request.delete(`${BASE}/api/devices/${deviceId}/mibs/${mibId}`)).status();
    } else {
      await request.put(`${BASE}/api/devices/${deviceId}/mibs/${mibId}/assignment`, {
        data: preexistingAssignment
      });
    }
    const restored = await directAssignment(request);

    if (preexistingAssignment === null) {
      expect(removeStatus < 400,
        `removing ${MIB_NAME} from ${DEVICE_NAME} returned ${removeStatus}`).toBeTruthy();
      expect(restored, `${MIB_NAME} is still assigned to ${DEVICE_NAME} after teardown`).toBeNull();
    } else {
      expect(restored, `${MIB_NAME} is no longer assigned to ${DEVICE_NAME}`).not.toBeNull();
      expect(restored!.enabled).toBe(preexistingAssignment.enabled ?? true);
      expect(restored!.exclude_names || []).toEqual(preexistingAssignment.exclude_names || []);
      expect(restored!.include_names || []).toEqual(preexistingAssignment.include_names || []);
      expect(restored!.max_oids || 0).toBe(preexistingAssignment.max_oids || 0);
    }

    const response = await request.get(`${BASE}/api/devices/${profileDeviceId}/mibs`);
    expect(response.ok()).toBeTruthy();
    const body = await response.json();
    expect(body.direct_mibs || [],
      `${PROFILE_DEVICE_NAME} gained a direct MIB assignment; the profile must not be shadowed by this spec`,
    ).toEqual([]);
  });

  test('every tick is the server verdict, and the selection is priced live', async ({ page }) => {
    const errors = consoleErrors(page);
    await openDevice(page, DEVICE_NAME);
    await openPicker(page, '#deviceDirectMibs', MIB_NAME);

    const server = await plan(page);
    expect(server.source).toBe('device');

    // Rendered boxes must agree with plan.polled object by object, never with
    // anything derived from the patterns in the browser.
    const audit = await auditRows(page);
    const disagreements = audit.filter(r => r.hasBox && r.checked !== r.polled);
    expect(disagreements, `boxes disagreeing with plan.polled: ${JSON.stringify(disagreements)}`).toEqual([]);

    // Every object the server says is polled is operable, so "stop collecting
    // this" is always available for the things actually being collected.
    const unreachable = audit.filter(r => r.polled && !r.hasBox);
    expect(unreachable, `polled objects with no checkbox: ${JSON.stringify(unreachable)}`).toEqual([]);

    // The running total names the count and the cost of the current choice.
    // With no filters stored, every configurable object is selected.
    const polledSeries = server.objects.filter(o => o.polled).reduce((sum, o) => sum + o.series, 0);
    await expect(page.locator('#objPickerTotal'))
      .toHaveText(new RegExp(`Selected ${server.polled} of ${server.polled} configurable objects → ~?${polledSeries}\\s*series`));

    // Unticking one polled object moves both halves of that sentence at once.
    const target = server.objects.find(o => o.polled && o.kind === 'scalar')!;
    expect(target, 'UDP-MIB should have a polled scalar').toBeTruthy();
    await objectRow(page, target.oid).locator('input[type=checkbox]').uncheck();
    await expect(page.locator('#objPickerTotal'))
      .toHaveText(new RegExp(`Selected ${server.polled - 1} of ${server.polled} configurable objects → ~?${polledSeries - target.series}\\s*series`));

    // ... and it is visible as a pattern in Advanced, not just as a tick.
    await expect(page.locator('#objPickerExcludeNames')).toHaveValue(`^${target.name}$`);
    await expect(page.locator('#objPickerBanners .pending')).toContainText('Unsaved');

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('the list opens on what can be operated, and no box in it is dead', async ({ page }) => {
    await openDevice(page, DEVICE_NAME);
    await openPicker(page, '#deviceDirectMibs', MIB_NAME);

    const server = await plan(page);

    // Nothing anywhere in this picker is a checkbox that refuses to be
    // clicked. That was the reported bug: a not-allowed cursor on 92% of rows.
    const dead = (await auditRows(page)).filter(r => r.disabled === true);
    expect(dead, `disabled checkboxes: ${JSON.stringify(dead)}`).toEqual([]);

    // With no filters stored, every pollable object is polled and the only
    // available action is to remove one. The picker has to say so, because a
    // user hunting for a control that cannot exist is what reads as broken.
    await expect(page.locator('#objPickerBanners .info.howto'))
      .toContainText(`Everything ${MIB_NAME} can poll on ${DEVICE_NAME} is being polled`);
    await expect(page.locator('#objPickerBanners .info.howto'))
      .toContainText('untick one to stop collecting it, and it becomes tickable again the moment it is excluded');

    // The default view is exactly the operable rows: a top-level row per
    // scalar and per table, with the tables' columns folded underneath them.
    const topLevel = page.locator('#objPickerList .obj-group > .obj-row');
    await expect(topLevel).toHaveCount(server.polled);
    for (const row of await topLevel.all()) {
      await expect(row.locator('input[type=checkbox]')).toBeChecked();
    }

    // What it is holding back is counted and named, not silently dropped.
    const never = server.objects.filter(o => o.reason === 'not-accessible' || o.reason === 'not-current');
    await expect(page.locator('#objPickerFooter .obj-picker-hidden'))
      .toContainText(`${never.length} reference-only object`);
    await expect(page.locator('#objPickerFooter .obj-picker-hidden'))
      .toContainText('nothing you can turn on or off');

    // ... and revealing it is one click, and reversible.
    await showAllObjects(page);
    await expect(page.locator('#objPickerShow')).toHaveValue('all');
    for (const object of never) {
      await expect(objectRow(page, object.oid)).toHaveCount(1);
      await expect(objectRow(page, object.oid).locator('input[type=checkbox]')).toHaveCount(0);
    }
    const revealed = (await auditRows(page)).filter(r => r.disabled === true);
    expect(revealed, `disabled checkboxes once everything is shown: ${JSON.stringify(revealed)}`).toEqual([]);

    await page.locator('#objPickerShowActionableBtn').click();
    await expect(page.locator('#objPickerShow')).toHaveValue('actionable');
    await expect(topLevel).toHaveCount(server.polled);
  });

  test('an object can be excluded and put back, and the box follows the server both ways', async ({ page, request }) => {
    const puts = recordAssignmentPuts(page);
    await openDevice(page, DEVICE_NAME);
    await openPicker(page, '#deviceDirectMibs', MIB_NAME);

    const server = await plan(page);
    const target = server.objects.find(o => o.polled && o.kind === 'scalar')!;
    const box = objectRow(page, target.oid).locator('input[type=checkbox]');

    // Start: polled, ticked, and operable.
    await expect(box).toBeChecked();
    await expect(box).toBeEnabled();

    await box.uncheck();
    await page.locator('#objPickerSaveBtn').click();

    // The picker re-reads the plan rather than predicting the new verdicts.
    await expect.poll(async () => (await plan(page)).objects.find(o => o.oid === target.oid)!.reason,
      { timeout: 20000 }).toBe('excluded-by-filter');

    expect(puts, `expected exactly one assignment PUT, got ${puts.length}`).toHaveLength(1);
    const stored = await getAssignment(request);
    expect(stored.exclude_names).toEqual([`^${target.name}$`]);

    // Excluded: unticked and still operable, which is the half of the round
    // trip the picker used to make impossible — nothing could be turned on.
    await expect(box).not.toBeChecked();
    await expect(box).toBeEnabled();
    await expect(objectRow(page, target.oid).locator('.obj-verdict'))
      .toContainText(`Removed by ^${target.name}$`);
    await expect(page.locator('#objPickerBanners .pending')).toHaveCount(0);

    // The picker now describes the other action, because there is one.
    await expect(page.locator('#objPickerBanners .info.howto'))
      .toContainText(`${server.polled - 1} of ${server.polled} configurable objects are polled`);
    await expect(page.locator('#objPickerBanners .info.howto'))
      .toContainText('Tick the excluded object to start collecting it again');

    // Re-ticking removes the pattern it named, and says so on the server too.
    await box.check();
    await expect(page.locator('#objPickerExcludeNames')).toHaveValue('');
    await page.locator('#objPickerSaveBtn').click();

    await expect.poll(async () => (await plan(page)).objects.find(o => o.oid === target.oid)!.polled,
      { timeout: 20000 }).toBe(true);
    const cleared = await getAssignment(request);
    expect(cleared.exclude_names || []).toEqual([]);

    await expect(box).toBeChecked();
    await expect(box).toBeEnabled();
    expect(puts, `expected exactly two assignment PUTs, got ${puts.length}`).toHaveLength(2);
  });

  test('a column collected by a table walk reads as on, under the table that governs it', async ({ page }) => {
    await openDevice(page, DEVICE_NAME);
    await openPicker(page, '#deviceDirectMibs', MIB_NAME);

    const server = await plan(page);
    const column = server.objects.find(o => o.reason === 'covered-by-table-walk' && o.kind === 'column')!;
    expect(column, 'UDP-MIB should have a column covered by a table walk').toBeTruthy();
    const table = server.objects.find(o => o.kind === 'table' && column.oid.startsWith(`${o.oid}.`))!;

    // The column is not a sibling of the table in the list; it is its detail.
    const group = page.locator(`#objPickerList .obj-group[data-group-oid="${table.oid}"]`);
    await expect(group).toHaveCount(1);
    await expect(group.locator(`.obj-children .obj-row[data-oid="${column.oid}"]`)).toHaveCount(1);

    const disclosure = group.locator('.obj-cols-toggle');
    await expect(disclosure).toHaveAttribute('aria-expanded', 'false');
    await expect(group.locator('.obj-group-note')).toContainText('collected by this walk');
    await disclosure.click();
    await expect(disclosure).toHaveAttribute('aria-expanded', 'true');

    const row = objectRow(page, column.oid);
    await row.scrollIntoViewIfNeeded();

    // This object IS being collected, so it reads on. An unticked box beside a
    // "Collected" badge said the opposite of the badge next to it.
    await expect(row.locator('input[type=checkbox]')).toHaveCount(0);
    await expect(row.locator('.obj-state.is-on')).toHaveCount(1);
    await expect(row.locator('.obj-state')).toHaveAttribute('aria-label', `Collected by the ${table.name} walk`);
    await expect(row.locator('.badge')).toHaveText('Collected');
    await expect(row.locator('.obj-verdict')).toContainText(`by the ${table.name} walk above`);

    // The control is the table's, and it is offered as such.
    await expect(group.locator('.obj-group-note'))
      .toContainText('excluding a column on its own would not stop it');
    await group.getByRole('button', { name: `Stop collecting ${table.name}` }).click();
    await expect(objectRow(page, table.oid).locator('input[type=checkbox]')).not.toBeChecked();
    await expect(page.locator('#objPickerExcludeNames')).toHaveValue(`^${table.name}$`);

    // And the columns stop claiming to be collected, without claiming to have
    // stopped already — the verdict for that only arrives with the next fetch.
    await expect(objectRow(page, column.oid).locator('.obj-state.is-stopping')).toHaveCount(1);
    await expect(objectRow(page, column.oid).locator('.obj-verdict'))
      .toContainText(`unticking ${table.name} stops this too, once saved`);
  });

  test('an object the agent can never return is shown as a state, not as a refused control', async ({ page }) => {
    await openDevice(page, DEVICE_NAME);
    await openPicker(page, '#deviceDirectMibs', MIB_NAME);

    const server = await plan(page);
    const unreadable = server.objects.find(o => o.reason === 'not-accessible')!;
    expect(unreadable, 'UDP-MIB should have a not-accessible object').toBeTruthy();

    // It is reference material, so the default view leaves it out.
    await expect(objectRow(page, unreadable.oid)).toHaveCount(0);
    await showAllObjects(page);

    const row = objectRow(page, unreadable.oid);
    await row.scrollIntoViewIfNeeded();
    await expect(row.locator('input[type=checkbox]')).toHaveCount(0);
    await expect(row.locator('.obj-state.is-never')).toHaveCount(1);
    await expect(row.locator('.obj-state'))
      .toHaveAttribute('aria-label', `${unreadable.name} cannot be polled`);
    await expect(row.locator('.obj-verdict')).toContainText('Not pollable');
    await expect(row.locator('.obj-verdict')).toContainText('No filter changes that');
  });

  test('an invalid pattern blocks the save with an inline error and sends nothing', async ({ page, request }) => {
    const puts = recordAssignmentPuts(page);
    await openDevice(page, DEVICE_NAME);
    await openPicker(page, '#deviceDirectMibs', MIB_NAME);

    await page.locator('#objPickerAdvanced > summary').click();
    // Unbalanced parenthesis. It must not be comma-split, either: `udp{1,2}`
    // below is a single valid pattern that happens to contain a comma.
    await page.locator('#objPickerExcludeNames').fill('(unclosed\nudp{1,2}');
    await page.locator('#objPickerSaveBtn').click();

    const error = page.locator('#objPickerExcludeNamesError');
    await expect(error).toBeVisible({ timeout: 10000 });
    await expect(error).toContainText('Line 1');
    await expect(error).toContainText('(unclosed');
    await expect(page.locator('#modalOverlay')).toBeVisible();
    expect(puts, `unexpected assignment PUT: ${puts.join(', ')}`).toHaveLength(0);

    // The comma-containing pattern alone is valid and saves.
    await page.locator('#objPickerExcludeNames').fill('udp{1,2}');
    await page.locator('#objPickerSaveBtn').click();
    await expect(error).toBeHidden({ timeout: 10000 });
    await expect.poll(async () => (await getAssignment(request)).exclude_names as string[],
      { timeout: 20000 }).toEqual(['udp{1,2}']);

    // Hand-edited patterns can catch objects the user never ticked, and the UI
    // has to admit that rather than show a tick it cannot vouch for.
    await page.locator('#objPickerIncludeNames').fill('udpIn.*');
    await expect(page.locator('#objPickerBanners .warn')).toContainText('the ticks below are still the last verdicts');
  });

  test('Cancel closes the picker without writing', async ({ page, request }) => {
    const puts = recordAssignmentPuts(page);
    await openDevice(page, DEVICE_NAME);
    await openPicker(page, '#deviceDirectMibs', MIB_NAME);

    const server = await plan(page);
    const target = server.objects.find(o => o.polled && o.kind === 'scalar')!;
    await objectRow(page, target.oid).locator('input[type=checkbox]').uncheck();
    await page.locator('#objPickerCancelBtn').click();

    await expect(page.locator('#modalOverlay')).toBeHidden({ timeout: 10000 });
    expect(puts, `Cancel issued a PUT: ${puts.join(', ')}`).toHaveLength(0);
    const stored = await getAssignment(request);
    expect(stored.exclude_names || []).toEqual([]);
  });

  test('a profile-sourced module warns before shadowing and writes nothing when declined', async ({ page }) => {
    const puts = recordAssignmentPuts(page);

    // Belt and braces: a regression that saved without confirmation must fail
    // this test rather than reconfigure a live router.
    let blocked = 0;
    await page.route(`**/api/devices/${profileDeviceId}/mibs/*/assignment`, route => {
      if (route.request().method() === 'PUT') { blocked++; return route.abort(); }
      return route.continue();
    });

    await openDevice(page, PROFILE_DEVICE_NAME);
    await openPicker(page, '#deviceAllMibs', MIB_NAME);

    const server = await plan(page);
    expect(server.source).toBe('profile');
    await expect(page.locator('#objPickerBanners .info.source')).toContainText('shadows it for this device only');

    const target = server.objects.find(o => o.polled && o.kind === 'scalar')!;
    await objectRow(page, target.oid).locator('input[type=checkbox]').uncheck();
    await page.locator('#objPickerSaveBtn').click();

    // The confirmation stands between the click and the write.
    const confirm = page.locator('#objPickerShadowConfirmBtn');
    await expect(confirm).toBeVisible({ timeout: 10000 });
    await expect(page.locator('#objPickerConfirm')).toContainText('Saving creates a device-level assignment');
    expect(puts, `a PUT was sent before the shadow was confirmed: ${puts.join(', ')}`).toHaveLength(0);

    await page.locator('#objPickerShadowCancelBtn').click();
    await expect(confirm).toHaveCount(0);
    await page.locator('#objPickerCancelBtn').click();
    await expect(page.locator('#modalOverlay')).toBeHidden({ timeout: 10000 });

    expect(puts, `declining the warning still wrote: ${puts.join(', ')}`).toHaveLength(0);
    expect(blocked, 'an assignment PUT reached the profile-backed device').toBe(0);
  });
});
