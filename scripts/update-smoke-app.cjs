// Drives the dashboard inside the running desktop app over its
// --remote-debugging-port, for scripts/update-smoke.ps1.
//   node update-smoke-app.cjs <playwright> probe|click <cdp endpoint>
const { chromium } = require(process.argv[2])
const [mode, endpoint] = process.argv.slice(3)
const started = Date.now()
const log = (line) => console.error(`[${((Date.now() - started) / 1000).toFixed(1)}s] ${line}`)

// page.evaluate takes no timeout.
function bounded(what, ms, promise) {
  let timer
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${what} did not finish in ${ms / 1000}s`)), ms)
  })
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer))
}

let browser
let origin

async function explain() {
  if (!browser) return
  const cdp = await bounded('CDP Browser.getVersion', 10000, browser.newBrowserCDPSession().then((s) => s.send('Browser.getVersion')))
    .then((version) => `answers (${version.product})`, (error) => error.message)
  log(`the browser process over CDP: ${cdp}`)
  const pages = browser.contexts().flatMap((context) => context.pages().map((p) => p.url().replace(/\?.*/, '')))
  log(`pages: ${JSON.stringify(pages)}`)
  if (!origin) return
  const direct = await fetch(origin + '/', { signal: AbortSignal.timeout(10000) })
    .then((response) => `HTTP ${response.status}`, (error) => `${error.name}: ${error.message}`)
  log(`GET ${origin}/ from outside the browser: ${direct}`)
}

;(async () => {
  browser = await chromium.connectOverCDP(endpoint, { timeout: 30000 })
  log('connected over CDP')
  const context = browser.contexts()[0]
  const page = context.pages()[0] || (await context.waitForEvent('page', { timeout: 30000 }))
  const gateway = /^http:\/\/127\.0\.0\.1:\d+\//
  // Kept from the first navigation, so a page stuck before load still gets
  // the outside-browser check.
  const note = (url) => {
    if (gateway.test(url)) origin ??= new URL(url).origin
  }
  note(page.url())
  page.on('framenavigated', (frame) => {
    if (frame !== page.mainFrame()) return
    log(`navigated to ${frame.url().replace(/\?.*/, '')}`)
    note(frame.url())
  })
  page.on('crash', () => log('the page crashed'))
  await page.waitForURL(gateway, { timeout: 45000 })
  log(`loaded ${page.url().replace(/\?.*/, '')}`)
  if (mode === 'probe') {
    await bounded('page.evaluate(document.readyState)', 15000, page.evaluate(() => document.readyState))
    // The page's own deadline says whether fetch hung; the outer one
    // whether the evaluate did.
    const status = await bounded(
      "page.evaluate(fetch('/'))",
      40000,
      page.evaluate(() =>
        fetch('/', { signal: AbortSignal.timeout(30000) }).then(
          (response) => response.status,
          (error) => `fetch('/') in the page: ${error.name}: ${error.message}`,
        ),
      ),
    )
    console.log(JSON.stringify({ url: page.url().replace(/\?.*/, ''), status }))
    if (status !== 200) throw new Error(`the probe got ${status}`)
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
  await bounded('browser.close', 10000, browser.close()).catch((error) => log(error.message))
})()
  .catch(async (error) => {
    console.error(error)
    await explain().catch((e) => log(`explain: ${e.message}`))
    process.exitCode = 1
  })
  // A CDP socket left open would keep node alive past its answer.
  .finally(() => process.exit())
