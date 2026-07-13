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
- **The credential is a saved session, not a stored password.** No bank login is ever stored. The
  person signs in themselves, once, in a headed browser (password and 2FA included); bookkeeper keeps
  only the resulting browser session (Playwright `storageState`) and reuses it headless afterward. A
  session expires, which is the whole reason to prefer it to a password. `Connector.TokenEnv` is no
  longer an env var the person sets — it is the *key* under which that session is stored, so several
  connectors that share a login (every RBC account) share one session by sharing a token-env. The
  session lives outside the book of record (machine-local, under the OS config dir), because it is a
  live credential, not committed history.
- **When a session must be refreshed, credentials are referenced, never stored.** For an unattended
  refresh (cron, an agent) the person can register where each login field lives instead of typing it:
  `-cred username=op://Private/RBC/username -cred password=op://…`. The connector stores only the
  *reference*, and `-secret-cmd` (default `op read {}`) is the command that resolves it — so 1Password
  works out of the box and any store with a CLI (macOS Keychain, `pass`) plugs in by naming its
  command. References are safe in the git-committed log; the secret is resolved only at sign-in
  (`resolveCredentials`, `lib/adapters/source/secret.go`) and handed to the browser script on **stdin**
  — never on the command line or in the environment, where another process could read it. With no
  `-cred`, sign-in stays interactive (headed, a person clears 2FA).
- **Sign-in is folded into import, and self-heals.** There is no separate `login` verb. `import`
  tries the saved session headless; if it lands on the sign-in wall, and a person is present (a real
  terminal, detected with isatty), it opens a headed browser to sign in again and saves the fresh
  session. With no person present (a pipe, a redirect, an agent, cron) it fails fast with a "run it
  from a terminal to sign in again" hint (exit `EX_TEMPFAIL`) rather than opening a browser nobody is
  watching — keeping the tool drivable by an agent. `-relogin` forces a fresh sign-in.
- **Automation lives in Node, not Go.** A browser needs Playwright, which Go has no stdlib for, so
  the bank importer runs a per-institution Node script via `os/exec` and reads its JSON output.
  The Go binary stays stdlib-only; the runtime dependency (Node + Playwright) is the scraper's, and
  only when you actually scrape. `NODE_PATH` is set so the script finds the customer's Playwright.
