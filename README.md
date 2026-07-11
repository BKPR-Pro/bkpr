# bookkeeper

Turns bank and card statements into a set of books.

The goal is Mint's touch with a real ledger's resolution: you set it up, it runs, and the only
recurring work is re-categorizing a couple of things every once in a while.

## Every line posts, and nothing is guessed

**Nothing blocks on a question.** A gate would be exactly the friction this tool exists to remove.
So every statement line becomes an entry, always.

Where the rules run out of knowledge, the account path stops:

```text
Expenses:Real Estate:Utilities:Water:123 Example Street   the rules know all of it
Expenses:Real Estate:Materials:Uncategorized              they know the kind, not the property
Uncategorized                                             they do not even know the kind
```

A hardware store charge could serve any property. That fact lives on the receipt, not in the
description, and no amount of matching recovers it. So the rule says `Materials:Uncategorized`,
which is **true**, rather than picking the likeliest unit and marking it as a guess.

### Truncate, do not guess

A wrong leaf (Unit 1 instead of Unit 2) costs insight only. An absent leaf costs exactly the same
insight and tells no lie. So there is never a reason to guess one.

A wrong **kind** is different. An expense booked as income breaks the books and does not
self-correct, so an unrecognized line posts to a top-level `Uncategorized` account rather than
being guessed into `Expenses`. Every total above the truncation stays honest: `Expenses` and
`Expenses:Real Estate:Materials` are both exactly right even while the unit is unknown.

The account path is the only marker there is. No tags, no flags, no review queue:

```sh
ledger -f books.ledger bal Uncategorized
```

That finds every unknown at every depth, because ledger matches on the whole account name.

### Fix the rule, not the line

`Uncategorized` is not a to-do list of corrections. It is a pointer to **a rule you have not
written yet**.

The books are a fold over the log, so writing that rule reclassifies the entire history at once.
Twelve months of hardware charges are one rule, not twelve corrections. You only ever correct a
single line when the fact is genuinely about that one charge and cannot generalize, which is what
a receipt in your truck is.

### Rules are defaults; corrections are about one line

- **Rules** produce defaults, keyed by pattern. Always followed. Never learned automatically.
- **Corrections** are facts about one transaction, keyed by its fingerprint. They never generalize,
  so correcting a single hardware charge does not silently re-pin every future one.

Rules are data, not code. A different set of books means a different rule set, not a different
build.

### Entries are postings, not a category

One charge can serve two properties. An entry therefore holds a list of postings rather than a
single category, and a split is just more than one of them.

Only the categorized side is stored. The posting against the account the statement came from is
elided and inferred by the ledger, which is why an entry cannot be unbalanced: the postings must
account for the whole line, and nothing else can name the source account.

## The log is the book of record

Double-entry bookkeeping is the oldest event-sourced system in continuous use. A ledger file is
already an append-only log of immutable, time-ordered facts, and `ledger bal` is already a fold
over it. So bookkeeper is built the same way, and the vocabulary is borrowed rather than invented.

The whole design follows from one rule:

> **The log holds only what cannot be recomputed. Everything else is a fold.**

| input | recomputable? | so it lives |
| --- | --- | --- |
| a statement line | no, it came from outside | in the log |
| a rule | no, it is authored out of what you know | in the log |
| a model's answer | no, it is nondeterministic and it cost money | in the log |
| your judgment | no, the receipt is in your truck | in the log |
| a categorization | yes, it is `rules(transaction)` | derived on read |
| a transfer pairing | yes, from the movement key | derived on read |
| the ledger file | yes, from the log | a generated artifact |

The last row is the one that changes how you work. **The ledger file is read-only output.** You
correct the books by recording a fact and regenerating, not by editing the artifact. That is what
buys the property everything else rests on: the books are a pure function of the log, so
regenerating them is boring, and a changed diff line means something actually changed.

### Events

Events are immutable, past-tense facts.

