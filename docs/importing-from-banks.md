# Importing transactions from Canadian banks

A design note behind the bank-import connector stub and the connector-import seam. It records why the
stub exists and what the real fetcher should target, so the choice is not re-litigated from memory.

## The accounts in play

RBC Mastercard, RBC business chequing, RBC business term loan, RBC personal line of credit, RBC
business Visa, Simplii Financial personal line of credit, PC Financial Mastercard.

## What is available today (mid-2026)

- **No official Canadian open-banking API yet.** The Consumer-Driven Banking Act received royal
  assent March 2026; oversight sits with the **Bank of Canada** (moved from FCAC). Draft regulations
  were pre-published June 27, 2026 with consultation to ~Aug 26, 2026. Read-only access rolls out in
  stages **after** final regulations, realistically **2027**; write access (payments) later still.
- **No standard is designated.** The Minister designates a technical-standards body by order; as of
  mid-2026 none is named. **FDX** (Financial Data Exchange) is the likely bet (de-facto North
  American standard, OAuth2/FAPI + a JSON accounts/transactions schema) but has no formal Canadian
  recognition yet.
- **Even after launch,** the framework serves *accredited* third parties; a solo tool may still need
  accreditation or an accredited aggregator, so a free direct personal API may not appear at all.
- **Per institution, right now:**
  - **RBC** has a developer portal and a business "Balance and Transactions" API, but it is
    commercial and advisor-gated. No free, self-serve, personal-account transaction API.
  - **Simplii** has no public API. Aggregators or an unofficial (scraping) library only.
  - **PC Financial Mastercard** has no API; the supported path is **manual CSV export** from the
    card portal.
- **Aggregators (Flinks, Plaid, MX, Mastercard/Finicity)** all cover Canada but are effectively
  paid for a developer building their own tool (free tiers are evaluation-grade only). We are
  avoiding a paid product.

**Conclusion:** for these accounts, the only no-cost paths today are **(a) a headless-browser
screen-scrape** and **(b) CSV export**. CSV already works: `import <file>.csv`. Scraping is the
stubbed direction.

## Architecture decision

- **Keep the fetch boundary abstract.** A connector import is `connectorFetch: books.Connector ->
  []model.Transaction`, dispatched by kind in the CLI (`fetcherFor`). A future Flinks/Plaid/FDX/
  official-API adapter is a new case, not a rewrite. The core never sees the fetch, the same
  containment the rentapp export adapter has.
- **Shape the internal model like FDX.** `model.Transaction` (account, date, signed amount,
  description, raw) already lines up with FDX's transaction shape. Do not build against any Canadian
  "official" schema until the Minister's order designates one.
- **Secrets stay out of the books.** A connector names an environment variable
  (`Connector.TokenEnv`) that holds its secret (a bearer token, or bank login credentials); the
  value is read at fetch time and never written to the log. Storing bank login credentials anywhere
  is a real risk to weigh before the scraper is built; prefer an aggregator/official token flow if a
  free one appears.
- **Automation lives in Node, not Go.** A browser needs Playwright, which Go has no stdlib for, so
  the bank importer runs a per-institution Node script via `os/exec` and reads its JSON output.
  The Go binary stays stdlib-only; the runtime dependency (Node + Playwright) is the scraper's, and
  only when you actually scrape.
- **Fail loudly while stubbed.** A stub script exits non-zero with a message, and `source.ReadBank`
  surfaces it, so a not-built import never reads as "imported nothing".

## What is built, and what is stubbed

The Go plumbing is **done** (`lib/adapters/source`):

- A connector's kind selects the institution: `rbc`, `simplii`, `pcfinancial`. `import <name>` →
  `fetcherFor` → `source.ReadBank`, which runs `scripts/<kind>.js` with Node, passing the connector's
  URL, account, currency, and secret in the environment (`BK_IMPORT_*`; the secret never on the
  command line). The script prints `[{date, description, amount, currency?}]` to stdout; Go
  normalizes it and fingerprints via `source.Identify`, so a bank import and a CSV of one account are
  interchangeable and idempotent. Several accounts at one bank share its script and differ only by
  URL and account.

The **Playwright scripts are stubs** (`lib/adapters/source/scripts/*.js`): each carries the intended
structure and TODO sketch, and exits non-zero so an import fails clearly until the login-and-scrape
steps are written.

## What to build next

1. Fill in a script's Playwright steps: launch a browser, sign in with `BK_IMPORT_SECRET`, open the
   account at `BK_IMPORT_URL`, read the transaction rows, print them as JSON. **Decide the credential
   model first** (see the secrets note) — storing bank logins is the real risk here.
2. Or, if a free token flow appears, an `fdx`/aggregator adapter as another `fetcherFor` case,
   leaving the seam and the rest of the tool unchanged.

## Watch

- Final Consumer-Driven Banking Regulations after the Aug 26, 2026 consultation.
- The Ministerial Order designating the technical-standards body — that is when "design toward FDX"
  becomes confirmable.

## Sources

- [Canada Gazette, Part I — Consumer-Driven Banking Regulations (2026-06-27)](https://gazette.gc.ca/rp-pr/p1/2026/2026-06-27/html/reg3-eng.html)
- [Dept. of Finance — pre-publishing regulations (June 2026)](https://www.canada.ca/en/department-finance/news/2026/06/government-pre-publishes-regulations-to-prevent-fraud-and-facilitate-the-next-phase-of-consumer-driven-banking.html)
- [2024 Fall Economic Statement — Complete Framework for Consumer-Driven Banking](https://www.canada.ca/en/department-finance/programs/financial-sector-policy/open-banking-implementation/2024-fall-economic-statement-canadas-complete-framework-consumer-driven-banking.html)
- [Consumer-Driven Banking Act — Justice Laws](https://laws.justice.gc.ca/eng/acts/C-36.75/page-1.html)
- [Financial Data Exchange (FDX)](https://financialdataexchange.org/)
- [RBC Business Banking APIs](https://www.rbcroyalbank.com/business/api/index.html)
- [PC Financial — download Mastercard transactions (CSV)](https://www.pcfinancial.ca/en/learning-hub/faqs/managing-your-account/can-i-download-my-pc-financial-mastercard-transactions-to-a-file-that-can-be-used-with-third-party-software/)
- [Plaid — Pricing](https://plaid.com/pricing/) · [Flinks — Pricing](https://www.flinks.com/pricing)
- [Ozone API — Consumer-Driven Banking (Canada)](https://ozoneapi.com/the-open-finance-tracker/library/consumer-driven-banking-open-banking-canada/)
