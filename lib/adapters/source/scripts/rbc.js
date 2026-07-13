// RBC Online Banking. One script serves every RBC account -- personal and business, chequing,
// Mastercard, Visa, term loan, line of credit -- which differ only by BK_IMPORT_URL, and (because
// they are one login) share one session, so signing in for any of them signs in for all. The harness
// owns the session, the headed sign-in, and the JSON output; this file is only RBC's specifics,
// pinned against the real site's account page (an RBC business Current Account).
const { run } = require('./harness.js')

// RBC prints dates as "Jul 11, 2026"; the books want 2026-07-11. toISO does that one conversion and
// leaves anything it does not recognize untouched, so the Go side fails loudly on a genuinely
// unexpected format rather than this silently inventing a date.
const MONTHS = { jan: '01', feb: '02', mar: '03', apr: '04', may: '05', jun: '06', jul: '07', aug: '08', sep: '09', oct: '10', nov: '11', dec: '12' }
function toISO(text) {
  const m = text.trim().match(/^([A-Za-z]{3})[a-z]*\.?\s+(\d{1,2}),\s+(\d{4})$/)
  if (!m) return text.trim()
  const mm = MONTHS[m[1].toLowerCase()]
  return mm ? `${m[3]}-${mm}-${m[2].padStart(2, '0')}` : text.trim()
}

// A line of credit still lives on RBC's legacy online banking (www1.royalbank.com), a server-rendered
// site unlike the Angular app the chequing uses. It prints dates as "22 Jun 2026"; toISOLegacy makes
// that 2026-06-22.
function toISOLegacy(text) {
  const m = text.trim().match(/^(\d{1,2})\s+([A-Za-z]{3})\s+(\d{4})$/)
  if (!m) return text.trim()
  const mm = MONTHS[m[2].toLowerCase()]
  return mm ? `${m[3]}-${mm}-${m[1].padStart(2, '0')}` : text.trim()
}

// isLegacy tells the two sites apart: the legacy account pages (a line of credit's grid, a term
// loan's balance-only summary) render inside a template-legacy body; the Angular app does not.
// readRows, readBalance, and applyDateRange each take the branch that matches.
async function isLegacy(page) {
  return (await page.locator('body.template-legacy, #dlHistSort, td.dataTableText').count()) > 0
}

// readRowsLegacy reads the legacy Debits/Credits grid. A line of credit is a liability held negative,
// so a Credit (a payment, which reduces what is owed) is positive and a Debit (an advance) is negative
// -- the opposite column sense from a chequing's Withdrawals/Deposits.
async function readRowsLegacy(page) {
  const rows = await page.locator('#dlHistSort tr.dataTableDarkRow, #dlHistSort tr.dataTableLightRow').evaluateAll(trs => trs.map(tr => {
    // A transaction row's cells are: date, description, debit, credit, balance.
    return Array.from(tr.querySelectorAll('td.dataTableText')).map(td => (td.textContent || '').replace(/ /g, ' ').trim())
  }))
  return rows
    .filter(c => /^\d{1,2}\s+[A-Za-z]{3}\s+\d{4}$/.test(c[0] || '')) // real transaction rows only
    .map(c => {
      const debit = (c[2] || '').replace(/[$,\s]/g, '')
      const credit = (c[3] || '').replace(/[$,\s]/g, '')
      return {
        date: toISOLegacy(c[0]),
        description: c[1] || '',
        amount: credit ? credit : (debit ? '-' + debit : ''),
      }
    })
}

