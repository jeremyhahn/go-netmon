import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// Every word on the dashboard, measured against what is painted behind it.
//
// WCAG AA asks 4.5:1 of body text. The palette was chosen for charts, where the
// bar is 3:1, and then used for words -- so amber measured 1.83:1 on white and
// was being used as text. A colour that reads as a stroke does not read as a
// word, and one token cannot be the nearest answer to both bars.
//
// Two things about HOW this measures, both of which produce a plausible wrong
// answer if you get them wrong:
//
//   - Read the colour back from a CANVAS, not from a regex over
//     getComputedStyle. Modern CSS resolves colours as oklch(), and a
//     three-number parser turns oklch(0.76 0.177 163.223) into r=0.76 g=0.177
//     b=163.223 and reports a ratio that looks like a real failure.
//   - Composite the WHOLE ancestor stack. Taking the first non-transparent
//     background and painting it over white is wrong wherever a layer is
//     semi-transparent: the selected module in the rail is a 14%-alpha accent
//     over a dark card, and measuring it over white reported 2.89:1 for
//     something that is actually 4.57:1.

const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';
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

// The device these run against. A dashboard is only worth measuring if it has
// panels on it, and the first entry in the selector is whichever device sorts
// first -- on this fleet a KVM with one. Named rather than indexed, so a thin
// device cannot make either of these pass by measuring nothing.
const DEVICE = process.env.NETMON_DEVICE || 'dist-01';

async function selectDevice(page: Page) {
  const labels = await page.locator('#dashboardDeviceSelect option').allTextContents();
  const wanted = labels.find(l => l.trim() === DEVICE);
  expect(wanted,
    `no device named "${DEVICE}" in the selector; it offers ${labels.join(', ')}. ` +
    `Set NETMON_DEVICE to one that has panels.`).toBeTruthy();
  await page.selectOption('#dashboardDeviceSelect', { label: wanted as string });
}

async function serveWorkingTree(page: Page) {
  const assets: [string, string, string][] = [
    ['**/js/app.js*', 'js/app.js', 'text/javascript'],
    ['**/js/chart-theme.js*', 'js/chart-theme.js', 'text/javascript'],
    ['**/css/style.css*', 'css/style.css', 'text/css'],
  ];
  for (const [glob, file, type] of assets) {
    await page.route(glob, route => route.fulfill({
      status: 200, contentType: type, body: fs.readFileSync(path.join(STATIC, file), 'utf8'),
    }));
  }
  await page.route(url => url.pathname === '/', async route => {
    const response = await route.fetch();
    await route.fulfill({ response, body: fs.readFileSync(path.join(STATIC, 'index.html'), 'utf8') });
  });
}

// Text that must clear 4.5:1. Each is a different one of the four axes the
// panel head separates information on, so a regression in any of them shows up.
const TEXT = [
  '.panel-eyebrow',
  '.panel-title',
  '.panel-note',
  '.panel-unit',
  '.panel-row-name',
  '.panel-row-value',
  '.panel-row:nth-child(even) .panel-row-name',
  '.panel-section-title',
  '.panel-section-note',
  '.panel-rows-more',
  '.tab-button.active',
  '.chart-container.is-empty:not(.is-nodata) .chart-empty-title',
  '.chart-container.is-nodata .chart-empty-title',
  '.chart-empty-detail',
];

// The same words on the library surfaces. These carry the accent as TEXT --
// the one place a graphic-tier colour keeps being reached for -- and none of
// them is on a dashboard, so the walk above never measured them.
//
// .pager-page.is-current is the accent worn on its own tint, and .danger-btn is
// red on the plain surface -- the two tightest of the eight places a
// graphic-tier colour was being used as a word. The sort headers use the same
// accent token but render only in a MIB's object table, which this surface does
// not reach, so they are not claimed as measured here.
const LIBRARY_TEXT = [
  '.pager-page.is-current',
  '.pager-status',
  '.danger-btn',
];