| event | what it means |
| --- | --- |
| `transaction.imported` | a statement line was read in. Once per fingerprint, ever |
| `transaction.categorized` | a person or a model asserted the postings for this line |
| `transaction.matched` | force or break a transfer pairing the automatic fold got wrong |
| `transaction.discarded` | that line was garbage; keep it out of the books |
| `transaction.exported` | this deposit was written to a connector (e.g. rent booked against a lease) |
| `accrual.recognized` | value was earned or incurred before its cash: an invoice or a bill. Once per fingerprint |
| `accrual.settled` | the bank line that paid an accrual, so its cash clears the receivable rather than re-booking income |
| `accrual.voided` | that accrual should not have been raised; keep it out of the books |
| `rule.added` | a pattern should be handled |
| `rule.changed` | a rule's answer is wrong |
| `rule.removed` | a rule should stop firing |
| `rule.moved` | two rules fire in the wrong order |
| `connector.added` / `connector.removed` | a live connector, registered by name |

An event's name says **what happened**. Its `actor` says **who**. Reading `actor` should never be
necessary to know what kind of fact you are looking at, which is why there is no
`categorized_by_model`. A person answering an `Uncategorized` line and a model answering one are
doing the same thing, and the log should say so.

Nothing is ever edited. Asserting a line's postings twice appends two facts, the later fold wins,
and how a categorization came to be survives next to what it currently is.

### Rules are facts too

Editing a rule retroactively rewrites the books. Change `acme hardware` from `Uncategorized` to
`Unit 1` and every past line matching it reclassifies on the next regeneration. That is the most
consequential operation in the system, so `rule.changed` sits in the log, in order, next to the
transactions it rewrote, carrying a `why`.

A rule is authored out of what you know about a merchant. It is exactly as unrecomputable as a
correction, and keeping rules in a file elsewhere would only mean the books were a function of two
histories joined by a commit nobody wrote down.

Per-rule events rather than snapshots of the set, because a snapshot names the effect (the set is
different now) instead of the intent, and because the signal that tells a good rule from a bad one
needs a rule to have an identity that survives being edited. Order is semantic, so `rule.moved`
anchors on the rule it now precedes; if that anchor was later removed, the fold appends.

A rule's identity is its **match pattern**. Changing what a rule answers keeps that identity, so a
merchant's history stays together. Changing the pattern is a different rule, and rightly loses that
history, because it now fires on a different set of lines. Two rules therefore cannot share a
pattern.

Each edit is its own command and its own event: `rules set`, `rm`, `mv`. That is the intent
recorded directly, with no file to diff. `rules list` prints what the log currently folds to.

### Importing a file is a one-time door

Reading a file in is not part of any fold. `transaction.imported` stores the transaction already
**normalized**, so re-reading the log never re-parses a CSV, and how you read a file (which columns,
which date format) is a one-time input rather than a fact the books depend on.

This is why the details are supplied inline at the import (`-account`, `-currency`, the columns)
rather than registered: a file is read once. It also could have stored the raw row and normalized
on read, which would make the reader a fold input and let re-reading with different flags repair
history. It does not, because the fingerprint is built from the normalized fields: re-normalizing
would move every fingerprint and silently orphan every correction keyed to one.

So a bad line is not fixed by re-parsing. You import again with the right flags (the corrected line
lands under a new fingerprint) and `discard` the garbage one.

A connector is the other kind of input, and it is bidirectional in principle: `export` writes to it
today, and importing from it by name is the same `import` verb, built later. It is registered once
(`connectors add`) and logged, unlike a file's inline flags, because it persists; its bearer token is
never stored, the registration keeping the name of the environment variable that holds it, read
when the connector is used, so the log stays committable. Rent goes out before it comes in only
because the bank statement is where the money is read from first, not because importing a rent roll
later is ruled out.

### Nothing has to be acknowledged

There is no `confirmed` event, and there is nothing to clear. Both would build an inbox: a list
that only empties if you work it. The queue is the friction this tool exists to remove.

**Silence is the confirmation.** If you did not correct a line, the rule stood. The signal that
tells a good rule from a bad one is corrections divided by how often the rule fired, and both of
those are already folds over the log. Six corrections out of six firings means go fix the rule. Six
out of six hundred means it is fine.

