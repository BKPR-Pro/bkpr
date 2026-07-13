// PC Financial Mastercard online banking. Unlike Simplii (its CIBC sibling), a plain browser reaches
// PC Financial fine -- no bot-manager wall -- so this takes the harness's normal path: a saved session,
// reused headless, and a headed sign-in only when it has expired. The harness owns the session and the
// JSON output; this file is only PC Financial's specifics.
//
// The sign-in is pinned; the transaction reader is not yet -- readRows/readBalance are marked
// TODO(pcfinancial) until the real account page is captured (npx playwright codegen), so until then an
// import signs in but reads nothing rather than inventing data. A manual CSV export also works today
// with `import <file>.csv`.
const { run } = require('./harness.js')

// PC Financial's sign-in form is rendered inside a ThreatMetrix iframe (#tmx_tags_iframe), and its
// verification step reloads that frame, so the fields live in whichever frame currently holds them.
// findInFrames polls every frame until the target is visible, returning its locator (or null). locate
// maps a frame to the field's locator, so a field can be matched by accessible role/name across frames.
async function findInFrames(page, locate, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    for (const frame of page.frames()) {
      const loc = locate(frame).first()
      if (await loc.isVisible().catch(() => false)) return loc
    }
    if (Date.now() >= deadline) return null
    await page.waitForTimeout(500)
  }
}

// onLoginPage is true while the Username field is present in any frame -- the one element unique to the
// sign-in step. It tells an expired session (sign-in shown) from a signed-in view, and marks when a
// sign-in has completed (the form is gone).
async function onLoginPage(page) {
  for (const frame of page.frames()) {
    // A frame can detach mid-scan (the iframe navigating between steps, or being torn down at sign-in);
    // treat a detached frame as simply not holding the field.
    const n = await frame.getByRole('textbox', { name: /username/i }).count().catch(() => 0)
    if (n > 0) return true
  }
  return false
}

// signedIn is true once the authenticated app shell (the post-login <app-auth-header>) is present --
// the signal that sign-in, including any texted verification code, has fully completed. It must key on
// this rather than "the Username field is gone", because the intervening verification page (choose a
// method, enter the code) also has no Username, and returning there would read the wrong page.
async function signedIn(page) {
  return (await page.locator('app-auth-header').count().catch(() => 0)) > 0
}

// PC Financial prints dates as "May 20, 2026"; the books want 2026-05-20. Unknown formats pass through
// so the Go side fails loudly rather than this inventing a date.
const MONTHS = { jan: '01', feb: '02', mar: '03', apr: '04', may: '05', jun: '06', jul: '07', aug: '08', sep: '09', oct: '10', nov: '11', dec: '12' }
function toISO(text) {
  const m = (text || '').trim().match(/^([A-Za-z]{3})[a-z]*\.?\s+(\d{1,2}),\s+(\d{4})$/)
  if (!m) return (text || '').trim()
  const mm = MONTHS[m[1].toLowerCase()]
  return mm ? `${m[3]}-${mm}-${m[2].padStart(2, '0')}` : (text || '').trim()
}

// readCurrentPage reads the visible page of the Mastercard's transaction table into
// { isoDate, description, amount }. PC Financial tags a charge amount "positive" (it adds to the
// balance owing) and a payment/refund "negative"; the books hold a card negative, so a charge becomes
// a negative amount and a payment a positive one -- the flip of PC Financial's own sense.
async function readCurrentPage(page) {
  const raw = await page.locator('sortable-table table tbody tr.clickable').evaluateAll(trs => trs.map(tr => {
    const amountCell = tr.querySelector('td.amount')
    const dateCell = tr.querySelector('td.date span')
    const descText = tr.querySelector('td.description .description-text')
    return {
      date: (dateCell ? dateCell.textContent : (tr.querySelector('td.date')?.textContent || '')).trim(),
      description: (descText?.textContent || '').replace(/\s+/g, ' ').trim(),
      amount: (amountCell?.textContent || '').replace(/[$,\s]/g, ''),
      isPayment: !!(amountCell && amountCell.classList.contains('negative')),
    }
  }))
  return raw
    .filter(r => r.date && r.amount)
    .map(r => ({
      isoDate: toISO(r.date),
      description: r.description,
      amount: r.isPayment ? r.amount : '-' + r.amount,
    }))
}

// gotoNextPage advances PC Financial's numbered paginator, returning whether it moved. It clicks Next
// and confirms the active page number changed, so the last page (Next present but inert, or absent)
// stops the crawl rather than looping.
async function gotoNextPage(page) {
  // count() returns at once; check it first so a page with no paginator stops the crawl immediately
  // rather than blocking on a textContent() wait.
  const next = page.locator('paginator .next-button button').first()
  if ((await next.count()) === 0) return false
  if (await next.isDisabled().catch(() => false)) return false
  const activeText = () => page.locator('paginator .page-number.active button').first().textContent({ timeout: 1000 }).catch(() => null)
  const before = await activeText()
  await next.click().catch(() => {})
  await page.waitForTimeout(1200)
  return (await activeText()) !== before
}

