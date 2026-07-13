// The bank-import harness. Every institution script (rbc.js, simplii.js, pcfinancial.js) is just a
// profile -- a handful of institution-specific selectors -- handed to run() below. Everything that is
// the same for every bank lives here: loading a saved browser session, signing in again when it has
// expired, persisting the fresh session, reading the rows, and printing them as the JSON the Go side
// expects:
//
//   [{ "date": "2026-03-01", "description": "SHELL GAS", "amount": "-62.40" }]
//
// date is YYYY-MM-DD, amount is a plain signed decimal (money out is negative), and currency is
// optional (bookkeeper applies the connector's default when a row omits it).
//
// Go runs an institution script with the connector's details in the environment:
//   BK_IMPORT_URL           the account's URL (several accounts at one bank differ only by this)
//   BK_IMPORT_ACCOUNT       the ledger account these lines belong to (for reference)
//   BK_IMPORT_CURRENCY      the account's default currency (Go applies it; rows may omit their own)
//   BK_IMPORT_SESSION_FILE  where this credential's browser session is saved, read at start and
//                           written back after a fresh sign-in; several connectors that share a login
//                           share this file, so signing in through one signs in all of them
//   BK_IMPORT_INTERACTIVE   "1" when a person is present to complete a sign-in (password + 2FA); when
//                           it is not set and the session has expired, the run fails fast instead of
//                           opening a browser nobody is watching
//   BK_IMPORT_RELOGIN       "1" to ignore any saved session and sign in fresh
//   BK_IMPORT_BROWSER_PATH  optional path to a Chromium/Chrome executable; when unset Playwright uses
//                           the browser it installed
//
// The password and any 2FA are entered by the person in the headed browser and are never seen by
// bookkeeper or written anywhere: only the resulting session (cookies + storage) is saved, and a
// session is not the credential -- it expires, which is the whole reason to prefer it.

const fs = require('fs')
const path = require('path')
const os = require('os')

// EXIT_SESSION_EXPIRED (EX_TEMPFAIL) means the saved session is gone and no person was present to sign
// in again. The Go side maps this exit code to a clear "run it interactively to refresh" error rather
// than a generic failure.
const EXIT_SESSION_EXPIRED = 75

function env(name, fallback) {
  const v = process.env[name]
  return v === undefined || v === '' ? fallback : v
}

// progress reports the current stage so the Go side can show a live status while the browser works.
// It goes to stderr, prefixed, so it is separable from real error output and never touches stdout
// (which carries the JSON result). PROGRESS_PREFIX matches the Go side's progressPrefix.
const PROGRESS_PREFIX = '@@BKPROG@@ '
function progress(msg) {
  process.stderr.write(PROGRESS_PREFIX + msg + '\n')
}

function chromium() {
  try {
    return require('playwright').chromium
  } catch (e) {
    process.stderr.write('bank import needs Node with the "playwright" package installed (npm i -g playwright)\n')
    process.exit(1)
  }
}

// patchright is a drop-in Playwright whose runtime closes the CDP automation leaks a bot manager
// scores; a bank behind one (Simplii/CIBC's Akamai) needs it to sign in at all.
function patchrightChromium() {
  try {
    return require('patchright').chromium
  } catch (e) {
    process.stderr.write('this bank needs the "patchright" package to get past its bot protection (npm i -g patchright)\n')
    process.exit(1)
  }
}

async function launch(headless) {
  const opts = { headless }
  const exe = env('BK_IMPORT_BROWSER_PATH', '')
  if (exe) opts.executablePath = exe
  return chromium().launch(opts)
}

// userAgent overrides Playwright's default, which advertises "HeadlessChrome" -- banks route that to a
// stripped-down or bot-blocked flow (RBC sends it to a device-verification page instead of the
// security-question one). A plain desktop-Chrome string gets the normal flow. Kept stable (not
// randomized) so a saved session, which a bank may tie to its user agent, keeps working. Overridable.
function userAgent() {
  return env(
    'BK_IMPORT_USER_AGENT',
    'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36'
  )
}