### Commands and folds

A command captures one intent, guards a precondition, and emits one event. `Import`, `AddRule`,
`ChangeRule`, `RemoveRule`, `MoveRule`, `Categorize`, `Match`, `Discard`. Nothing else writes.

`TrackOnce` appends a fact that can only be true once and reports `ErrAlreadyTracked` otherwise,
which is how re-importing an overlapping statement becomes a no-op rather than a second rent
payment. `Track` appends a fact that may recur. Which facts are once-only is the caller's business,
so storage carries no domain knowledge: it holds the set of once-only keys it has seen and refuses
a repeat.

Rules do not interleave with transactions in one chronological fold. If they did, a rule added in
June would not reach a transaction imported in March, and fixing a rule would not fix history,
which is the whole point of regenerating. So it is two folds over one log: rule events fold to the
current rule set, that set categorizes every transaction, and assertions keyed to a fingerprint
override the result.

### Storage: one JSON object per line, in git

The log is a `log.jsonl` file: one event per line, append-only, committed to git. That is the whole
store. It is small (a set of books reaches a few hundred lines a year), so folding the file on
every run costs nothing, and being text is what makes the rest true:

- **Git is the backup and the audit trail.** A signed history over an append-only log is a
  tamper-evident book of record. `bk log` is `cat`; restore is `git checkout`.
- **The append-only invariant is git-checkable.** Every write appends, so `git diff log.jsonl`
  should always be a pure addition. A diff that changes or deletes an existing line means something
  rewrote history, and you would see it in review. Git becomes a continuous check on the one
  property the whole design rests on.

Durability is bought without a database. Each append is flushed and `fsync`ed before the caller is
told the fact is recorded, so a fact survives a crash. Because the log only ever grows, a crash can
only tear the *last* line; on open, a final line that will not parse is dropped and the file
healed, while a bad line anywhere earlier is refused as corruption rather than guessed at. A single
writer is held by an advisory lock, which is what lets the once-only check trust its in-memory set.

The one guarantee this gives up against a database is that uniqueness is enforced by the writer
rather than by the storage engine. The lock closes that: no second process can append behind the
first's back. It is a trade taken deliberately, to keep the book of record readable, diffable, and
dependency-free (the binary is stdlib only). Storage sits behind a small interface, and the tests
fold over an in-memory adapter.

## Three tiers, and a model that never writes

1. **Rules.** Deterministic, free, reproducible. Handles almost everything.
2. **A model.** An external agent that drives the whole tool through its commands, the same surface
   a person uses: import, rules, categorize, match, export, review. Within this pipeline its job is
   the lines the rules left `Uncategorized` — it proposes postings, and code writes. bookkeeper
   never calls a model itself.
3. **You.** Never blocking. `ledger bal Uncategorized` is the whole review surface: whatever is
   left is a rule you have not written or a line to correct, and nothing stops the books being
   complete in the meantime.

A model may label. Code does the writing, the deduplication, and the arithmetic. Quarantining the
nondeterminism is what keeps the books regenerable, and it is why a model's answer is written to
the log: it cannot be recomputed, so it must be remembered.

## Inputs and the artifact

The core understands only normalized transactions. An **adapter** brings lines in; only the adapter
knows about the outside world, and CSV is the default transport because every bank exports it and
it needs no credentials.

The only output is the ledger file. Bookkeeper turns statements into a committed double-entry
ledger and stops there; the artifact is the product. It does not drive other systems, which keeps
it standalone and keeps nothing about any particular app wired into the core.

(The event carries a stable `Event.Key()`, so if bookkeeper ever did drive an external system, a
destination that must not act twice could send that key as an `Idempotency-Key` and a retry would
rebuild the identical key from the identical stored event. That is what makes a retry safe when the
response is the part that gets lost. It is designed for and not built.)

## Usage

`bk` is the command-line wrapper; `go build -o bk ./cli` builds it, or run it from a checkout with
`go run ./cli`.

`init` creates a set of books in the current directory, marked by a `.bookkeeper` directory the way
a git repository is marked by `.git`. Every other command finds it by walking up, so you can run
them from anywhere inside your project.