run({
  // On the sign-in page (no/expired session) while the Username field is present.
  async isLoginWall(page) {
    return await onLoginPage(page)
  },

  // Sign in. With a username and password (resolved from the secret store) this fills the two
  // iframe-nested fields, submits, and picks SMS as the verification method so a code is texted; the
  // person types that code in the headed window -- it is never seen or stored here. With no
  // credentials a person signs in entirely by hand. Either way the run waits, generously, until the
  // sign-in form is gone (the signed-in app has loaded).
  async signIn(page, creds) {
    if (creds && creds.username && creds.password) {
      // Best-effort auto-fill: wait for the slow ThreatMetrix iframe to render its fields, then fill.
      const user = await findInFrames(page, f => f.getByRole('textbox', { name: /username/i }), 30 * 1000)
      if (user) {
        await user.fill(creds.username)
        const pw = await findInFrames(page, f => f.getByRole('textbox', { name: /password/i }), 5 * 1000)
        if (pw) await pw.fill(creds.password)
        // Submit: the "Sign in" button, else Enter in the password field.
        const button = await findInFrames(page, f => f.getByRole('button', { name: /sign ?in/i }), 5 * 1000)
        if (button) await button.click().catch(() => {})
        else if (pw) await pw.press('Enter').catch(() => {})
        // The verification step: choose SMS so the code is texted. Absent on a trusted device, so this
        // is best-effort -- the person completes the code in the window.
        const method = await findInFrames(page, f => f.getByLabel(/choose a verification method/i), 15 * 1000)
        if (method) await method.selectOption('ChallengeSMS').catch(() => {})
      }
    }
    // Signed in once the authenticated app shell has loaded. Generous, to allow a texted code the
    // person enters, and keyed on the signed-in header (not merely the sign-in form being gone, which
    // is also true of the verification page in between).
    const deadline = Date.now() + 5 * 60 * 1000
    for (;;) {
      if (await signedIn(page)) return
      if (Date.now() >= deadline) {
        throw new Error('timed out waiting for the signed-in PC Financial dashboard; check the snapshot for where it stopped')
      }
      await page.waitForTimeout(1000)
    }
  },

  // Read the card's transactions into { date, description, amount }. A charge is money owed, so it is
  // negative; a payment or refund is positive (readCurrentPage applies that flip). PC Financial paginates
  // newest-first, so page back until the requested history start (BK_IMPORT_HISTORY_FROM) is passed, then
  // keep only rows on or after it. With no window set, this reads the first page.
  async readRows(page) {
    const fromEnv = process.env.BK_IMPORT_HISTORY_FROM || ''
    const from = fromEnv ? new Date(fromEnv) : null
    const fromValid = !!(from && !isNaN(from.getTime()))

    // Wait briefly for the table so a still-loading page is not misread as empty; a genuinely empty
    // account falls through to zero rows, with the snapshot left for diagnosis.
    await page.locator('sortable-table table tbody tr.clickable').first().waitFor({ timeout: 2500 }).catch(() => {})

    const all = []
    for (let i = 0; i < 60; i++) {
      const pageRows = await readCurrentPage(page)
      if (pageRows.length === 0) break
      all.push(...pageRows)
      // The list is newest-first: once a page reaches past the from-date, no older page is needed.
      if (fromValid) {
        const oldest = pageRows.reduce((min, r) => {
          const d = new Date(r.isoDate)
          return isNaN(d.getTime()) || (min && d >= min) ? min : d
        }, null)
        if (oldest && oldest < from) break
      }
      if (!(await gotoNextPage(page))) break
    }

    return all
      .filter(r => {
        if (!fromValid) return true
        const d = new Date(r.isoDate)
        return isNaN(d.getTime()) ? true : d >= from
      })
      .map(r => ({ date: r.isoDate, description: r.description, amount: r.amount }))
  },

  // The card's current balance for reconciliation, as a positive magnitude read from the "Current
  // balance" tile; the Go side negates it for the liability (a Mastercard's balance owing is negative).
  // Return null to record no balance.
  async readBalance(page) {
    const bal = await page.evaluate(() => {
      for (const block of document.querySelectorAll('balance-block')) {
        const desc = block.querySelector('.description')
        if (desc && /current balance/i.test(desc.textContent || '')) {
          const amt = block.querySelector('.amount')
          if (amt) return amt.textContent || ''
        }
      }
      return null
    })
    if (bal == null) return null
    const magnitude = bal.replace(/[$,\s]/g, '').replace(/^-/, '')
    return magnitude || null
  },
}).catch(e => { process.stderr.write('pcfinancial: ' + e.message + '\n'); process.exit(1) })
