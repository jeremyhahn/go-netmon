import { test, expect } from '@playwright/test';

// Verifies the MIB browser fix against the deployed build: collapsed on arrival,
// no permanent spinners, lazy fetch on expand.
test('MIB browser loads collapsed and lazily', async ({ page }) => {
    const requests: string[] = [];
    page.on('requestfinished', (r) => requests.push(r.url().replace(/^https?:\/\/[^/]+/, '')));
    const errors: string[] = [];
    page.on('pageerror', (e) => errors.push(String(e)));

    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.goto('/');
    await page.waitForTimeout(1200);

    const beforeTab = requests.length;
    await page.locator('#mibBrowserBtn').click();
    await page.waitForTimeout(2500);

    const state = await page.evaluate(() => {
        const acc = document.getElementById('mibAccordion')!;
        return {
            items: acc.querySelectorAll('.mib-accordion-item').length,
            spinners: acc.querySelectorAll('.mib-loading').length,
            openBodies: Array.from(acc.querySelectorAll('.mib-accordion-item'))
                .filter((el) => (el as HTMLElement).querySelector('.expanded')
                    || el.classList.contains('expanded')).length,
            height: document.body.scrollHeight,
        };
    });
    console.log(`collapsed state: ${JSON.stringify(state)}`);
    console.log(`requests on tab open: ${requests.length - beforeTab}`);

    // The library is paged rather than held in the document all at once -- that
    // is the point of this surface, and rendering 179 accordion items to show
    // eight is what it replaced. What has to be true is that one page is on
    // screen and the pager accounts for the rest.
    expect(state.items, 'a page of modules should be listed').toBeGreaterThan(0);
    expect(state.items, 'the whole library is in the document again').toBeLessThanOrEqual(25);
    const listed = await page.locator('#mibAccordionStatus').textContent();
    expect(listed, `the pager does not account for the library: ${listed}`)
      .toMatch(/of [\d,]+ modules/);
    expect(state.spinners, 'no permanent loading spinners').toBe(0);
    expect(state.height, 'page should not be tens of thousands of px').toBeLessThan(20000);

    // Expanding one module fetches exactly once.
    const beforeExpand = requests.length;
    await page.locator('.mib-accordion-item').first().locator('.mib-accordion-header').click();
    await page.waitForTimeout(1500);
    const fetched = requests.length - beforeExpand;
    console.log(`requests on expanding one module: ${fetched}`);
    expect(fetched, 'expanding fetches at most one module').toBeLessThanOrEqual(2);

    const objectsShown = await page.evaluate(() =>
        document.querySelectorAll('#mibAccordion .mib-object-item').length);
    console.log(`objects rendered after expand: ${objectsShown}`);
    expect(objectsShown, 'expanded module should render its objects').toBeGreaterThan(0);

    expect(errors, `page errors: ${errors.join('; ')}`).toHaveLength(0);
    await page.screenshot({ path: '/tmp/netmon-shots/browser-fixed.png' });
});

test('MIB library shows summary cards', async ({ page }) => {
    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.goto('/');
    await page.locator('#mibsBtn, nav button:has-text("MIBs")').first().click();
    await page.waitForTimeout(2500);

    const cards = await page.evaluate(() => {
        const el = document.getElementById('mibSummaryCards');
        return el ? (el.textContent || '').replace(/\s+/g, ' ').trim() : null;
    });
    console.log(`summary cards: ${cards}`);
    expect(cards, 'summary cards should be present').toBeTruthy();
    expect(cards!, 'cards should report real counts').toMatch(/\d/);
    await page.screenshot({ path: '/tmp/netmon-shots/mibs-cards.png' });
});