```sh
bk init
# Initialized a book of record in /your/project/.bookkeeper
```

`import` records what a file said, and it is safe to run twice. A file is a one-time input, so its
details are supplied inline. A CSV does not name its own account, currency, or columns, so you give
them; a ledger file names all of that itself and takes no options.

```sh
bk import statements/march.csv -account "Assets:Bank:Chequing" -currency CAD -amount Amount
# 9 lines read: 9 imported, 0 already in the log

bk import statements/march.csv -account "Assets:Bank:Chequing" -currency CAD -amount Amount
# 9 lines read: 0 imported, 9 already in the log

# a debit/credit pair instead of one signed column:
bk import visa.csv -account "Liabilities:Card:Visa" -currency CAD -debit Charge -credit Payment -date Posted
```

A connector is bidirectional in principle; `export` is the direction built first. The rent app is
the first connector: `export` records rent the books already booked back to it, so its paid/unpaid
state stays current. The token is kept in an environment variable, never in the books.

Which lease a deposit belongs to is not in the bank memo, so the tenant's rule carries it as
metadata: `-meta rentapp.lease=<id>` rides onto the categorized deposit, and `export` records that
deposit against that lease, keyed by the deposit's fingerprint so a repeat is a no-op. Without
`-confirm` it is a dry run.

```sh
export BK_RENT_TOKEN=...   # the rent app's bearer token
bk connectors add rent -kind rentapp -url https://rent.stcroixproperties.ca \
  -token-env BK_RENT_TOKEN -account "Assets:Bank:Chequing" -currency CAD
bk rules set -match "hyungjin" -category "Income:Real Estate:Rent:22 Lisgar Street" \
  -meta rentapp.lease=31
bk export rent            # dry run: what it would record
bk export rent -confirm   # records each rent deposit against its lease
```

`rules set` authors a rule: it adds a pattern not yet known, or changes the one already matching it.
Each is one event. Order decides which of two matching rules wins, so a new rule lands at the end
unless `-before` places it ahead of another. `rules rm` and `mv` drop and reorder.

```sh
bk rules set -match "shell|petro" -category "Expenses:Travel:Fuel" -payee "Fuel Stop"
bk rules set -match "city water"  -category "Expenses:Utilities:Water" -before "water"
bk rules list
```

`books` folds the log into a table, or regenerates the ledger artifact in the store:

```sh
bk books                 # a table, to read
bk books -format ledger  # regenerates .bookkeeper/books.ledger
```

```text
DATE        PAYEE                AMOUNT   POSTS TO
2026-03-01  Fuel Stop            -62.40   Expenses:Consulting:Travel:Fuel
2026-03-02  Acme Hardware        -84.20   Expenses:Real Estate:Materials:Uncategorized
2026-03-05  J. Smith             1600.00  Income:Real Estate:Rent:123 Example Street
2026-03-12  UNKNOWN MERCHANT 88  -39.99   Uncategorized

9 lines posted, 2 of them uncategorized
```

### Cash and accrual are one log read two ways

Cash-basis books record money when it moves; accrual-basis books record value when it is earned or
incurred, before the cash follows. bookkeeper does not choose between them and does not store a mode.
The basis is a **read-time lens** over the one log, chosen with `-basis`:

```sh
bk books -basis cash      # only money that moved. The default, and every earlier example
bk books -basis accrual   # also books the invoices and bills that have not been paid yet
```

Cash basis is what every example above already is: it ignores accruals entirely, so it is exactly
the books bookkeeper was born on. Accrual basis adds the value you have recognized but not yet been
paid — an invoice raised, a bill received — each as its own line.

An **invoice** is money owed to you; a **bill** is money you owe. They are the one kind of fact a
bank statement cannot supply, because the money has not moved, so they are recorded rather than
folded from a line:

```sh
bk accrue invoice -party "J. Smith" -amount 1600.00 -category "Income:Consulting" -date 2026-03-01
bk accrue bill    -party "Power Co"  -amount 142.03  -category "Expenses:Utilities:Power" -date 2026-03-02
```