// readBalanceLegacy reads the current balance from a legacy page: a grid account's (a line of credit)
// newest running balance, or a balance-only account's (a term loan) "Current balance" field. Either
// is the amount owing, shown positive; the books' liability sign is applied on the Go side.
async function readBalanceLegacy(page) {
  return await page.evaluate(() => {
    const amount = el => {
      const m = (el.textContent || '').match(/[\d,]+\.\d{2}/)
      return m ? m[0].replace(/,/g, '') : ''
    }
    // A grid account (a line of credit): the newest running balance, in its 5th cell.
    for (const tr of document.querySelectorAll('#dlHistSort tr.dataTableDarkRow, #dlHistSort tr.dataTableLightRow')) {
      const cells = tr.querySelectorAll('td.dataTableText')
      if (cells.length >= 5) {
        const bal = amount(cells[4])
        if (bal) return bal
      }
    }
    // A balance-only account (a term loan): the value beside the "Current balance" label.
    for (const label of document.querySelectorAll('.fieldLabel')) {
      if (/current\s+balance/i.test(label.textContent || '') && label.parentElement) {
        const val = label.parentElement.querySelector('.bodyText')
        if (val) {
          const bal = amount(val)
          if (bal) return bal
        }
      }
    }
    return null
  })
}

// applyDateRangeLegacy sets the legacy from/to date via its select dropdowns (#DAY/#MONTH/#YEAR and
// #RDAY/#RMONTH/#RYEAR) and runs the search, which reloads the grid with the whole range (no paging).
async function applyDateRangeLegacy(page, from, to) {
  const f = new Date(from)
  if (isNaN(f)) return
  await page.locator('#DAY').selectOption(String(f.getDate())).catch(() => {})
  await page.locator('#MONTH').selectOption(String(f.getMonth() + 1)).catch(() => {})
  await page.locator('#YEAR').selectOption(String(f.getFullYear())).catch(() => {})
  const t = new Date(to)
  if (!isNaN(t)) {
    await page.locator('#RDAY').selectOption(String(t.getDate())).catch(() => {})
    await page.locator('#RMONTH').selectOption(String(t.getMonth() + 1)).catch(() => {})
    await page.locator('#RYEAR').selectOption(String(t.getFullYear())).catch(() => {})
  }
  await page.locator('#id_btn_search').click().catch(() => {})
  await page.waitForLoadState('networkidle').catch(() => {})
}

// The client-card / username field, the one element unique to RBC's sign-in page. getByRole matches
// it by its accessible name whatever the underlying markup, so a live or expired session is told
// apart without pinning a brittle id.
function signInField(page) {
  return page.getByRole('textbox', { name: /client card or username/i })
}

// The button that opens the sign-in form from RBC's landing page (its accessible name spells out
// "R B C"). Its presence -- like the username field's -- means we are not signed in.
function signInButton(page) {
  return page.getByRole('button', { name: /sign in to r\s*b\s*c online/i })
}

// signedIn is any landmark that means an authenticated view has loaded: an account's transactions
// grid, an accounts-summary link, or a sign-out control. Used to know a sign-in (headed or unattended)
// has completed before reading, and re-navigating, the account.
function signedIn(page) {
  return page
    .locator('table.rbc-transaction-list-table, [data-role="transaction-list-table-transaction"]')
    .or(page.getByRole('link', { name: /my accounts|accounts summary/i }))
    .or(page.getByRole('link', { name: /sign out|sign off|log ?out/i }))
    .or(page.getByRole('button', { name: /sign out|sign off|log ?out/i }))
}

// waitSignedIn waits for an authenticated landmark. On the credentialed path it is lenient (no throw):
// RBC's post-sign-in landing varies, and the account navigation that follows surfaces a real problem
// with a snapshot. The human path passes throwOnTimeout, so a sign-in that never completes is reported
// rather than read as an empty statement.
async function waitSignedIn(page, timeout, throwOnTimeout) {
  try {
    await signedIn(page).first().waitFor({ timeout })
  } catch (e) {
    if (throwOnTimeout) {
      throw new Error('timed out waiting for an account after sign-in; run interactively, or check the snapshot for where it stopped')
    }
  }
}

