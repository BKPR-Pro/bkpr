// RBC Online Banking. One script serves every RBC account -- personal and business, chequing,
// Mastercard, Visa, term loan, line of credit -- which differ only by BK_IMPORT_URL, and (because
// they are one login) share one session, so signing in for any of them signs in for all. The harness
// owns the session, the headed sign-in, and the JSON output; this file is only RBC's specifics.
//
// The three selectors below are the whole job, and they can only be pinned against the real site.
// Capture them once with:  npx playwright codegen https://www.rbcroyalbank.com/  -- sign in, open an
// account, and read the selectors it records into the three functions. Until then this fails loudly
// (readRows throws), so an import never records nothing.
const { run } = require('./harness.js')

run({
  // Are we on the sign-in page rather than the account? True means the session is missing or expired
  // and the harness will (interactively) sign in again.
  async isLoginWall(page) {
    // TODO(rbc): the sign-in card's unique element, e.g. the username field.
    return await page.locator('#userName, input[name="K1"]').count() > 0
  },

  // Wait for the person to finish signing in -- password and any 2FA happen here, in the headed
  // browser, and are never seen or stored. Return once an account page is reached.
  async signIn(page) {
    // TODO(rbc): wait for a post-sign-in landmark (an accounts-summary URL or element). The long
    // timeout is deliberate: a person may take a minute to clear a texted code.
    await page.waitForURL('**/(summary|accounts)**', { timeout: 5 * 60 * 1000 })
  },

  // Read the account's transactions into { date, description, amount, currency? }. date is YYYY-MM-DD,
  // amount is a plain signed decimal with money out negative; omit currency to take the account's
  // default. Normalize RBC's own date and amount formatting here.
  async readRows(page) {
    // TODO(rbc): the transactions table's row and cell selectors, and RBC's date format. This shape
    // is a placeholder to replace with what codegen records.
    return await page.locator('table.transactions tbody tr').evaluateAll(trs => trs.map(tr => {
      const cell = c => (tr.querySelector(c)?.textContent || '').trim()
      return {
        date: cell('.date'),            // TODO(rbc): reformat to YYYY-MM-DD if the site differs
        description: cell('.description'),
        amount: cell('.amount').replace(/[$,]/g, ''),
      }
    }))
  },
}).catch(e => { process.stderr.write('rbc: ' + e.message + '\n'); process.exit(1) })