The magnitude is positive and the kind decides the signs: an invoice debits a receivable and credits
income, a bill debits an expense and credits a payable. Where it parks defaults to `Assets:Receivable`
or `Liabilities:Payable`; `-account` overrides. On the accrual basis the invoice above books on the
day it was earned:

```text
2026/03/01  * J. Smith
  Income:Consulting  -1600.00 CAD
  Assets:Receivable
```

When the deposit that pays it lands in the bank, `settle` records which line paid which accrual, so
the cash clears the receivable instead of booking the income a second time (that income was booked
when the invoice was recognized):

```sh
bk import march.csv -account "Assets:Bank:Chequing" -currency CAD -amount Amount
bk settle -accrual 9617607456a06619 -tx 6afa3719db1eb739-1
```

```text
2026/03/20  * J. Smith
  Assets:Receivable  -1600.00 CAD
  Assets:Bank:Chequing
```

The receivable now nets to zero (debited when the invoice was raised, credited when the cash cleared
it), and `Income:Consulting` is booked exactly once.

The pairing is **recorded, not guessed**. An internal transfer pairs automatically because each
sighting names the other's account with certainty; a deposit's memo does not reliably name which
invoice it clears, so settling is an asserted fact rather than a fold, in keeping with *say only what
is known*. A wrong accrual is dropped with `void`, which supersedes it the way `discard` supersedes a
bad import; `bk accruals` lists the open ones and what settled each.

Because the basis is a lens and accruals are additive facts, you can **start on cash and turn on
accrual later** with no migration: recognize invoices from whatever day you begin, and every period
before that reads identically under both bases, because there is nothing there to accrue. The one
honest caveat is the seam — a period that straddles the switch mixes the two — and the log dates
exactly when the first `accrual.recognized` appears, so the switch documents itself.

### Fixing a rule fixes history

Learn that every hardware receipt was Unit 1, and say so once. `set` changes only the fields you
name, so the payee is left as it was:

```sh
bk rules set -match "acme hardware" -category "Expenses:...:Unit 1" -why "the receipts were all Unit 1"
bk books -format ledger
```

The books change by exactly one line per affected transaction, and so does the log, by exactly one
appended event:

```diff
# books.ledger
-  Expenses:Real Estate:Materials:Uncategorized  84.20 CAD
+  Expenses:Real Estate:Materials:45 Sample Avenue:Unit 1  84.20 CAD
# log.jsonl
+{"collection":"rule","record_id":"acme hardware","action":"changed", ...}
```

Twelve months of hardware charges would have moved together, from one edit. That is what the log
buys, and committing both files is how the change reviews.

### Correcting a single line

Some attributions are not a rule. A hardware receipt in your truck says Unit 1, and no pattern over
the description could have known that. So `categorize` asserts the answer for that one line, keyed
by its fingerprint, and it wins over whatever the rule said:

```sh
bk categorize -tx 0d76f1f1... -category "Expenses:...:Unit 1" -payee "Acme" -why "receipt was Unit 1"
```

One charge can serve two properties, so an assertion can be a split, and it is only accepted if the
postings still account for the whole line:

```sh
bk categorize -tx 0d76f1f1... \
  -post "Expenses:Materials:Unit 1=40.00" \
  -post "Expenses:Materials:Unit 2=44.20"
```

An assertion is a fact about one transaction. It never generalizes into a rule, so correcting one
hardware charge does not re-pin every future one, and it survives a later rule change: fixing the
rule moves every line except the ones you have already spoken for. Assert twice and the later fact
wins, with both kept in the log.

### Undoing a bad import

Import stores each line already normalized, so a wrong flag (a flipped sign, the wrong date column)
imports garbage that re-importing cannot repair on its own: the fingerprints are already in the log,
and a re-import is a no-op on them. `discard` is the way out.

```sh
bk discard -tx 33247b87... -why "imported to the wrong account"
```

It does not delete the imported event. It appends a fact that supersedes it, and the fold drops the
line, so the mistake and its correction both stay in the log and the git diff is still a pure
append. Re-importing the same statement will not bring the line back, because the imported fact is
still there and the import stays a no-op.

