// Simplii Financial online banking (CIBC's direct-banking division). Simplii sits behind Akamai Bot
// Manager, which blocks any browser Playwright launches at the sign-in call; the harness's stealth
// mode (patchright + real Chrome + a persistent profile) is what gets past it, so this profile sets
// `stealth: true`. One login serves every account -- chequing, savings, line of credit -- reached by
// clicking through "Banking" to the account's link; the harness owns the session and the JSON output,
// this file is only Simplii's selectors.
const { run } = require('./harness.js')

// Simplii prints dates as "Jul 10, 2026"; the books want 2026-07-10. Unknown formats pass through so
// the Go side fails loudly rather than inventing a date.
const MONTHS = { jan: '01', feb: '02', mar: '03', apr: '04', may: '05', jun: '06', jul: '07', aug: '08', sep: '09', oct: '10', nov: '11', dec: '12' }
function toISO(text) {
  const m = text.trim().match(/^([A-Za-z]{3})[a-z]*\.?\s+(\d{1,2}),\s+(\d{4})$/)
  if (!m) return text.trim()
  const mm = MONTHS[m[1].toLowerCase()]
  return mm ? `${m[3]}-${mm}-${m[2].padStart(2, '0')}` : text.trim()
}

// clean turns Simplii's money text into a plain decimal: its running balances use a Unicode minus
// (U+2212), and amounts carry $ and thousands commas.
function clean(text) {
  return (text || '').replace(/−/g, '-').replace(/[$,\s]/g, '')
}

// The sign-in fields may render on the page itself or inside the auth widget's iframe, and the widget
// loads slowly; findField polls every frame until the field appears, returning its locator (or null).
async function findField(page, selector, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    for (const frame of page.frames()) {
      const loc = frame.locator(selector).first()
      if (await loc.isVisible().catch(() => false)) return loc
    }
    if (Date.now() >= deadline) return null
    await page.waitForTimeout(500)
  }
}

