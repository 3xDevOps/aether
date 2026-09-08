import { defineConfig, devices } from '@playwright/test'

// The end-to-end suite drives the built dashboard in a browser against a
// real `aether gui` gateway and a real `aether-server`. Each test starts its
// own server, so there is no shared base URL and no `webServer` here: the
// `aether` fixture in e2e/fixtures.ts owns every process.
//
// One worker. The suite runs real containers against one Docker daemon, and
// a serial run is what makes a CI failure reproducible locally.
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  // A scenario that starts a container and waits for a run to finish is
  // minutes of real work, not milliseconds of rendering.
  timeout: 5 * 60 * 1000,
  expect: { timeout: 30 * 1000 },
  globalTimeout: 30 * 60 * 1000,
  reporter: process.env.CI
    ? [['github'], ['list'], ['html', { open: 'never' }]]
    : [['list']],
  use: {
    // Bounded so a locator that matches nothing fails in seconds rather than
    // burning the whole test timeout, which the run wait needs.
    actionTimeout: 30 * 1000,
    navigationTimeout: 30 * 1000,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