The repair flow the append-only log makes possible: re-import with the right flags (the corrected
line lands under a new fingerprint, since the amount or date changed), then discard the garbage one.

### Transfers between your own accounts

Move $500 from chequing to savings and it appears in both statements: once leaving chequing, once
arriving in savings. Each sighting, categorized to the other account, is a complete balanced entry
on its own, so booking both would move the money out and then back and net it to zero, losing the
movement.

So the second sighting is suppressed. Two sightings pair when each names the other's account, their
amounts are equal and opposite, and their dates fall within a few days; the earlier one is kept and
books the transfer, the later is dropped. Import a statement that has not been paired yet and the
transfer books normally on its own; import its other half later and the duplicate is recognised and
dropped.

This is a pure fold, so no event records a pairing: it is recomputed from the lines and their
categorization every time the books are built. The mutual-naming check is what keeps it honest. A
$500 expense and a coincidental $500 deposit are not a transfer, because neither names the other's
account, so both are booked. Suppression only ever happens when two accounts you own each point at
the other.

## Books

`-format ledger` regenerates `.bookkeeper/books.ledger`, the plain-text double-entry artifact.

```sh
bk books -format ledger           # into the store
bk books -format ledger -stdout   # to stdout, to pipe
```

Every entry is **cleared** (`*`), because every line came off a bank statement and so has cleared
the bank. Pending (`!`) means the bank has not reported a transaction yet, which is a real state
and not one bookkeeper can produce from a statement. Nothing here borrows those flags to mean
anything about categorization.

The output is a real ledger file, so the usual tools work:

```sh
ledger -f books.ledger bal                # balances, which sum to zero
ledger -f books.ledger bal Uncategorized  # everything the rules could not name, at any depth
ledger -f books.ledger print --uncleared  # empty, and correctly so
```

## Configuration

There are no config files. Rules are recorded straight into the log by command, and the log is what
`books` reads. Import details are given inline, since a file is read once (see Usage).

A **rule** matches a description and supplies a payee, an account to post to, or both. Rules are
ordered, and for each field the first rule that supplies it wins; `match` is the rule's identity, so
no two may share one.

```sh
bk rules set -match "acme hardware" -payee "Acme Hardware" -category "Expenses:Real Estate:Materials:Uncategorized"
```

Categories are free-form account paths, so you can go as deep as your books do, down to the
property and unit, and stop at `Uncategorized` wherever you cannot. No catch-all rule is needed: a
statement line already knows which account it came from, and a line no rule matches posts to
`Uncategorized`.

## Design rules

- **Deterministic where money is recorded.** The books are a pure function of the log. A model
  proposes; code writes.
- **Idempotent end to end.** Every line carries a stable fingerprint, so a re-import is always
  safe. Two genuinely identical charges on one day stay two charges.
- **An amount is an exact quantity of a commodity.** Held as integer minor units, so no float ever
  touches the books, and the commodity may be a currency or a share. Ledger-cli's model, which is
  why the books can hold anything ledger can.
- **Connectors know the outside world; the core does not.**
- **Say only what is known.** Truncate an account path rather than guess a leaf, and never guess a
  kind.
- **Nothing blocks.** Every line posts, an unknown one truncates to `Uncategorized` rather than
  stopping the run, and every correction is cheap because regenerating is cheap.

## Status

Everything lives in a `.bookkeeper` directory found by walking up, the way git finds `.git`. The
log is `log.jsonl`, committed, one event per line; the ledger is its committed artifact.
Re-importing an overlapping statement is a proven no-op, two renders of the same log are
byte-identical, changing a rule reclassifies history in one appended event, correcting a single
line overrides the rule for that line only and survives later rule changes, and a wrong working
directory is refused rather than turned into a new empty book of record.

