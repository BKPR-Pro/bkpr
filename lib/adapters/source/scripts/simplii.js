// Simplii Financial scraper (STUB). Drives Simplii online banking in a headless browser and prints
// one account's transactions as JSON to stdout, one array of { date, description, amount }:
//
//   [{ "date": "2026-03-01", "description": "INTEREST", "amount": "-12.50" }]
//
// date is YYYY-MM-DD, amount is a plain signed decimal (money out is negative), currency optional.
//
// bookkeeper runs this with the connector's details in the environment:
//   BK_IMPORT_URL       the login or account URL
//   BK_IMPORT_ACCOUNT   the ledger account these lines belong to (for reference)
//   BK_IMPORT_CURRENCY  the account's default currency
//   BK_IMPORT_SECRET    the sign-in secret, from the connector's token-env; never logged
//
// TODO: implement with Playwright. Sketch:
//   const { chromium } = require('playwright')
//   const browser = await chromium.launch()
//   const page = await browser.newPage()
//   await page.goto(process.env.BK_IMPORT_URL)
//   // ...sign in using BK_IMPORT_SECRET, open the account, read the transaction rows...
//   const rows = [/* { date, description, amount } */]
//   process.stdout.write(JSON.stringify(rows))
//   await browser.close()

process.stderr.write('the Simplii Playwright script is a stub; implement scripts/simplii.js\n')
process.exit(1)