run({
  // Simplii needs the stealth launch (patchright + real Chrome + persistent profile) to clear Akamai.
  stealth: true,

  // On the sign-in page until the signed-in chrome (the profile initials link, or the account picker)
  // is present.
  async isLoginWall(page) {
    return (await page.locator('[data-test-id="sidebar-profile-link-header"], select[aria-label*="Select an account"]').count()) === 0
  },

  // simplii-chequing and simplii-loc share one login and run back to back in the same persistent
  // profile (import -all groups connectors by login). The site is a hash-routed SPA that resumes
  // whatever route the profile last left off on -- landing on the bare origin after the prior
  // connector's run does NOT reset it, so a run can start deep on that connector's own account page
  // instead of the accounts overview its account-path steps expect (e.g. "No Fee Chequing Account"
  // is a link on the overview, not on the line-of-credit page). Force the overview explicitly so the
  // account path always starts from the same place regardless of where the shared session was left.
  async beforeNavigate(page) {
    const origin = new URL(page.url()).origin
    await page.goto(origin + '/ebm-resources/public/simplii/online-banking/accounts/client/index.html#/accounts', { waitUntil: 'domcontentloaded' })
    await page.waitForLoadState('networkidle').catch(() => {})
  },

  // Sign in. With a card number and password (resolved from the secret store) this fills the two
  // iframe-nested fields and submits; a person completes any one-time code in the headed window. The
  // persistent profile means Simplii remembers this device, so later runs should skip the code.
  async signIn(page, creds) {
    if (creds && creds.username && creds.password) {
      // Best-effort auto-fill: find the card field in whatever frame the slow-loading auth widget puts
      // it (waiting for a real element, not a padded delay). If it never appears, fall through and let
      // the person sign in by hand in the headed window.
      const card = await findField(page, '[data-test-id="card-number-input"]', 30 * 1000)
      if (card) {
        await card.fill(creds.username)
        const pw = await findField(page, '[data-test-id="password-input"]', 5 * 1000)
        if (pw) await pw.fill(creds.password)
        // The sign-in button (data-test-id="primary-button", "Sign in"), else Enter.
        const button = await findField(page, '[data-test-id="primary-button"]', 5 * 1000)
        if (button) await button.click().catch(() => {})
        else if (pw) await pw.press('Enter').catch(() => {})
      }
    }
    // Wait for a signed-in landmark; generous, to allow a texted one-time code on an unrecognized
    // device.
    await page.locator('[data-test-id="sidebar-profile-link-header"], select[aria-label*="Select an account"]')
      .first().waitFor({ timeout: 5 * 60 * 1000 })
  },

  // Read the account's transactions. Both the chequing and the line of credit render the same
  // transaction-list table; a real amount is the plain <span>, an empty column is a .hidden-text
  // "Not applicable". Funds in (credit) is positive, funds out (debit) negative -- which is already
  // the books' sign for the asset chequing and the liability line of credit alike, because Simplii
  // carries the credit line's balance negative.
  async readRows(page) {
    await widenHistory(page)
    const raw = await page.locator('section.transaction-list table tbody tr').evaluateAll(trs => trs.map(tr => {
      const dateCell = tr.querySelector('td.date')
      if (!dateCell) return null // header/spacer rows
      const amount = sel => {
        const span = tr.querySelector(sel + ' span:not(.hidden-text)')
        return span ? (span.textContent || '') : ''
      }
      return {
        date: (dateCell.textContent || '').trim(),
        description: (tr.querySelector('.transactionDescription')?.textContent || '').replace(/\s+/g, ' ').trim(),
        debit: amount('td.debit'),
        credit: amount('td.credit'),
      }
    }).filter(Boolean))
    return raw.map(r => {
      const debit = clean(r.debit)
      const credit = clean(r.credit)
      return {
        date: toISO(r.date),
        description: r.description,
        amount: credit ? credit : (debit ? '-' + debit : ''),
      }
    })
  },

  // The account's current balance for reconciliation, as a positive magnitude; the Go side negates it
  // for a liability (the line of credit). Read from the tombstone's "Balance" field.
  async readBalance(page) {
    const bal = await page.evaluate(() => {
      for (const box of document.querySelectorAll('.tombstone .box-small')) {
        const label = box.querySelector('span')
        if (label && /balance/i.test(label.textContent || '')) {
          const em = box.querySelector('em')
          if (em) return em.textContent || ''
        }
      }
      return null
    })
    if (bal == null) return null
    const magnitude = clean(bal).replace(/^-/, '')
    return magnitude || null
  },
}).catch(e => { process.stderr.write('simplii: ' + e.message + '\n'); process.exit(1) })

// widenHistory reads a requested window rather than the default four weeks by driving Simplii's custom
// search: it fills the From (and To) date from BK_IMPORT_HISTORY_FROM/TO -- month, day, year each a
// <select> -- and submits "Get Details". A no-op when no window is set. The date fields are keyed by
// their .from/.to containers and .ui-month/.ui-date/.ui-year classes, which are stable across the
// chequing and line-of-credit pages (their aria-labels are not).
async function widenHistory(page) {
  const from = process.env.BK_IMPORT_HISTORY_FROM || ''
  if (!from) return
  const f = new Date(from)
  if (isNaN(f.getTime())) return
  const pad = n => String(n).padStart(2, '0')
  const setDate = async (container, d) => {
    if (isNaN(d.getTime())) return
    await page.locator(`.filter-by-range ${container} .ui-month select`).selectOption(pad(d.getMonth() + 1)).catch(() => {})
    await page.locator(`.filter-by-range ${container} .ui-date select`).selectOption(pad(d.getDate())).catch(() => {})
    await page.locator(`.filter-by-range ${container} .ui-year select`).selectOption(String(d.getFullYear())).catch(() => {})
  }
  await setDate('.from', f)
  await setDate('.to', new Date(process.env.BK_IMPORT_HISTORY_TO || ''))
  await page.getByRole('button', { name: /get details/i }).first().click().catch(() => {})
  await page.waitForLoadState('networkidle').catch(() => {})
  await page.waitForTimeout(1500)
}