// answerSecurityQuestion fills RBC's 2-step security question from the stored answers. An answer is a
// credential keyed "answer:<keyword>", the keyword a distinctive word or phrase from the question
// ("first car", "oldest cousin", "father"); the displayed question is matched against those keywords,
// so the code carries none of RBC's actual questions. Returns whether it answered one.
async function answerSecurityQuestion(page, creds) {
  const answers = Object.keys(creds || {})
    .filter(k => k.toLowerCase().startsWith('answer:'))
    .map(k => ({ needle: k.slice(k.indexOf(':') + 1).trim().toLowerCase(), value: creds[k] }))
    .filter(a => a.needle)
  if (answers.length === 0) return false

  const questionText = ((await page.textContent('body').catch(() => '')) || '').toLowerCase()
  const match = answers.find(a => questionText.includes(a.needle))
  if (!match) return false

  // The answer field is labelled by the question (newer flow) or is the page's lone text input (older).
  const field = page.getByRole('textbox').first()
  await field.waitFor({ timeout: 2500 }).catch(() => {})
  await field.fill(match.value).catch(() => {})
  await page.getByRole('button', { name: /^\s*continue\s*$/i }).first().click().catch(() => {})
  await page.waitForTimeout(2000)
  return true
}

// clearTwoStep walks RBC's 2-step verification after the password, following the working reference
// fetcher's flow and its deliberate fixed waits (RBC's SPA settles slowly between steps). Every step is
// best-effort -- skipped when its page is not the one shown -- because which appear varies by session:
// a direct security question, or the newer "choose a method" page (pick the security question), then a
// "That was me" confirmation, a success page, and a security interstitial.
async function clearTwoStep(page, creds) {
  await page.waitForTimeout(2500) // let the post-password page settle

  // Direct security question.
  if (await page.getByText(/personal verification question/i).first().isVisible({ timeout: 2500 }).catch(() => false)) {
    await answerSecurityQuestion(page, creds)
  }

  // Newer MFA: choose the security-question method rather than a push notification, then answer.
  if (await page.getByRole('button', { name: /get a notification/i }).isVisible({ timeout: 2500 }).catch(() => false)) {
    await page.getByRole('button', { name: /get a notification/i }).click().catch(() => {})
    await page.waitForTimeout(1000)
    await page.getByText(/personal verification question/i).first().click().catch(() => {})
    await page.getByRole('button', { name: /^\s*continue\s*$/i }).first().click().catch(() => {})
    await page.waitForTimeout(2000)
    await answerSecurityQuestion(page, creds)
  }

  // "That was me": confirm a sign-in RBC flagged as unusual.
  if (await page.getByRole('button', { name: /that was me/i }).isVisible({ timeout: 2500 }).catch(() => false)) {
    await page.getByRole('button', { name: /that was me/i }).click().catch(() => {})
    await page.waitForTimeout(2500)
  }

  // Success page: nudge past it toward the dashboard.
  if (page.url().includes('#/success')) {
    const cont = page.getByRole('button', { name: /continue|proceed|go to/i })
    if (await cont.first().isVisible({ timeout: 2500 }).catch(() => false)) await cont.first().click().catch(() => {})
    await page.waitForURL(u => !u.href.includes('#/success'), { timeout: 2500 }).catch(() => {})
  }

  // Security interstitial.
  if (page.url().includes('ISAMSecureRequest')) {
    const cont = page.getByRole('button', { name: /^\s*continue\s*$/i })
    if (await cont.first().isVisible({ timeout: 2000 }).catch(() => false)) {
      await cont.first().click().catch(() => {})
      await page.waitForTimeout(2000)
    }
  }
}