async function measure(page: Page, selectors: string[]) {
  return page.evaluate(sels => {
    const ctx = document.createElement('canvas')
      .getContext('2d', { willReadFrequently: true })!;
    const paint = (layers: string[]) => {
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillStyle = '#ffffff';
      ctx.fillRect(0, 0, 1, 1);
      for (const layer of layers) { ctx.fillStyle = layer; ctx.fillRect(0, 0, 1, 1); }
      const d = ctx.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2]] as [number, number, number];
    };
    const lin = (c: number) => { c /= 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
    const lum = ([r, g, b]: number[]) => 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
    const ratio = (f: number[], b: number[]) => {
      const a = lum(f), c = lum(b);
      return (Math.max(a, c) + 0.05) / (Math.min(a, c) + 0.05);
    };

    // The page keeps every view in the DOM and shows one, so querySelector
    // hands back whichever match comes first in document order -- which is
    // routinely inside a hidden view, painted in whatever the palette was when
    // it was last built. Only the matches that are actually RENDERED are ones a
    // person can read, and only those are worth a ratio.
    const rendered = (el: HTMLElement) =>
      el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden';

    const measureOne = (el: HTMLElement) => {
      const stack: string[] = [];
      for (let n: HTMLElement | null = el; n; n = n.parentElement) {
        const bg = getComputedStyle(n).backgroundColor;
        if (bg && bg !== 'transparent' && !/,\s*0\)$/.test(bg)) stack.unshift(bg);
      }
      return ratio(paint([getComputedStyle(el).color]), paint(stack));
    };

    return sels.map(sel => {
      // EVERY rendered match, reported at its worst. One selector can name
      // elements sitting on different backgrounds -- the danger buttons are on
      // several surfaces -- and taking the first in document order measures
      // whichever the markup happened to emit first.
      const all = ([...document.querySelectorAll(sel)] as HTMLElement[]).filter(rendered);
      if (all.length === 0) return { sel, ratio: null as number | null, count: 0 };

      const ratios = all.map(measureOne);
      return {
        sel,
        ratio: +Math.min(...ratios).toFixed(2),
        count: all.length,
      };
    });
  }, selectors);
}

for (const theme of ['light', 'dark'] as const) {
  test(`every label on a dashboard clears AA in the ${theme} theme`, async ({ page }) => {
    test.setTimeout(180_000);

    await serveWorkingTree(page);
    await page.addInitScript(() => {
      try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
    });
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.evaluate(t => document.documentElement.setAttribute('data-theme', t), theme);
    await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

    await page.click('#dashboardBtn');
    await page.waitForTimeout(700);
  await selectDevice(page);
    // Long enough for the first sweep to fill the panels; an empty pane
    // measures nothing and would pass.
    await page.waitForTimeout(45000);
    await page.evaluate(t => document.documentElement.setAttribute('data-theme', t), theme);

    // A card that has drawn has no placeholder in it, so the two empty states
    // are only on screen when something is wrong -- which is exactly when they
    // are read, and why the accent-as-text failure in .chart-empty-title
    // (4.42:1) went unmeasured until a cold catalog put it on screen. Both
    // states are put back deliberately, through the app's own transitions.
    const empties = await page.evaluate(() => {
      const app = (window as any).app;
      const ids = Object.keys(app.charts || {});
      if (ids.length < 2) return 0;
      app.resetChartCard(ids[0]);
      app.markChartNoData(ids[1]);
      return 2;
    });
    expect(empties, 'no chart cards to put into their empty states').toBe(2);

    const measured = await measure(page, TEXT);

    const found = measured.filter(m => m.ratio !== null);
    expect(found.length,
      `nothing was measured: the dashboard rendered no panels, so this test ` +
      `would pass whatever the palette was`).toBeGreaterThan(6);

    const failing = found.filter(m => (m.ratio as number) < 4.5);
    expect(failing,
      `below 4.5:1 — ${failing.map(f => `${f.sel} ${f.ratio}:1`).join(', ')}`)
      .toHaveLength(0);
  });
}


for (const theme of ['light', 'dark'] as const) {
  test(`every label in the MIB library clears AA in the ${theme} theme`, async ({ page }) => {
    test.setTimeout(180_000);

    await serveWorkingTree(page);
    await page.addInitScript(() => {
      try { localStorage.removeItem('go-netmon-state'); } catch (e) { /* ignore */ }
    });
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.evaluate(t => document.documentElement.setAttribute('data-theme', t), theme);
    await expect(page.locator('#fleetOverview')).toBeVisible({ timeout: 30000 });

    await page.click('#mibManagerBtn');
    // ':visible' rather than .first(): several paged lists are built into the
    // DOM and only one of them is on screen.
    await expect(page.locator('.pager-page.is-current:visible').first())
      .toBeVisible({ timeout: 60000 });
    await expect(page.locator('.danger-btn:visible').first())
      .toBeVisible({ timeout: 60000 });
    await page.evaluate(t => document.documentElement.setAttribute('data-theme', t), theme);

    const measured = await measure(page, LIBRARY_TEXT);
    const found = measured.filter(m => m.ratio !== null);

    console.log(`\n=== ${theme} library ===`);
    for (const m of found) console.log(`  ${m.sel.padEnd(28)} ${m.ratio}:1  (worst of ${m.count})`);

    expect(found.length,
      `the library rendered only ${found.length} of ${LIBRARY_TEXT.length} selectors; ` +
      `an unmeasured one passes whatever the palette is`).toBe(LIBRARY_TEXT.length);

    const failing = found.filter(m => (m.ratio as number) < 4.5);
    expect(failing,
      `below 4.5:1 — ${failing.map(f => `${f.sel} ${f.ratio}:1`).join(', ')}`)
      .toHaveLength(0);
  });
}
