import { defineConfig, devices } from '@playwright/test'

const port = process.env.WEB_PORT || '5173'
const baseURL = `http://127.0.0.1:${port}`

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  reporter: 'list',
  outputDir: '../../outputs/web-check/test-results',
  use: { baseURL, trace: 'retain-on-failure', launchOptions: { channel: 'chrome' } },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 1000 } } },
    { name: 'mobile', use: { ...devices['iPhone 13'], defaultBrowserType: 'chromium' } },
  ],
  webServer: { command: `npm run dev -- --port ${port}`, url: `${baseURL}/page/auth/login/`, reuseExistingServer: !process.env.CI },
})
