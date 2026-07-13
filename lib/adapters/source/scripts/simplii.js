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

// The sign-in card lives two iframes deep (title="empty" nested in title="empty"); this resolves the
// inner frame where the card-number and password fields live.
function signInFrame(page) {
  return page.locator('iframe[title="empty"]').contentFrame().locator('iframe[title="empty"]').nth(2).contentFrame()
}

run({
  // Simplii needs the stealth launch (patchright + real Chrome + persistent profile) to clear Akamai.
  stealth: true,

  // On the sign-in page until the signed-in chrome (the profile initials link, or the account picker)
  // is present.
  async isLoginWall(page) {
    return (await page.locator('[data-test-id="sidebar-profile-link-header"], select[aria-label*="Select an account"]').count()) === 0
  },

  // Sign in. With a card number and password (resolved from the secret store) this fills the two
  // iframe-nested fields and submits; a person completes any one-time code in the headed window. The
  // persistent profile means Simplii remembers this device, so later runs should skip the code.
  async signIn(page, creds) {
    if (creds && creds.username && creds.password) {
      const frame = signInFrame(page)
      const card = frame.locator('[data-test-id="card-number-input"]')
      await card.waitFor({ timeout: 2500 })
      await card.fill(creds.username)
      await frame.locator('[data-test-id="password-input"]').fill(creds.password)
      // Submit: a sign-in button if present, otherwise Enter. (Selector to confirm against the live
      // sign-in card; the reader path below is pinned, this is the remaining unknown.)
      const button = frame.locator('[data-test-id="signon-button"], button[type="submit"]')
      if ((await button.count()) > 0) await button.first().click().catch(() => {})
      else await frame.locator('[data-test-id="password-input"]').press('Enter').catch(() => {})
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
