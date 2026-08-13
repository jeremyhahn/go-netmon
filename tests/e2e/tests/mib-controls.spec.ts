import { test, expect, Page, APIRequestContext } from '@playwright/test';

// Per-MIB polling controls (enable/disable + include/exclude filters) exist
// in the CLI but were missing from the GUI. This drives the real device page
// against the real server and confirms the toggle and filter modal actually
// change what the backend stores — not just what the DOM shows.
//
// The device under test is "pdu-03": it carries no profile, so nothing
// here can touch the "Core Routers" profile that core-01/02 inherit while
// they are live-monitored. (bastion, the other non-router device, was tried
// first but its assignment endpoint blocks for ~30s per request — apparently
// a synchronous reachability check against that unreachable device — which
// made it an unreliable, slow test fixture; pdu-03 answers in
// milliseconds.)
//
// Nothing below hardcodes a row id. The database is rebuilt from scratch
// regularly and every id is renumbered when it is, so the device and the MIB
// are looked up by name through the API before the first test runs, and the
// direct assignment this spec needs is created if it is not already there and
// torn back down to whatever was found at the start.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';
const DEVICE_NAME = 'pdu-03';

// UDP-MIB is small (31 objects, ~11 estimated series) so the enable
// confirmation never trips the >5000-series high-impact gate, and it is part
// of the standard import that is always present.
const MIB_NAME = 'UDP-MIB';

// Resolved in beforeAll; never assumed.
let deviceId = 0;
let mibId = 0;

// preexistingAssignment records the direct assignment found before the spec
// touched anything: null means the MIB was not directly assigned and this
// spec created it, so teardown has to remove it again.
let preexistingAssignment: Record<string, unknown> | null = null;

function consoleErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('console', msg => {
    if (msg.type() === 'error') errors.push(msg.text());
  });
  page.on('pageerror', err => errors.push(`pageerror: ${err.message}`));
  return errors;
}

// resolveId looks up one record by name in a list endpoint. Failing here
// names the missing fixture rather than letting a stale numeric id turn into
// an unexplained 404 later.
async function resolveId(
  request: APIRequestContext, path: string, key: string, name: string,
): Promise<number> {
  const response = await request.get(`${BASE}${path}`);
  expect(response.ok(), `GET ${path} status ${response.status()}`).toBeTruthy();

  const body = await response.json();
  const records: Array<{ id: number; name: string }> = body[key] || [];
  const match = records.find(record => record.name === name);

  expect(match,
    `fixture "${name}" was not found in ${path}; available: ${records.map(r => r.name).join(', ')}`,
  ).toBeTruthy();

  return match!.id;
}

// directAssignment returns the stored assignment for the fixture MIB, or null
// when the MIB is not directly assigned to the device at all.
async function directAssignment(request: APIRequestContext): Promise<Record<string, unknown> | null> {
  const response = await request.get(`${BASE}/api/devices/${deviceId}/mibs/${mibId}/assignment`);
  if (response.status() === 404) {
    return null;
  }
  expect(response.ok(), `assignment fetch status ${response.status()}`).toBeTruthy();
  return response.json();
}

// getAssignment is directAssignment with the assignment required to exist,
// which is the state every assertion in this spec runs against.
async function getAssignment(request: APIRequestContext): Promise<Record<string, unknown>> {
  const assignment = await directAssignment(request);
  expect(assignment, `${MIB_NAME} is no longer directly assigned to ${DEVICE_NAME}`).not.toBeNull();
  return assignment!;
}

// setAssignment forces the assignment to enabled with no filters — the state
// the fixture must be in before each test and the state it must be left in.
async function setAssignment(request: APIRequestContext) {
  const response = await request.put(`${BASE}/api/devices/${deviceId}/mibs/${mibId}/assignment`, {
    data: { enabled: true }
  });
  expect(response.ok(), `restore status ${response.status()}`).toBeTruthy();
}

async function openDevice(page: Page) {
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await page.locator('#devicesBtn').click();

  // Matched on the name cell exactly, not by substring: the fleet also holds
  // pdu-03-2, and "contains pdu-03" is true of both of them.
  const row = page.locator('#devicesList tr').filter({
    has: page.locator('td strong', { hasText: new RegExp(`^${DEVICE_NAME}$`) }),
  });
  await expect(row).toBeVisible({ timeout: 20000 });
  await row.getByRole('button', { name: 'View' }).click();

  await expect(page.locator('#deviceDetailsTitle')).toHaveText(DEVICE_NAME, { timeout: 20000 });
  await expect(page.locator('#deviceDirectMibs .mib-row', { hasText: MIB_NAME })).toBeVisible({ timeout: 20000 });
}

function mibRow(page: Page) {
  return page.locator('#deviceDirectMibs .mib-row', { hasText: MIB_NAME });
}

