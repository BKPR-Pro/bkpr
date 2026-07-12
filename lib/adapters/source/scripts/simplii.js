// Simplii Financial online banking. The harness owns the session, the headed sign-in, and the JSON
// output; this file is only Simplii's specifics -- the three selectors, which can only be pinned
// against the real site. Capture them once with:  npx playwright codegen https://online.simplii.com/
// Until then this fails loudly (readRows throws), so an import never records nothing.
const { run } = require('./harness.js')

run({
  // Are we on the sign-in page (no/expired session)?
  async isLoginWall(page) {
    // TODO(simplii): the sign-in card's unique element, e.g. the card-number field.
    return await page.locator('#card, input[name="cardNumber"]').count() > 0
  },

  // Wait for the person to finish signing in -- password and any 2FA happen in the headed browser and
  // are never stored. Return once an account page is reached.
  async signIn(page) {
    // TODO(simplii): wait for a post-sign-in landmark. The long timeout allows for a texted code.
    await page.waitForURL('**/(accounts|myaccounts)**', { timeout: 5 * 60 * 1000 })
  },

  // Read the account's transactions into { date, description, amount, currency? }.
  async readRows(page) {
    // TODO(simplii): the transactions table's row and cell selectors, and Simplii's date format.
    return await page.locator('table.transactions tbody tr').evaluateAll(trs => trs.map(tr => {
      const cell = c => (tr.querySelector(c)?.textContent || '').trim()
      return {
        date: cell('.date'),
        description: cell('.description'),
        amount: cell('.amount').replace(/[$,]/g, ''),
      }
    }))
  },
}).catch(e => { process.stderr.write('simplii: ' + e.message + '\n'); process.exit(1) })
