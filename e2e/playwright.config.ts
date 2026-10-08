import { defineConfig, devices } from '@playwright/test';

/**
 * End-to-end tests: the real server (API, worker, LibreOffice, PostgreSQL)
 * with the built SPA, against the fake Confluence and identity provider.
 * Build the frontend first (`make e2e` does).
 */
export default defineConfig({
  testDir: './tests',
  // One user, one database: the tests share state and run one after another.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 4 * 60_000,
  expect: { timeout: 15_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: 'http://localhost:8080',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    acceptDownloads: true,
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        // A browser already on the machine (e.g. CHROMIUM_PATH=/opt/pw-browsers/chromium) instead of the
        // one `playwright install` downloads.
        launchOptions: process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {},
      },
    },
  ],
  webServer: [
    {
      command: 'go build -o ../e2e/.bin/devmocks ./cmd/devmocks && exec ../e2e/.bin/devmocks',
      cwd: '../backend',
      url: 'http://localhost:8091/.well-known/openid-configuration',
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
    },
    {
      command: './start-server.sh',
      url: 'http://localhost:8080/readyz',
      reuseExistingServer: !process.env.CI,
      timeout: 180_000,
      stdout: 'pipe',
    },
  ],
});
