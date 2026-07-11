// PC Financial Mastercard scraper (STUB). Drives the PC Financial card portal in a headless browser
// and prints the card's transactions as JSON to stdout, one array of { date, description, amount }:
//
//   [{ "date": "2026-03-01", "description": "GROCERY", "amount": "-84.20" }]
//
// date is YYYY-MM-DD, amount is a plain signed decimal (a charge is negative), currency optional.
// PC Financial also supports a manual CSV export, which works today with `import <file>.csv`; this
// script is for the hands-off path once its automation is written.
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
//   // ...sign in using BK_IMPORT_SECRET, open the card, read the transaction rows...
//   const rows = [/* { date, description, amount } */]
//   process.stdout.write(JSON.stringify(rows))
//   await browser.close()

process.stderr.write('the PC Financial Playwright script is a stub; implement scripts/pcfinancial.js\n')
process.exit(1)