// newBankContext opens a context with the real user agent, carrying a saved session when present.
async function newBankContext(browser, sessionFile) {
  const opts = { userAgent: userAgent() }
  if (sessionFile && fs.existsSync(sessionFile)) opts.storageState = sessionFile
  return browser.newContext(opts)
}

// readCredentials reads the resolved credentials Go passes as a JSON object on stdin (never on the
// command line or in the environment). It is {} when there are none, in which case sign-in falls back
// to a saved session or a headed person.
function readCredentials() {
  try {
    const data = fs.readFileSync(0, 'utf8').trim()
    return data ? JSON.parse(data) : {}
  } catch (e) {
    return {}
  }
}

// accountPath is the ordered list of link (or button) labels to click, after signing in, to reach the
// specific account -- for a bank whose accounts have no stable URL and are reached by menu (RBC:
// ["Go to RBC Business Banking", "Current Account"]). Empty means the URL itself is the account.
function accountPath() {
  try {
    const p = JSON.parse(env('BK_IMPORT_ACCOUNT_PATH', '') || '[]')
    return Array.isArray(p) ? p : []
  } catch (e) {
    return []
  }
}

// stepLocator resolves one account-path step to a clickable element. A step is a CSS selector when it
// begins with '#', '.', '[', or 'css=' (RBC's menu link is #gotoBusinessHref); 'text=' forces a
// visible-text match; otherwise it is a link or button by accessible name, falling back to any element
// with that visible text (RBC's account and range are plain text: "Current Account", "30 days").
function stepLocator(page, step) {
  const s = String(step)
  if (s.startsWith('css=')) return page.locator(s.slice(4))
  if (s.startsWith('text=')) return page.getByText(s.slice(5))
  if (/^[#.[]/.test(s)) return page.locator(s)
  return page.getByRole('link', { name: s }).or(page.getByRole('button', { name: s })).or(page.getByText(s))
}

// navigatePath clicks each step in turn and waits for the page to settle between clicks. It is how one
// login reaches any of its accounts.
async function navigatePath(page, path) {
  for (const step of path) {
    progress('opening ' + step)
    await dismissCookieBanner(page)
    try {
      await stepLocator(page, step).first().click({ timeout: 2500 })
    } catch (e) {
      throw new Error('could not open "' + step + '" -- it was not found on the page reached after sign-in; the snapshot shows where it stopped')
    }
    await page.waitForLoadState('networkidle').catch(() => {})
  }
}

// saveSession persists the browser session (cookies + storage) after a sign-in, so later runs reuse
// it headless. The file is a live credential, kept to its owner.
async function saveSession(context, sessionFile) {
  if (!sessionFile) return
  fs.mkdirSync(path.dirname(sessionFile), { recursive: true })
  await context.storageState({ path: sessionFile })
  fs.chmodSync(sessionFile, 0o600)
}

// blockCookieBanners stops a consent banner from ever loading, by aborting requests to the common
// consent CDNs. RBC (and Simplii) render a OneTrust banner whose overlay intercepts clicks and typing,
// stalling an unattended sign-in; blocking it at the network is race-free, needing no wait for the
// banner to appear before dismissing it.
async function blockCookieBanners(context) {
  try {
    await context.route(/onetrust|cookielaw\.org|cookiepro/i, route => route.abort())
  } catch (e) {}
}

// dismissCookieBanner removes any consent widget already in the page (an inline one, or one that
// slipped past the network block) and clears the scroll-lock such widgets add, so the form beneath is
// interactable. Best-effort: absent, it does nothing.
async function dismissCookieBanner(page) {
  try {
    await page.evaluate(() => {
      for (const id of ['onetrust-consent-sdk', 'onetrust-banner-sdk', 'ot-sdk-container']) {
        const el = document.getElementById(id)
        if (el) el.remove()
      }
      document.documentElement.style.overflow = ''
      document.body.style.overflow = ''
    })
  } catch (e) {}
}

// saveSnapshot writes the HTML of the page about to be read to BK_IMPORT_SNAPSHOT_FILE, with the URL
// it came from, so a run that reads nothing or the wrong thing can be diagnosed from what the browser
// actually saw. The directory is made self-ignoring: the page holds real statement data that must not
// be committed. A snapshot is a debugging aid, so a failure to write one never fails the import.
async function saveSnapshot(page) {
  const file = env('BK_IMPORT_SNAPSHOT_FILE', '')
  if (!file) return
  try {
    const dir = path.dirname(file)
    fs.mkdirSync(dir, { recursive: true })
    fs.writeFileSync(path.join(dir, '.gitignore'), '*\n!.gitignore\n')
    const header = `<!-- bookkeeper snapshot: ${page.url()} at ${new Date().toISOString()} -->\n`
    fs.writeFileSync(file, header + (await page.content()))
  } catch (e) {
    process.stderr.write('snapshot: ' + e.message + '\n')
  }
}

// run drives one profile end to end. A profile is the only institution-specific part:
//   isLoginWall(page): Promise<boolean>   are we looking at the sign-in page (no/expired session)?
//   signIn(page):      Promise<void>      wait for the person to finish signing in (2FA included)
//   readRows(page):    Promise<row[]>     read the account's transactions, newest or oldest first,
//                                         each { date, description, amount, currency? }
//   readBalance(page): Promise<string?>   optional: the account's current balance as a plain signed
//                                         decimal in the books' sign (a card's balance owing is
//                                         negative). Return null/undefined to record no balance.
// runStealth is run() for a bank behind bot protection that blocks any browser Playwright launches
// (Simplii/CIBC's Akamai). It drives real Chrome through patchright -- closing the CDP automation leak
// the bot manager scores -- in a persistent profile, so the "device" is remembered across runs and the
// reads look like an ordinary browser. Headed only when a person is on hand to complete a one-time
// code; otherwise headless. The persistent profile stands in for a saved session.
async function runStealth(profile) {
  const url = env('BK_IMPORT_URL', '')
  if (!url) {
    process.stderr.write('BK_IMPORT_URL is required\n')
    process.exit(1)
  }
  const interactive = env('BK_IMPORT_INTERACTIVE', '') === '1'
  const credentials = readCredentials()
  const canAutoSignIn = !!(credentials.username && credentials.password)
  const accPath = accountPath()
  // A stable per-machine profile directory keeps the bank's device trust so later runs skip the code.
  const profileDir = env('BK_IMPORT_SESSION_FILE', '') || path.join(os.tmpdir(), 'bk-stealth-profile')

  let context
  let page
  try {
    progress('starting a browser')
    context = await patchrightChromium().launchPersistentContext(profileDir, {
      channel: 'chrome',
      headless: !interactive,
      viewport: null,
    })
    await blockCookieBanners(context)
    page = context.pages()[0] || (await context.newPage())
    progress('opening the bank')
    await page.goto(url, { waitUntil: 'domcontentloaded' })
    await page.waitForLoadState('networkidle').catch(() => {})
    await dismissCookieBanner(page)

    if (await profile.isLoginWall(page)) {
      if (!interactive && !canAutoSignIn) {
        await saveSnapshot(page)
        process.stderr.write('session expired; re-run interactively or configure credentials to sign in again\n')
        await context.close()
        process.exit(EXIT_SESSION_EXPIRED)
      }
      progress(interactive ? 'waiting for you to sign in' : 'signing in')
      await profile.signIn(page, credentials)
    }

    await navigatePath(page, accPath)
    progress('reading transactions')
    await dismissCookieBanner(page)
    await saveSnapshot(page)
    const rows = await profile.readRows(page)
    let balance = null
    if (profile.readBalance) {
      const b = await profile.readBalance(page)
      if (b !== null && b !== undefined && String(b).trim() !== '') balance = String(b).trim()
    }
    process.stdout.write(JSON.stringify({ rows, balance }))
  } catch (e) {
    if (page) await saveSnapshot(page)
    throw e
  } finally {
    if (context) await context.close().catch(() => {})
  }
}

//
// signIn receives the resolved credentials as its second argument. With a username and password it
// signs in unattended (headless); with none it waits for the person signing in headed.
async function run(profile) {
  // A bank behind a bot manager (Simplii) can never sign in through a browser Playwright launches, so
  // it takes the stealth path -- real Chrome via patchright in a persistent profile -- instead.
  if (profile.stealth) return runStealth(profile)

  const url = env('BK_IMPORT_URL', '')
  if (!url) {
    process.stderr.write('BK_IMPORT_URL is required\n')
    process.exit(1)
  }
  const sessionFile = env('BK_IMPORT_SESSION_FILE', '')
  const interactive = env('BK_IMPORT_INTERACTIVE', '') === '1'
  const relogin = env('BK_IMPORT_RELOGIN', '') === '1'
  const credentials = readCredentials()
  const canAutoSignIn = !!(credentials.username && credentials.password)
  const haveSaved = !relogin && sessionFile && fs.existsSync(sessionFile)
  const path = accountPath()

  let browser
  let context
  let page
  try {
    // First try headless with whatever session we have. A live session lands on the account directly;
    // a missing or expired one is bounced to the sign-in page, which is the profile's isLoginWall.
    progress('starting a browser')
    browser = await launch(true)
    context = await newBankContext(browser, haveSaved ? sessionFile : '')
    await blockCookieBanners(context)
    page = await context.newPage()
    progress('opening the bank')
    await page.goto(url, { waitUntil: 'domcontentloaded' })
    // The bank's page is often an Angular/SPA app that renders the sign-in form after the initial
    // load; wait for the network to settle so isLoginWall does not run against a still-empty page and
    // wrongly conclude we are already signed in.
    await page.waitForLoadState('networkidle').catch(() => {})
    await dismissCookieBanner(page)

    if (await profile.isLoginWall(page)) {
      if (canAutoSignIn) {
        progress('signing in')
        // Sign in headless by typing the resolved credentials; no person is needed. The session that
        // results is saved and reused, so credentials are resolved only when the session has expired.
        await profile.signIn(page, credentials)
        await saveSession(context, sessionFile)
        if (path.length === 0) await page.goto(url, { waitUntil: 'domcontentloaded' })
      } else if (interactive) {
        // Reopen headed so the person can enter the password and complete 2FA themselves. bookkeeper
        // never sees either; it only keeps the session that results.
        await browser.close()
        try {
          browser = await launch(false)
        } catch (e) {
          // A terminal with no display (a headless server, SSH without X). Signing in needs a browser
          // window, so say so plainly rather than spill Playwright's launch log.
          process.stderr.write('cannot open a browser window to sign in (no display); run this on a machine where a browser can open\n')
          process.exit(1)
        }
        context = await newBankContext(browser, '')
        await blockCookieBanners(context)
        page = await context.newPage()
        await page.goto(url, { waitUntil: 'domcontentloaded' })
        await page.waitForLoadState('networkidle').catch(() => {})
        await dismissCookieBanner(page)
        progress('waiting for you to sign in')
        await profile.signIn(page, credentials)
        await saveSession(context, sessionFile)
        // Signing in usually lands on a dashboard; when the account is a plain URL, go back to it.
        if (path.length === 0) await page.goto(url, { waitUntil: 'domcontentloaded' })
      } else {
        await saveSnapshot(page) // leave the login wall behind for diagnosis
        process.stderr.write('session expired; re-run interactively or configure credentials to sign in again\n')
        await browser.close()
        process.exit(EXIT_SESSION_EXPIRED)
      }
    }

    // Reach the specific account by clicking through its menu path, for a bank whose accounts have no
    // stable URL (RBC). With no path, the URL already is the account.
    await navigatePath(page, path)

    progress('reading transactions')
    await dismissCookieBanner(page)
    await saveSnapshot(page) // capture the page the reader is about to parse, for diagnosis
    const rows = await profile.readRows(page)
    let balance = null
    if (profile.readBalance) {
      const b = await profile.readBalance(page)
      if (b !== null && b !== undefined && String(b).trim() !== '') balance = String(b).trim()
    }
    process.stdout.write(JSON.stringify({ rows, balance }))
  } catch (e) {
    // Whatever went wrong -- a sign-in that stalled, a menu step that was not found -- leave a
    // snapshot of the page it stopped on, which is where the fix begins.
    if (page) await saveSnapshot(page)
    throw e
  } finally {
    if (browser) await browser.close().catch(() => {})
  }
}

module.exports = { run, EXIT_SESSION_EXPIRED }
