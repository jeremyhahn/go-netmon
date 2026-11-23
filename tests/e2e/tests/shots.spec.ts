import { test, expect } from '@playwright/test';
import { SEEDED_DEVICE } from '../stack';

// Captures the dashboard with a real device selected, so the populated state can
// be reviewed rather than the empty shell. Not an assertion suite.
const OUT = process.env.SHOT_DIR || '/tmp/netmon-shots';

for (const theme of ['dark', 'light']) {
    test(`capture populated ${theme}`, async ({ page }) => {
        await page.setViewportSize({ width: 1600, height: 1200 });
        await page.goto('/');
        await page.evaluate((t) => {
            document.documentElement.setAttribute('data-theme', t);
            localStorage.setItem('netmon-theme', t);
        }, theme);
        await page.waitForTimeout(1000);

        const select = page.locator('select').first();
        await select.selectOption({ label: SEEDED_DEVICE }).catch(async () => {
            await select.selectOption({ index: 1 });
        });

        // Give the websocket time to stream the first values into the charts.
        await page.waitForTimeout(9000);
        await page.screenshot({ path: `${OUT}/${theme}-populated.png`, fullPage: false });
        await page.screenshot({ path: `${OUT}/${theme}-populated-full.png`, fullPage: true });
        expect(true).toBe(true);
    });
}
