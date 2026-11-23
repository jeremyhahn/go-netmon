import { defineConfig, devices } from '@playwright/test';
import { BASE } from './target';

export default defineConfig({
  testDir: './tests',
  timeout: 60000,
  expect: {
    timeout: 10000
  },
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: [
    ['html', { outputFolder: 'test-results/html' }],
    ['list']
  ],
  use: {
    // The web UI is run externally (docker compose) on published port 8082.
    // Override with
    // NETMON_URL to point at a different deployment.
    baseURL: BASE,
    trace: 'on-first-retry',
    screenshot: 'on',
    video: 'retain-on-failure',
    actionTimeout: 15000,
    navigationTimeout: 30000
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        // The system Chrome is used rather than Playwright's own browser
        // download, which need not be present.
        launchOptions: {
          executablePath: process.env.CHROME_PATH || '/usr/bin/google-chrome',
          args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu'],
        },
        viewport: { width: 1920, height: 1080 }
      },
    },
  ],
  // No webServer block: the stack is started separately with docker compose.
  // A placeholder command here exits immediately and fails every run before a
  // single test executes.
});