Amounts are an integer `Amount` (quantity, scale, commodity), serialized in the log as a
ledger-style string like `84.20 CAD` or `10 AAPL`. No float touches money at any boundary; the CSV
reader's old float is gone. An entry may hold two commodities: a share posting carries the total
cash it cost as a price (the ledger `@@` form), and the entry balances by resolving that price back
to the line's own commodity. A cross-commodity posting with no price, or a price in a third
commodity, still cannot be summed and so is refused.

A brokerage account holds shares against cash. A purchase is a priced posting
(`-post "Assets:Brokerage:AAPL=10 AAPL @@ 1000.00 USD"`), and a sale names the shares it disposed of
and where the gain lands (`-sell "Assets:Brokerage:AAPL=10 AAPL" -gain "Income:Capital Gains"`). The
cost base the shares leave at is folded from the account's purchases under the average cost base
(ACB), so the gain is derived rather than stored: correct an earlier purchase's base and every later
sale's gain moves with it. A sale that disposes of more than the account holds is refused when it is
asserted. The base is a total, never a divided per-unit price, so a full disposal returns the exact
cost and a partial one rounds to the cent without leaking.

Internal transfers seen in both accounts' statements are recognised and booked once, as a
deterministic fold over the lines and their categorization, so the money is not double-counted. A
bad line is undone with `discard`, which supersedes the imported line without deleting it.

Cash and accrual are the same log read through two lenses, chosen with `bk books -basis`. Cash is the
default and every statement example is already it. Accrual also books the value recognized before its
cash: `accrue invoice` raises a receivable, `accrue bill` raises a payable, and `settle` records the
bank line that paid one so the cash clears the parked account rather than booking income or expense
twice. The basis is a read-time choice, never stored, so a book can start on cash and turn on accrual
later with no migration; a wrong accrual is dropped with `void`. Recognizing the same accrual twice
is a no-op, keyed by a fingerprint of its content, exactly as re-importing a statement is.

Inputs are files (CSV, and the ledger form it writes), read once inline with `import`, and connectors. A
connector is bidirectional in principle; `export` is the direction built first, because the bank
statement is where the money is read from. The rent app is the first connector: `export` records
rent the books already booked back to it so its paid/unpaid state stays current, with its token kept
in the environment rather than the books. The lease a deposit belongs to rides on the tenant's rule
as metadata (`rentapp.lease`), and the export is keyed by the deposit's fingerprint, so re-running
it records nothing twice. Importing a rent roll later, as context rather than as a second copy of
the money, is a natural next step and is not precluded.

The surface an external model drives is in place: `review` prints the open decisions as JSON (the
`Uncategorized` lines with their fingerprints), and `categorize`, `rules set`, and `discard` take an
`-actor`, so a model proposes through the same path a person uses and the log records who answered.
bookkeeper never calls a model itself.

Next: the cost basis follow-ons. The policy is ACB and pluggable at the seam; making it a logged,
per-account setting (so a US account can run FIFO in the same book) and reading the share quantity
straight off a brokerage statement (so a trade need not be typed) are the slices from here. Parked
until asked: importing from a connector (the rent roll as context), and out-of-tree connectors as
installable plugins.

## Layout

Ports and adapters: a domain core, with the outside world reached only through adapters.

```text
lib/model/            the normalized shapes: Amount, Transaction, Posting, Entry
lib/books/            the commands and folds: Import, AddRule, Categorize, Recognize, Settle, Ledger, ...
lib/rules/            the deterministic categorization engine
lib/eventlog/         the append-only log and its storage
lib/store/            locating and opening a .bookkeeper book of record
lib/adapters/source/  reads statements from the outside world (CSV and ledger files)
lib/adapters/ledger/  renders the books as a plain-text double-entry artifact
lib/adapters/rentapp/ the rent app connector: exports recorded rent payments
cli/                  the command-line wrapper (the driving adapter)
```

Every package is importable, so another program can drive the books directly:

```go
s, _ := store.Open(".")
defer s.Close()
books.Import(s.Log, "statement:march", txs)
txs, entries, _ := books.Ledger(s.Log)
```

`bk docs` prints the full command reference, so the CLI is self-documenting.

## Development

```sh
go test ./...
go vet ./...
gofmt -l .
go build ./cli
```
