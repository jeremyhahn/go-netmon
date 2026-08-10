import { defineConfig, devices } from '@playwright/test';

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
    // The web UI is run externally (docker compose), and the published port
    // moved to 8082 because 8081 was already taken on this host. Override with
    // NETMON_URL to point at a different deployment.
    baseURL: process.env.NETMON_URL || 'http://localhost:8082',
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
        // Playwright's own browser download is not present on this host; the
        // system Chrome is used instead.
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
