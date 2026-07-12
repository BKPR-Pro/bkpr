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

// EXIT_SESSION_EXPIRED (EX_TEMPFAIL) means the saved session is gone and no person was present to sign
// in again. The Go side maps this exit code to a clear "run it interactively to refresh" error rather
// than a generic failure.
const EXIT_SESSION_EXPIRED = 75

function env(name, fallback) {
  const v = process.env[name]
  return v === undefined || v === '' ? fallback : v
}

function chromium() {
  try {
    return require('playwright').chromium
  } catch (e) {
    process.stderr.write('bank import needs Node with the "playwright" package installed (npm i -g playwright)\n')
    process.exit(1)
  }
}

async function launch(headless) {
  const opts = { headless }
  const exe = env('BK_IMPORT_BROWSER_PATH', '')
  if (exe) opts.executablePath = exe
  return chromium().launch(opts)
}

// run drives one profile end to end. A profile is the only institution-specific part:
//   isLoginWall(page): Promise<boolean>  are we looking at the sign-in page (no/expired session)?
//   signIn(page):      Promise<void>     wait for the person to finish signing in (2FA included)
//   readRows(page):    Promise<row[]>    read the account's transactions, newest or oldest first,
//                                        each { date, description, amount, currency? }
async function run(profile) {
  const url = env('BK_IMPORT_URL', '')
  if (!url) {
    process.stderr.write('BK_IMPORT_URL is required\n')
    process.exit(1)
  }
  const sessionFile = env('BK_IMPORT_SESSION_FILE', '')
  const interactive = env('BK_IMPORT_INTERACTIVE', '') === '1'
  const relogin = env('BK_IMPORT_RELOGIN', '') === '1'
  const haveSaved = !relogin && sessionFile && fs.existsSync(sessionFile)

  // First try headless with whatever session we have. A live session lands on the account directly; a
  // missing or expired one is bounced to the sign-in page, which is the profile's isLoginWall.
  let browser = await launch(true)
  let context = await browser.newContext(haveSaved ? { storageState: sessionFile } : {})
  let page = await context.newPage()
  await page.goto(url, { waitUntil: 'domcontentloaded' })

  if (await profile.isLoginWall(page)) {
    if (!interactive) {
      process.stderr.write('session expired; re-run interactively to sign in again\n')
      await browser.close()
      process.exit(EXIT_SESSION_EXPIRED)
    }
    // Reopen headed so the person can enter the password and complete 2FA themselves. bookkeeper never
    // sees either; it only keeps the session that results.
    await browser.close()
    try {
      browser = await launch(false)
    } catch (e) {
      // A terminal with no display (a headless server, SSH without X). Signing in needs a browser
      // window, so say so plainly rather than spill Playwright's launch log.
      process.stderr.write('cannot open a browser window to sign in (no display); run this on a machine where a browser can open\n')
      process.exit(1)
    }
    context = await browser.newContext()
    page = await context.newPage()
    await page.goto(url, { waitUntil: 'domcontentloaded' })
    await profile.signIn(page)
    if (sessionFile) {
      fs.mkdirSync(path.dirname(sessionFile), { recursive: true })
      await context.storageState({ path: sessionFile })
      fs.chmodSync(sessionFile, 0o600) // a session is machine-local; keep it to its owner
    }
    // Signing in usually lands on a dashboard; go to the specific account we were asked for.
    await page.goto(url, { waitUntil: 'domcontentloaded' })
  }

  const rows = await profile.readRows(page)
  process.stdout.write(JSON.stringify(rows))
  await browser.close()
}

module.exports = { run, EXIT_SESSION_EXPIRED }