test.describe('MIB polling controls', () => {
  test.beforeAll(async ({ request }) => {
    deviceId = await resolveId(request, '/api/devices', 'devices', DEVICE_NAME);
    mibId = await resolveId(request, '/api/mibs', 'mibs', MIB_NAME);

    preexistingAssignment = await directAssignment(request);
    if (preexistingAssignment === null) {
      const created = await request.post(`${BASE}/api/devices/${deviceId}/mibs`, {
        data: { mib_id: mibId }
      });
      expect(created.ok(), `assigning ${MIB_NAME} to ${DEVICE_NAME} returned ${created.status()}`).toBeTruthy();
    }

    // Start from the known-good baseline so the test is not sensitive to
    // leftovers from a previous run.
    await setAssignment(request);
  });

  test.afterAll(async ({ request }) => {
    // The device must be left exactly as found, regardless of which assertion
    // above failed, if any.
    if (preexistingAssignment === null) {
      const removed = await request.delete(`${BASE}/api/devices/${deviceId}/mibs/${mibId}`);
      expect(removed.ok(), `removing ${MIB_NAME} from ${DEVICE_NAME} returned ${removed.status()}`).toBeTruthy();
      expect(await directAssignment(request),
        `${MIB_NAME} is still assigned to ${DEVICE_NAME} after teardown`).toBeNull();
      return;
    }

    await setAssignment(request);
    const assignment = await getAssignment(request);
    expect(assignment.enabled).toBe(true);
    expect(assignment.include_names || []).toEqual([]);
    expect(assignment.exclude_names || []).toEqual([]);
    expect(assignment.include_oids || []).toEqual([]);
    expect(assignment.exclude_oids || []).toEqual([]);
  });

  test('toggling the switch disables and re-enables the direct MIB', async ({ page, request }) => {
    const errors = consoleErrors(page);
    await openDevice(page);

    const toggle = mibRow(page).getByRole('checkbox');
    await expect(toggle).toBeChecked();

    // Disable: applies immediately, no confirmation required.
    await toggle.uncheck();
    await expect(mibRow(page).getByRole('checkbox')).not.toBeChecked({ timeout: 10000 });

    let assignment = await getAssignment(request);
    expect(assignment.enabled).toBe(false);

    // Re-enable: this goes through the estimate confirmation step, which
    // must use #modalOverlay rather than a native dialog.
    await mibRow(page).getByRole('checkbox').check();

    const overlay = page.locator('#modalOverlay');
    await expect(overlay).toBeVisible({ timeout: 10000 });
    await expect(page.locator('#modalOverlayTitle')).toContainText(MIB_NAME);

    const confirmBtn = page.locator('#mibEnableConfirmBtn');
    await expect(confirmBtn).toBeVisible({ timeout: 10000 });
    await expect(confirmBtn).toBeEnabled();
    await confirmBtn.click();
    await expect(overlay).toBeHidden({ timeout: 10000 });

    await expect(mibRow(page).getByRole('checkbox')).toBeChecked({ timeout: 10000 });

    assignment = await getAssignment(request);
    expect(assignment.enabled).toBe(true);

    expect(errors, `console errors: ${errors.join(' | ')}`).toHaveLength(0);
  });

  test('the filter modal saves a filter and rejects an invalid pattern without a request', async ({ page, request }) => {
    await openDevice(page);

    await mibRow(page).getByRole('button', { name: 'Filters' }).click();

    const overlay = page.locator('#modalOverlay');
    await expect(overlay).toBeVisible({ timeout: 10000 });
    await expect(page.locator('#modalOverlayTitle')).toContainText(`Filters: ${MIB_NAME}`);

    // --- Bad regex is refused client-side, no PUT is sent ---
    const puts: string[] = [];
    page.on('request', req => {
      if (req.method() === 'PUT' && req.url().includes('/assignment')) {
        puts.push(req.url());
      }
    });

    // Unbalanced parenthesis: not a valid regex.
    await page.locator('#mibFilterExcludeNames').fill('(unclosed');
    await page.locator('#mibFilterSaveBtn').click();

    const excludeError = page.locator('#mibFilterExcludeNamesError');
    await expect(excludeError).toBeVisible({ timeout: 5000 });
    await expect(excludeError).toContainText('(unclosed');
    await expect(overlay).toBeVisible(); // modal stays open, nothing was saved
    expect(puts, `unexpected PUT requests: ${puts.join(', ')}`).toHaveLength(0);

    // Fix the bad field, add one valid include-name filter, and save.
    await page.locator('#mibFilterExcludeNames').fill('');
    await page.locator('#mibFilterIncludeNames').fill('udp.*');
    await page.locator('#mibFilterSaveBtn').click();

    await expect(overlay).toBeHidden({ timeout: 10000 });
    expect(puts.length, `expected exactly one PUT, got: ${puts.join(', ')}`).toBe(1);

    // --- Persisted: re-fetch from the API directly ---
    const assignment = await getAssignment(request);
    expect(assignment.include_names).toEqual(['udp.*']);

    // --- Reflected in the row's filter summary ---
    await expect(mibRow(page).locator('.mib-filter-summary')).toContainText('1 include');

    // --- Clean up via the UI's own "Clear all filters" + Save, proving that
    // control works too, before the afterAll hook double-checks via the API.
    await mibRow(page).getByRole('button', { name: 'Filters' }).click();
    await expect(page.locator('#mibFilterIncludeNames')).toHaveValue('udp.*');
    await page.locator('#mibFilterClearBtn').click();
    await expect(page.locator('#mibFilterIncludeNames')).toHaveValue('');
    await page.locator('#mibFilterSaveBtn').click();
    await expect(overlay).toBeHidden({ timeout: 10000 });

    const cleared = await getAssignment(request);
    expect(cleared.include_names || []).toEqual([]);
  });
});