// applyDateRange widens the transaction window using RBC's date-range filter, when a history window was
// requested (BK_IMPORT_HISTORY_FROM/TO, "MMM D, YYYY"). Open the Filter panel (the date fields are
// hidden until then), type the from and to dates into #rbc-dp-0 / #rbc-dp-1, apply, then page through
// "Show More" until it is gone so the whole range loads before reading. A no-op when no window is set
// or the account has no such filter.
async function applyDateRange(page) {
  const from = process.env.BK_IMPORT_HISTORY_FROM || ''
  const to = process.env.BK_IMPORT_HISTORY_TO || ''
  if (!from) return

  if (await isLegacy(page)) return applyDateRangeLegacy(page, from, to)

  // A card lists Pending and Posted transactions in separate tables; open the Posted table's search
  // first, which is where its Filter lives. A chequing has no such table, so this is skipped there.
  const postedSearch = page.getByLabel(/posted transactions/i).getByRole('button', { name: /^\s*search\s*$/i })
  if ((await postedSearch.count()) > 0) {
    await postedSearch.first().click().catch(() => {})
    await page.waitForTimeout(1000)
  }

  const filter = page.getByRole('button', { name: /^\s*filter\s*$/i })
  if ((await filter.count()) === 0) return // this account has no filter UI
  await filter.first().click().catch(() => {})
  await page.waitForTimeout(1000)

  // The from/to date fields are keyed by id on some accounts (#rbc-dp-0/1) and only by an accessible
  // label on others (a card's "Date Range From"/"Date Range To"); match either.
  const fromInput = page.locator('#rbc-dp-0').or(page.getByRole('textbox', { name: /date range from/i })).first()
  if ((await fromInput.count()) === 0) return
  await fromInput.click().catch(() => {})
  await fromInput.fill(from).catch(() => {})
  if (to) {
    const toInput = page.locator('#rbc-dp-1').or(page.getByRole('textbox', { name: /date range to/i })).first()
    await toInput.click().catch(() => {})
    await toInput.fill(to).catch(() => {})
  }
  await page.getByRole('button', { name: /^\s*apply\s*$/i }).first().click().catch(() => {})
  await page.waitForTimeout(2500)

  // Load the entire range: RBC shows a page at a time behind a "Show More" control.
  for (let i = 0; i < 60; i++) {
    const more = page.getByRole('button', { name: /show more/i })
    if ((await more.count()) === 0 || !(await more.first().isVisible().catch(() => false))) break
    await more.first().click().catch(() => {})
    await page.waitForTimeout(1500)
  }
}

