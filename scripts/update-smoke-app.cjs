// Drives the dashboard inside the running desktop app over its
// --remote-debugging-port, for scripts/update-smoke.ps1.
//   node update-smoke-app.cjs <playwright> probe|click <cdp endpoint>
const { chromium } = require(process.argv[2])
const [mode, endpoint] = process.argv.slice(3)

;(async () => {
  const browser = await chromium.connectOverCDP(endpoint)
  const context = browser.contexts()[0]
  const page = context.pages()[0] || (await context.waitForEvent('page'))
  await page.waitForURL(/^http:\/\/127\.0\.0\.1:\d+\//, { timeout: 45000 })
  if (mode === 'probe') {
    const status = await page.evaluate(() => fetch('/').then((response) => response.status))
    console.log(JSON.stringify({ url: page.url().replace(/\?.*/, ''), status }))
    if (status !== 200) process.exitCode = 1
  } else {
    await page.getByRole('button', { name: /^Update$/ }).click({ timeout: 90000 })
    const button = page.getByRole('button', { name: 'Update now' })
    await button.waitFor({ timeout: 30000 })
    console.log('dialog:', (await page.getByRole('dialog').innerText()).replace(/\s+/g, ' '))
    await button.click()
    console.log('clicked Update now')
    // The app may quit under us while the update finishes; that is the point.
    await page.waitForTimeout(3000).catch(() => {})
    console.log('after:', (await page.getByRole('dialog').innerText().catch(() => '(window gone)')).replace(/\s+/g, ' '))
  }
  await browser.close().catch(() => {})
})().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
