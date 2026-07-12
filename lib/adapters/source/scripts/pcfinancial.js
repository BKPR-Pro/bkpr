// PC Financial Mastercard online banking. The harness owns the session, the headed sign-in, and the
// JSON output; this file is only PC Financial's specifics -- the three selectors, which can only be
// pinned against the real site. Capture them once with:  npx playwright codegen
// https://www.pcfinancial.ca/  Until then this fails loudly (readRows throws), so an import never
// records nothing. (A manual CSV export also works today with `import <file>.csv`.)
const { run } = require('./harness.js')

run({
  // Are we on the sign-in page (no/expired session)?
  async isLoginWall(page) {
    // TODO(pcfinancial): the sign-in card's unique element, e.g. the username field.
    return await page.locator('#username, input[name="username"]').count() > 0
  },

  // Wait for the person to finish signing in -- password and any 2FA happen in the headed browser and
  // are never stored. Return once an account page is reached.
  async signIn(page) {
    // TODO(pcfinancial): wait for a post-sign-in landmark. The long timeout allows for a texted code.
    await page.waitForURL('**/(accounts|dashboard)**', { timeout: 5 * 60 * 1000 })
  },

  // Read the card's transactions into { date, description, amount, currency? }. A card charge is money
  // out, so it is negative; a payment or refund is positive.
  async readRows(page) {
    // TODO(pcfinancial): the transactions table's row and cell selectors, and its date format.
    return await page.locator('table.transactions tbody tr').evaluateAll(trs => trs.map(tr => {
      const cell = c => (tr.querySelector(c)?.textContent || '').trim()
      return {
        date: cell('.date'),
        description: cell('.description'),
        amount: cell('.amount').replace(/[$,]/g, ''),
      }
    }))
  },
}).catch(e => { process.stderr.write('pcfinancial: ' + e.message + '\n'); process.exit(1) })