run({
  // Are we signed out rather than looking at an account? True when either the landing page's sign-in
  // button or the sign-in form's username field is present, so the harness signs in again.
  async isLoginWall(page) {
    return (await signInField(page).count()) > 0 || (await signInButton(page).count()) > 0
  },

  // Sign in. With a username and password (resolved from the secret store) this types RBC's two-step
  // form -- client card / username, Next, password, Sign In -- and completes unattended, headless.
  // With none, a person is signing in headed and this just waits for them; either way the password
  // and any 2FA are never seen or stored, only the resulting session is. RBC's 2-step-verification
  // page (a security question, once 2-step is set to questions rather than texts) is the remaining
  // selector to pin; until then, a run that hits it fails loudly in waitSignedIn rather than hanging.
  async signIn(page, creds) {
    // The landing page hides the form behind a button; open it if that is where we are.
    if ((await signInButton(page).count()) > 0) {
      await signInButton(page).first().click()
      await page.waitForLoadState('networkidle').catch(() => {})
    }
    if (creds && creds.username && creds.password) {
      const user = signInField(page)
      await user.waitFor({ timeout: 2500 })
      await user.fill(creds.username)
      await page.getByRole('button', { name: /^\s*next\s*$/i }).click()
      const password = page.getByRole('textbox', { name: /password/i })
      await password.waitFor({ timeout: 2500 })
      await password.fill(creds.password)
      // Anchored so it does not also match the "Sign in to RBC Online" landing button.
      await page.getByRole('button', { name: /^\s*sign in\s*$/i }).click()
      // RBC's 2-step verification: pick security questions, answer from the stored answers, confirm.
      await clearTwoStep(page, creds)
      // Lenient: let the account navigation surface a genuine problem (with a snapshot).
      await waitSignedIn(page, 2500, false)
      return
    }
    // A person may take a minute to enter a password and clear a texted code, so wait generously.
    await waitSignedIn(page, 5 * 60 * 1000, true)
  },

  // Read the account's transactions into { date, description, amount }. RBC renders the list twice --
  // a desktop grid and a responsive/print variant -- so select only the desktop transaction rows
  // (`rbc-transaction-list-transaction-new`); selecting across both would double every line, and the
  // day-header rows are not transactions. Each row is either a withdrawal (already signed negative)
  // or a deposit (positive); the description spans a few divs (type, then merchant/counterparty).
  // The in-page step returns raw strings; date and amount are normalized here in Node.
  async readRows(page) {
    // Widen the window to the requested history before reading (RBC's presets stop at 30 days).
    await applyDateRange(page)
    // A line of credit is served by the legacy site, whose grid is read differently.
    if (await isLegacy(page)) return readRowsLegacy(page)
    // RBC is an Angular app; the account page's grid renders after navigation. Wait briefly for a row
    // so a still-loading page is not misread as empty, but do not fail here -- a genuinely empty
    // account (or a wrong page) falls through to zero rows, with the snapshot left for diagnosis.
    await page.locator('tr.rbc-transaction-list-transaction-new').first().waitFor({ timeout: 2500 }).catch(() => {})
    const raw = await page.locator('tr.rbc-transaction-list-transaction-new').evaluateAll(trs => trs.map(tr => {
      const text = sel => (tr.querySelector(sel)?.textContent || '').trim()
      const descCell = tr.querySelector('.rbc-transaction-list-desc')
      const description = descCell
        ? Array.from(descCell.querySelectorAll('div')).map(d => d.textContent.trim()).filter(Boolean).join(' ')
        : ''
      return {
        // The chequing tags its date cell headers="date"; a card uses headers="cc-date-large" but both
        // carry the date-column-padding class, so match either.
        date: text('td.date-column-padding, td[headers~="date"]'),
        description,
        withdrawal: text('.rbc-transaction-list-withdraw span'),
        deposit: text('.rbc-transaction-list-deposit span'),
      }
    }))
    // A card is a liability: RBC signs its amounts by their effect on the owing balance (a charge is
    // positive, a payment negative), the reverse of a chequing's Withdrawals/Deposits. The books hold a
    // liability negative, so flip the sign for a liability account.
    const flip = (process.env.BK_IMPORT_ACCOUNT || '').startsWith('Liabilities')
    return raw.map(r => {
      let amount = (r.withdrawal || r.deposit).replace(/[$,\s]/g, '')
      if (flip && amount) amount = amount.startsWith('-') ? amount.slice(1) : '-' + amount
      return { date: toISO(r.date), description: r.description, amount }
    })
  },

  // Read the account's current balance for reconciliation, as a plain signed decimal in the books'
  // sign. RBC carries a running balance on the newest transaction of each day, so the first non-empty
  // Balance cell is the current posted balance. It is shown positive for an asset (a Current Account);
  // a card or loan account, whose balance is owing, would need negating -- pin that when a card is
  // wired. Return null to record no balance.
  async readBalance(page) {
    if (await isLegacy(page)) return readBalanceLegacy(page)
    // A card carries no per-row running balance; its balance owing is a summary tile ($0.00 when paid
    // off). Read that when present so the card records a real balance rather than none.
    const summary = page.locator('.current-balance [data-role="product-summary-details-booked-balance"]')
    if ((await summary.count()) > 0) {
      const bal = (await summary.first().textContent()).replace(/[$,\s]/g, '')
      if (bal) return bal
    }
    const cells = page.locator('tr.rbc-transaction-list-transaction-new .rbc-transaction-list-balance')
    const n = await cells.count()
    for (let i = 0; i < n; i++) {
      const bal = (await cells.nth(i).textContent()).replace(/[$,\s]/g, '')
      if (bal) return bal
    }
    return null
  },
}).catch(e => { process.stderr.write('rbc: ' + e.message + '\n'); process.exit(1) })