- **A bot-walled bank gets a stealth browser.** Simplii (CIBC's Akamai Bot Manager) blocks the login
  call for any browser Playwright launches, whatever the user agent or flags — it scores the CDP
  automation leak. A profile that sets `stealth: true` takes the harness's `runStealth` path instead:
  real Chrome driven through **patchright** (a patched Playwright that closes that leak) in a
  persistent profile, so the "device" is remembered and the reads look ordinary. It needs `patchright`
  installed alongside Playwright (`npm i -g patchright`). The persistent profile stands in for the
  saved session; the one-time SMS code is entered by the person in the headed window on first sign-in,
  then the trusted device skips it.
- **One harness, thin per-institution scripts.** `scripts/harness.js` owns everything common —
  loading the session, the headed re-sign-in, saving the session, printing JSON. Each institution
  script (`rbc.js`, `simplii.js`, `pcfinancial.js`) is just a profile: the three selectors that
  differ by site. Go materializes the whole `scripts/` dir to a temp dir so a script can
  `require('./harness.js')`.
- **Fail loudly.** A script that cannot read rows exits non-zero, and `source.ReadBank` surfaces the
  message, so a not-yet-pinned import never reads as "imported nothing".

## What is built, and what remains

The Go plumbing and the harness are **done** (`lib/adapters/source`):

- A connector's kind selects the institution: `rbc`, `simplii`, `pcfinancial`. `import <name>` →
  `fetcherFor` → `source.ReadBank`, which runs `scripts/<kind>.js` with Node, passing the connector's
  URL, account, currency, session-file path, and whether a person is present in the environment
  (`BK_IMPORT_*`). The script prints `[{date, description, amount, currency?}]` to stdout; Go
  normalizes it and fingerprints via `source.Identify`, so a bank import and a CSV of one account are
  interchangeable and idempotent. Several accounts at one bank share its script — and, when they share
  a login, its session — differing only by URL and account.
- The session lifecycle (reuse headless, re-sign-in headed when expired, save, and fail fast when no
  one is present) is implemented in `scripts/harness.js` and verified end to end against a local
  fake-bank fixture with a real browser.

**RBC is pinned** (`scripts/rbc.js`) against its real account page -- an RBC business Current Account.
`isLoginWall` keys on the client-card / username field; `signIn` waits for an authenticated landmark
(a transactions grid, an accounts-summary link, or a sign-out control); `readRows` reads the desktop
transaction grid (`rbc-transaction-list-transaction-new`), turning a withdrawal/deposit into a signed
amount and "Jul 11, 2026" into 2026-07-11; `readBalance` reads the newest running balance. It is
verified end to end through a headless browser against a fixture that mirrors RBC's DOM
(`TestRBCScriptReadsAccountPage`, `TestRBCScriptDetectsLoginWall`).

RBC's login is not a plain URL: its accounts have no stable address (the SSO deep link is one-time),
so an account is reached by **signing in and clicking through a menu**. `-url` is the landing page,
whose sign-in form sits behind a "Sign in to RBC Online" button; `isLoginWall` keys on that button or
the username field, `signIn` opens the form and types the two-step credentials, and the harness then
walks the connector's **account path** -- an ordered list of link labels (`-account-path "Go to RBC
Business Banking" -account-path "Current Account"`) -- to the account before `readRows`. Several RBC
accounts thus share one login and one `-url`, differing only by their path. The whole flow (button ->
two-step login -> menu -> account) is verified end to end through a browser
(`TestRBCSignsInAndWalksTheAccountPath`), as is the plain unattended sign-in
(`TestRBCSignsInWithCredentials`). Every run also drops a snapshot of the page it read to
`.bkpr/snapshots/<institution>.html` (self-ignoring), so a run that reads nothing is diagnosable.

What remains for RBC is the **2-step-verification page**: with 2-step set to security questions, the
stored answers are typed on that page, but its selectors are not captured yet, so a run that reaches
it fails loudly (`waitSignedIn`) rather than hanging. Switch RBC's 2-step to security questions,
capture that page, and pin it.

What remains for RBC otherwise is the account types beyond a Current Account: a **card or loan** shows
its balance owing, which `readBalance` must negate to the books' sign, and RBC may label its columns
differently than chequing's Withdrawals/Deposits -- capture one and extend the same way. **Simplii**
is pinned: its chequing and line of credit are read through the stealth path (see above), one reader
for both since Funds in/out already matches the books' sign for the asset and the (negatively shown)
liability alike. **PC Financial** is pinned too, and takes the **stealth path** like its CIBC sibling
Simplii: it also sits behind Akamai, which rejects a headless browser at sign-in (a headless run fails
`net::ERR_HTTP2_PROTOCOL_ERROR`) -- a headed browser passes, which is why a plain `playwright codegen`
works but the harness's headless-first normal path does not, so its profile sets `stealth: true`. Its
sign-in form sits inside a ThreatMetrix iframe (`#tmx_tags_iframe`) with a two-step flow --
username/password, then a "choose a verification method" select whose SMS code the person types in the
window; `signIn` fills across that frame, picks SMS, and waits for the authenticated `<app-auth-header>`
(not merely the form leaving, which is also true of the verification page between), `isLoginWall` keys
on the Username field in any frame (`TestPCFinancialSignsInAcrossTheThreatMetrixFrame`). The account has
no stable deep link (Akamai flags a direct `/transactions` navigation), so its `-url` is the sign-in
page and its account-path clicks "Transactions" in the top nav. Its Mastercard reader keys on the
Angular `sortable-table`: a charge is tagged amount `positive` (it adds to the balance owing) and a
payment `negative`, so the reader flips PC Financial's sense to the books' -- a charge negative, a
payment positive -- and reads the "Current balance" tile as the owing magnitude; it pages back through
the newest-first list until it passes `-from`, then keeps only rows within the window
(`TestPCFinancialReadsTheMastercard`, `TestPCFinancialPagesBackToTheFromDate`).

## What to build next

1. Pin RBC's 2-step-verification (security-question) page so an unattended refresh completes without a
   person, finishing the full auto-login.
2. Extend RBC to a card/loan account: verify the transactions layout and negate `readBalance` for a
   balance owing, so the books agree.
3. PC Financial is pinned (login + Mastercard reader). If a second PC account is added (e.g. a PC Money
   Account), verify its table matches or extend the reader the same way.
4. Or, if a free token flow appears, an `fdx`/aggregator adapter as another `fetcherFor` case,
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
