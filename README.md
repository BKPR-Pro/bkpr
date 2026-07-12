# bookkeeper

Turns bank and card statements into a set of books.

The goal is Mint's touch with a real ledger's resolution: you set it up, it runs, and the only
recurring work is re-categorizing a couple of things every once in a while.

It is driven entirely through commands, so it does not matter whether a person or an agent operates
it. Every capability has deterministic, idempotent, machine-readable I/O: `books -format json`
reports the whole books, every write is safe to repeat, and the log is the whole state. Whoever runs
it imports statements, writes rules, and answers the `Uncategorized` lines the same way. The only
trace of who is `-actor`, stamped on each authoring command (default `human`; e.g. `-actor claude`
when an agent runs it), so the log records the hand without the tool caring whose it is.

## Quickstart

```sh
go build -o bk ./cli      # bk is the short name used throughout

bk init
bk import statement.csv -account "Assets:Bank:Chequing" -currency CAD -amount Amount
bk books                  # every line and where it posted
bk books -account Uncategorized   # only the lines the rules could not place
bk rules set "shell|petro" -category "Expenses:Travel:Fuel" -payee "Fuel Stop"
bk books                  # the whole history, reclassified by the rule you just wrote
```

`bk help <command>` explains one command; `bk docs` prints the whole reference. One grammar
throughout: the thing a command acts on — a file, a connector, a rule's pattern, a fingerprint —
is its first argument, and flags assert facts about it. Wherever a fingerprint is taken, a unique
prefix is enough, as with a git hash. The rest of this README is the design and the why.

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
| an answer where the rules ran out | no, it is a judgment (the receipt is in your truck) | in the log |
| a categorization | yes, it is `rules(transaction)` | derived on read |
| a transfer pairing | yes, from the movement key | derived on read |
| a capital gain, or a cost base | yes, folded from the purchases before the sale | derived on read |
| the cash-or-accrual basis | yes, the same log re-dated on read | a read-time choice, no event |
| an invoice, or a bill | no, the bank never saw it (the money has not moved) | in the log |
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
| `transaction.categorized` | a person or an agent asserted the postings for this line |
| `transaction.matched` | force or break a transfer pairing the automatic fold got wrong |
| `transaction.voided` | that line should not count; keep it out of the books |
| `transaction.exported` | this deposit was written to a connector (e.g. rent booked against a lease) |
| `invoice.raised` | revenue was earned and billed before its cash: money owed to you. Once per fingerprint |
| `bill.received` | an expense was incurred and billed before its cash: money you owe. Once per fingerprint |
| `invoice.settled` / `bill.settled` | the bank line that paid an accrual, so its cash clears the receivable or payable rather than re-booking the value |
| `invoice.voided` / `bill.voided` | that accrual should not have been raised; keep it out of the books |
| `rule.added` | a pattern should be handled |
| `rule.changed` | a rule's answer is wrong |
| `rule.removed` | a rule should stop firing |
| `rule.moved` | two rules fire in the wrong order |
| `connector.registered` / `connector.removed` | a live connector, registered by name |
| `policy.set` | the cost-basis method for an account, or the book default |
| `account.set` | metadata on an account (a letterhead name, a mailing address) |

An event's name says **what happened**. Its `actor` says **who**. Reading `actor` should never be
necessary to know what kind of fact you are looking at, which is why there is no
`categorized_by_agent`. A person answering an `Uncategorized` line and an agent answering one are
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
lands under a new fingerprint) and `void` the garbage one.

A connector is the other kind of input, and it is bidirectional in principle: `export` writes to it
today, and importing from it by name is the same `import` verb, built later. It is registered once
(`connectors register`) and logged, unlike a file's inline flags, because it persists; its bearer token is
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
`ChangeRule`, `RemoveRule`, `MoveRule`, `Categorize`, `Match`, `VoidTransaction`, `Raise`,
`SettleInvoice`, `VoidInvoice`, `ReceiveBill`, `SettleBill`, `VoidBill`, `RegisterConnector`. Nothing
else writes.

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

## Two tiers: rules, and whoever operates them

1. **Rules.** Deterministic, free, reproducible. Handles almost everything.
2. **Whoever operates it.** A person or an agent, through the same commands — import, rules,
   categorize, match, export, books. The standing job is the lines the rules left `Uncategorized`:
   find them (`books -account Uncategorized`, or `ledger bal Uncategorized`), assert the postings,
   and code writes, with `-actor` recording the hand. Never blocking — whatever is left is a rule
   not yet written or a line to correct, and the books are complete in the meantime.

Whoever operates it may label; code does the writing, the deduplication, and the arithmetic.
Quarantining the nondeterminism is what keeps the books regenerable, and it is why an asserted answer
is written to the log: it cannot be recomputed, so it must be remembered.

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

### Adapters: how the outside world gets in and out

Ports and adapters: the core folds normalized transactions and knows nothing of files, formats, or
endpoints — only an adapter does, and each speaks exactly one protocol (see Layout). There are two
kinds of door. A **file** is a one-time input, read with its details supplied inline. A
**connector** is a live system, registered once by name. One subsection per adapter:

#### CSV statements

Every bank exports CSV and it needs no credentials, so it is the default transport. `import` reads
one with the account, currency, and columns named inline (see above). The parser accepts the
shapes banks actually emit — `$1,234.56`, `(45.00)` for a negative, a single signed column or a
debit/credit pair, a blank amount as zero — and refuses a row carrying both a debit and a credit.
Each line is stored already normalized, so the books never re-parse the file.

#### Ledger files

The plain-text ledger format is both a door and the artifact. `import` reads a ledger file with no
flags: each entry's single amountless posting names the account its line came from, and the file's
own categorization is deliberately not carried in — the rules place every line, so the books stay
a fold. `bk books -format ledger` writes the same format back out as the committed artifact,
read-only and regenerated whole.

When a rule renamed a payee, the artifact keeps the line's raw description as a `; memo:` note (an
ordinary ledger entry note), and the reader prefers it. That makes the trip honest: importing the
artifact into an empty book regenerates the same fingerprints, and the same rules fold it to the
same books — which is exactly what the round-trip test drives, end to end, on every run.

What the artifact deliberately does not carry is the rules, corrections, invoices, and connectors
themselves. Those are facts, and facts live in the log: `.bookkeeper/log.jsonl` is the complete
backup — one committable file holding everything the books cannot recompute — and restoring is
copying it back (or `git checkout`). Encoding facts into artifact comments would be a second copy
that could drift from the first, which is the one thing the design refuses.

#### Another book's log

The log is its own interchange format, so combining two books is an import, not a new adapter:

```sh
bk import ../business/.bookkeeper/log.jsonl
```

The other book's events replay here in their order. Statement lines, invoices, bills, and exports
dedupe by fingerprint, so a line both books saw lands once; rules and corrections are recorded
again here, later than everything this book holds, so where both books answered the same question
the imported answer wins, and a rule pattern both books authored folds to one rule rather than
two. A transfer each book saw from its own side — chequing out, savings in — pairs up once the
books merge, and the movement counts once. Re-importing the same file is a no-op, keyed by a
fingerprint of its content, exactly as re-importing a statement is.

#### Connectors: register, list, rm

A connector is bidirectional in principle: `export` writes to it today, and importing from it by
name is the same registry, built later. Registering one is a logged fact (`connector.registered`)
and moves no data by itself; `export` is the verb that does.

```sh
bk connectors register <name> -kind rentapp -url <url> -token-env <ENV> -account <a> [-currency <c>]
bk connectors list   # NAME, KIND, URL, ACCOUNT, CURRENCY, TOKEN-ENV
bk connectors rm <name>
```

- `<name>` is yours to choose and is how every other command refers to it: `bk export rent`.
- `-kind` names the adapter that speaks the system's protocol; each kind below.
- `-url` is the system's base URL.
- `-token-env` names the **environment variable** that holds the bearer token. The token itself is
  never stored: the log keeps only the variable's name and reads it at the moment the connector is
  used, so the books stay committable. Set the variable in your shell (or profile) before an
  export; a missing one is refused with the variable named.
- `-account` and `-currency` say which ledger account's lines the connector concerns.

`connectors rm` forgets one; like everything else the registration stays in the log and the fold
drops it, so a re-register is a new fact, not an edit.

#### The rent app (`-kind rentapp`)

The first connector: `export` records rent the books already booked back to it, so its paid/unpaid
state stays current. Which lease a deposit belongs to is not in the bank memo, so the tenant's
rule carries it as metadata: `-meta rentapp.lease=<id>` rides onto the categorized deposit, and
`export` sends each such deposit as its lease, amount, and date, with the deposit's fingerprint as
the idempotency key — so a repeat records nothing twice, and a partially failed run reports which
deposits failed while the rest stand. Without `-confirm` it is a dry run:

```sh
export BK_RENT_TOKEN=...   # the rent app's bearer token
bk connectors register rent -kind rentapp -url https://rent.stcroixproperties.ca \
  -token-env BK_RENT_TOKEN -account "Assets:Bank:Chequing" -currency CAD
bk rules set "hyungjin" -category "Income:Real Estate:Rent:22 Lisgar Street" \
  -meta rentapp.lease=31
bk export rent            # dry run: what it would record
bk export rent -confirm   # records each rent deposit against its lease
```

`rules set` authors a rule: it adds a pattern not yet known, or changes the one already matching it.
Each is one event. Order decides which of two matching rules wins, so a new rule lands at the end
unless `-before` places it ahead of another. `rules rm` and `mv` drop and reorder.

```sh
bk rules set "shell|petro" -category "Expenses:Travel:Fuel" -payee "Fuel Stop"
bk rules set "city water"  -category "Expenses:Utilities:Water" -before "water"
bk rules list
```

`books` folds the log and renders it: a table or JSON to read, or the ledger artifact. `-account`
narrows any of the three to the lines posting to a matching account — repeat it to name several —
so there is no separate review command: the decision queue is just the books, filtered:

```sh
bk books                                  # a table, to read
bk books -account Uncategorized           # only the lines the rules could not place
bk books -account Fuel -account Water     # several accounts, one reading
bk books -from 2026-03-01 -to 2026-03-31  # exactly March: that month's lines and health line
bk books -format json                     # the same reading for a machine
bk books -format ledger                   # regenerates .bookkeeper/books.ledger
bk books -account Fuel -format ledger     # a filtered ledger, to stdout; the artifact stays whole
```

```text
FINGERPRINT         DATE        PAYEE                AMOUNT   POSTS TO
6d67c4670ff1e372-1  2026-03-01  Fuel Stop            -62.40   Expenses:Consulting:Travel:Fuel
bf3292b4aac90b2e-1  2026-03-02  Acme Hardware        -84.20   Expenses:Real Estate:Materials:Uncategorized
a106c3b1d01636de-1  2026-03-05  J. Smith             1600.00  Income:Real Estate:Rent:123 Example Street
5f79d9a707bc433f-1  2026-03-12  UNKNOWN MERCHANT 88  -39.99   Uncategorized

9 lines posted, 2 of them uncategorized (bk books -account Uncategorized shows only them)

INCOME       EXPENSES    NET          UNCATEGORIZED
1600.00 CAD  146.60 CAD  1453.40 CAD  -39.99 CAD
```

The fingerprint is the handle every correction takes, which is why the table leads with it. The
closing block is the health line: income, expenses, net, and money whose kind is unknown — kept in
statement sign rather than guessed into either column — one row per commodity, since amounts of
different commodities cannot honestly sum. Every format ends with this same line, computed once
from the same fold (JSON carries it as a `summary` object, the ledger as a trailing comment that
ledger tools ignore), so the formats cannot disagree; a filtered reading is summarized as
filtered.

### Cash and accrual are one log read two ways

Cash-basis books record money when it moves; accrual-basis books record value when it is earned or
incurred, before the cash follows. bookkeeper does not choose between them and does not store a mode.
The basis is a **read-time lens** over the one log, chosen with `-basis`:

```sh
bk books -basis cash      # only money that moved. The default, and every earlier example
bk books -basis accrual   # also books the invoices and bills that have not been paid yet
```

Cash basis is what every example above already is: it ignores invoices and bills entirely, so it is
exactly the books bookkeeper was born on. Accrual basis adds the value you have recognized but not yet
settled — an invoice raised, a bill received — each as its own line.

**Choosing the basis triggers no event.** `-basis` (and its optional `-since`) are read-time flags,
parsed fresh on each command and stored nowhere: the books carry no mode. That is the principle above
at work. The recognition timing is derivable, so it is a fold, and a fold is never written down. The
only accrual facts in the log are the inputs a statement cannot supply: `invoice.raised`,
`bill.received`, and `settled` (which line cleared which). So with no invoices and no bills there are
no accrual facts to re-time, and `-basis cash` and `-basis accrual` return identical books. The same
lens drives the statements too: `bk report -basis accrual` gives an accrual income statement. Do not
confuse this with the cost-basis *method* (ACB or FIFO), which is an asserted decision and does live
in the log as `policy.set`: that changes how a gain is computed, it is not a question asked on read.

An **invoice** is money owed to you; a **bill** is money you owe. They are the one kind of fact a bank
statement cannot supply, because the money has not moved, so they are recorded rather than folded from
a line. Each is a first-class thing you do, so each is its own command, the way rules and connectors
are:

```sh
bk invoice raise -party "J. Smith" -amount 1600.00 -category "Income:Consulting"  -date 2026-03-01
bk bill    receive -party "Power Co" -amount 142.03 -category "Expenses:Utilities:Power" -date 2026-03-02
```

An invoice debits a receivable and credits income; a bill is the mirror, debiting an expense and
crediting a payable. They share all their machinery and differ only in signs and words.

Where it parks defaults to `Assets:Receivable` (or `Liabilities:Payable` for a bill); `-account`
overrides, so each customer or vendor can carry their own sub-account. On the accrual basis the
invoice books on the day it was earned:

```text
2026/03/01  * J. Smith
  Income:Consulting  -1600.00 CAD
  Assets:Receivable
```

When the deposit that pays it lands in the bank, `invoice settle` records which line paid which
invoice, so the cash clears the receivable instead of booking the income a second time (that income
was booked when the invoice was raised):

```sh
bk import march.csv -account "Assets:Bank:Chequing" -currency CAD -amount Amount
bk invoice settle 9617607456a06619 -tx 6afa3719db1eb739-1
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
is known*. A wrong invoice is dropped with `invoice void` — the same verb as voiding a bad import,
one operation on a different noun.

You do not have to hunt the fingerprint, though. `bk invoice list` (and `bk bill list`) prints, for
each open accrual, the bank lines that plausibly settle it under a **CANDIDATES** column — the same
transfer-pairing heuristic (same amount, within a few months, not already used), surfaced instead of
applied. Settling is then copying a suggested fingerprint, not grepping the log:

```text
ID                DATE        PARTY     AMOUNT       ... SETTLED BY  CANDIDATES
9617607456a06619  2026-03-01  J. Smith  1600.00 CAD  ...             6afa3719db1eb739-1
```

The offer is never taken on its own — the fold still will not decide which line clears which invoice
— but the choice is now a glance, not a search.

### Aging: what is still owed, and how overdue

`invoice aging` ages the open receivables; `bill aging` the open payables. Each is what is still
owed, oldest first, bucketed the way every aging report is — current, 31-60, 61-90, 90+ — with a
subtotal per bucket. Settled and voided accruals have already left the fold, so only genuinely
outstanding money shows.

```sh
bk invoice aging -as-of 2026-07-11
```

```text
ID                DATE        PARTY     AMOUNT       DAYS  BUCKET
78d4de825655d2de  2026-01-15  Deadbeat  300.00 CAD   177   90+
32b0e0f178017f30  2026-05-01  Slow Co   1200.00 CAD  71    61-90
820513f55bc819c5  2026-06-20  Fresh Co  500.00 CAD   21    current

BUCKET   TOTAL        COUNT
current  500.00 CAD   1
61-90    1200.00 CAD  1
90+      300.00 CAD   1
```

Like everything else it is a fold over the log as of a date, not a stored report: `-as-of` ages
against any day, and the answer is recomputed each time.

Because the basis is a lens and invoices are additive facts, you can **start on cash and turn on
accrual later** with no migration: raise invoices from whatever day you begin, and every period
before that reads identically under both bases, because there is nothing there to accrue.

`-since` makes that switch explicit when you want it:

```sh
bk books -basis accrual -since 2026-07-01   # accrue only invoices/bills dated on or after July 1
```

On the accrual basis it books only accruals dated on or after the effective date; an earlier one is
dropped whole and reads as cash — no receivable, and its payment books as ordinary income or expense
when it lands. This is a read-time argument, never stored, so the seam it creates is a fact about how
you are reading the log, not a change to it. The one honest caveat is a receivable open *across* the
date: it is not shown until its cash arrives, when it books as cash rather than clearing a receivable
that was never raised. (Carrying those forward as an opening balance is the natural next step, and is
not precluded.)

### Fixing a rule fixes history

Learn that every hardware receipt was Unit 1, and say so once. `set` changes only the fields you
name, so the payee is left as it was:

```sh
bk rules set "acme hardware" -category "Expenses:...:Unit 1" -why "the receipts were all Unit 1"
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
by its fingerprint, and it wins over whatever the rule said. `bk books -account Uncategorized`
lists every waiting line with its fingerprint, and any unique prefix of one is enough, as with a
git hash:

```sh
bk categorize 0d76f1f1 -category "Expenses:...:Unit 1" -payee "Acme" -why "receipt was Unit 1"
```

One charge can serve two properties, so an assertion can be a split, and it is only accepted if the
postings still account for the whole line:

```sh
bk categorize 0d76f1f1 \
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
and a re-import is a no-op on them. `void` is the way out.

```sh
bk void 33247b87 -why "imported to the wrong account"
```

It does not delete the imported event. It appends a fact that supersedes it, and the fold drops the
line, so the mistake and its correction both stay in the log and the git diff is still a pure
append. Re-importing the same statement will not bring the line back, because the imported fact is
still there and the import stays a no-op.

The repair flow the append-only log makes possible: re-import with the right flags (the corrected
line lands under a new fingerprint, since the amount or date changed), then void the garbage one.

### Undo, restore, start over

Three different itches, three different tools, only one of them new:

- **A wrong fact** is superseded, never erased: a bad line is `void`, a wrong rule is `rules rm`,
  a wrong settlement is `-reopen`. The mistake and its correction both stay in the log.
- **A wrong batch** — an import with the wrong flags, a merge you regret — is git's job: every
  write is a pure append, so `git restore .bookkeeper/log.jsonl` rolls the book back to any
  committed point, and the diff you are discarding is readable before you discard it.
- **Starting over** is `bk reset`: the log emptied, the artifact removed, the directory still a
  book. It is the one verb in the tool that destroys history, so without `-confirm` it is a dry
  run that says what would be lost — and after a reset the old log is recoverable only from git.

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

A **cross-currency** move — $1000 CAD out of chequing, $740 USD into a US account — is the same one
movement, but the two sightings are not equal and opposite, so the amount check cannot pair them.
Here the price does the work the mutual naming does above. Categorize the leg that left, naming the
account the money reached and the rate it cleared at:

```sh
bk categorize -tx <cad-leg> -payee "Transfer to USD" -post "Assets:USD=740 USD @@ 1000.00 CAD"
```

That posting ties the two real amounts together — the 740 USD that landed, priced at the 1000 CAD
that left — which is the cross-currency stand-in for mutual naming, so the USD sighting is recognised
as the duplicate and dropped. The kept entry books both accounts, and because the received currency
is acquired at a stated cost, the USD holding picks up a cost base in CAD for free (the same fold
that prices a stock purchase), so a later conversion back realizes the exchange gain or loss.

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
bk rules set "acme hardware" -payee "Acme Hardware" -category "Expenses:Real Estate:Materials:Uncategorized"
```

Categories are free-form account paths, so you can go as deep as your books do, down to the
property and unit, and stop at `Uncategorized` wherever you cannot. No catch-all rule is needed: a
statement line already knows which account it came from, and a line no rule matches posts to
`Uncategorized`.

## Design rules

- **Deterministic where money is recorded.** Every capability is a command with machine-readable,
  idempotent I/O, so a person or an agent runs the tool the same way. Whoever operates it proposes;
  code writes. The books stay a pure function of the log.
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
cost base the shares leave at is folded from the account's purchases, so the gain is derived rather
than stored: correct an earlier purchase's base and every later sale's gain moves with it. A sale
that disposes of more than the account holds is refused when it is asserted. The base is a total,
never a divided per-unit price, so a full disposal returns the exact cost and a partial one rounds to
the cent without leaking.

The cost-basis method is itself a fact in the log, set by `policy set` and folded like a rule.
`policy set -method acb` (the default, Canada's rule for capital property) blends every purchase into
one average; `policy set -method fifo` draws each sale from the oldest lots first. Without `-account`
it sets the book-wide default; with one it overrides that account only, so a US account can run FIFO
in the same book a Canadian default keeps on ACB. Because the method is folded, not baked into the
sale, changing it reclassifies every affected gain on the next regeneration.

Internal transfers seen in both accounts' statements are recognised and booked once, as a
deterministic fold over the lines and their categorization, so the money is not double-counted —
across a currency boundary too, where the rate on the categorized leg ties the two amounts together.
A bad line is undone with `void`, which supersedes the imported line without deleting it.

Cash and accrual are the same log read through two lenses, chosen with `bk books -basis`. Cash is the
default and every statement example is already it. Accrual also books the value recognized before its
cash: `invoice raise` raises a receivable and `bill receive` raises a payable, and `settle` records the
bank line that paid one so the cash clears the parked account rather than booking the value twice. The
basis is a read-time choice, never stored, so a book can start on cash and turn on accrual later with
no migration; a wrong accrual is dropped with `void`. Recording the same accrual twice is a no-op,
keyed by a fingerprint of its content, exactly as re-importing a statement is. Invoices and bills are
mirror images sharing one machinery, so a receivable and a payable book side by side and differ only
in signs and words.

Inputs are files (CSV, and the ledger form it writes), read once inline with `import`, and
connectors. `import <connector>` fetches a registered connector's lines and records them through the
same path a file does, deduped by fingerprint; the connector already carries its account and
currency, so a name is all `import` needs. The fetch is a boundary the core never crosses (a
`connector -> transactions` value the CLI supplies), so a future aggregator or official-API adapter
is a new case, not a rewrite. A bank connector (kind `rbc`, `simplii`, or `pcfinancial`, for the
Canadian banks that expose no free transaction API) drives a headless-browser **Playwright** script
to read the account: Go runs the per-institution script with the connector's details in the
environment, and the script prints the lines as JSON, which Go normalizes and fingerprints like a
CSV. The automation lives in Node so the Go binary stays stdlib-only. The credential is a saved
browser **session**, never a stored password: the person signs in themselves the first time (and
whenever it expires) in a headed browser, and bookkeeper keeps only the session that results, reused
headless after. Sign-in is folded into `import` and self-heals — a live session imports silently; an
expired one re-opens the browser when a person is present, or fails fast with a "sign in again" hint
when one is not, so an agent is never left staring at a browser it cannot answer. `TokenEnv` keys the
session, so accounts on one login (every RBC account) share it. The shared session-and-sign-in logic
is one harness; each institution script is just its selectors, and pinning those to the real site is
all that remains per bank. The design — and the FDX-shaped official API this grows into once Canada
designates one, realistically 2027 — is written up in `docs/importing-from-banks.md`.

A connector is bidirectional in principle, and `export` was the direction built first. The rent app
is the first connector: `export` records rent the books already booked back to it so its paid/unpaid
state stays current, with its token kept in the environment rather than the books. The lease a
deposit belongs to rides on the tenant's rule as metadata (`rentapp.lease`), and the export is keyed
by the deposit's fingerprint, so re-running it records nothing twice. It is export-only, so importing
from it is refused: the rent money already arrives on the bank statement, and importing the rent
app's copy would double-count it.

The tool is driven entirely through commands, so a person or an agent operates it the same way.
`books -format json` reports the whole books, and the authoring commands take an `-actor` (default
`human`, e.g. `-actor claude`), so whoever answers works through one path and the log records the
hand without the core caring whose it is.

An account can carry metadata (`accounts set`, folded like a rule): a letterhead name and address on
the account money moves through, a customer's address on the account a payment is booked to. `receipt
-tx <fingerprint>` reads that metadata to render one settled transaction as a printable invoice or
receipt: the account's letterhead, the payee as the bill-to, the postings as line items, always
stamped PAID because every line came off a statement. It bills in the currency that was billed, so a
USD contract paid in CAD reads as the USD owed. (This prints a document from money that already
moved; the accrual `invoice raise` above is the invoice for money still owed.)

`report` folds the books into the full picture of the company: an **income statement** over a period
(what was earned and spent, by account, with the net) and a **balance sheet** as of its end (assets
held, liabilities owed, and net worth). A P&L is flows over a period, so income and expenses live
there; a balance sheet is a position on a date, so that is where liabilities sit. The balance sheet
counts the source-account posting the entries elide, since that is where cash and debt actually
accumulate. `-income` or `-balance` shows one; `-account` narrows by a substring of the path, so "123
Main" reaches a property's rent and its repairs at once; `-from`/`-to` bound the period. Totals are
per commodity, because a USD fee and CAD rent, or cash and shares, do not sum without a price. It is
a fold, so it adds a view, not state.

`report -gains` is the tax-time view off the same cost-basis fold: a disposal per row — date, shares,
proceeds, cost base, and realized gain (a loss is negative) — with the total gain, for a year bounded
by `-from`/`-to` (Canada's Schedule 3, the T5008 world). The proceeds and base are read straight off
the resolved sale, never recomputed, so the schedule and the books can never disagree.

Both `receipt` and `report` render **text by default and `-format html`** for a self-contained page
to open and print to PDF, with `-out` to write a file. The HTML uses the standard library's
templates, so bookkeeper needs no PDF library and stays stdlib-only. These, and the ledger, are the
outputs besides the log: a deliberate widening of "the artifact is the product". A new document
follows the same shape (see below), so it is the same convention, not a special case.

## Next

In order:

1. **Pin each bank's selectors.** The browser import itself is built: `import <connector>` drives a
   Playwright harness that reuses a saved browser session, signs in again in a headed browser when it
   has expired, saves the fresh session, and prints the account's lines as the JSON the importer
   expects (`BK_IMPORT_*` in, `[{date, description, amount}]` out). The credential is that session,
   never a stored password — the person enters their password and 2FA in the browser themselves, and
   only the session is kept, reused headless after, keyed by `Connector.TokenEnv` so accounts on one
   login share it. What remains per bank is the three selectors that differ by site — `isLoginWall`,
   `signIn`, `readRows`, marked `TODO` in each of `scripts/rbc.js`, `simplii.js`, `pcfinancial.js`;
   `npx playwright codegen <bank url>` records them. Start with RBC: one login reaches all five of its
   accounts. Until a bank's selectors are pinned, a CSV export is the way in.

Around the engine, the product is a **web layer** that wraps it, and it owns two things this tool
deliberately does not: the **human surface**, so a person never sees a fingerprint (chat over the
books, and the `report` and `receipt` views to read and print), and the **secret store** a live
connection needs. bookkeeper's job is to stay a clean thing to drive and to keep secrets out of the
committed log: the web layer holds them and injects them at run time through the `TokenEnv` /
`BK_IMPORT_*` contract. A bank is the softer case already handled — its credential is not a stored
secret at all but a saved browser session, kept machine-local outside the book and re-earned by
signing in when it expires. For a genuine token (an FDX bearer, say), and to encrypt that session at
rest, the same store applies: standalone, the bare CLI reads it from an **encrypted `.env`**,
decrypting at run time with a key held in the **OS keychain** — no plaintext secret on disk, no
passphrase, and no new build dependency: AES-GCM decryption is standard-library, and the keychain is
reached through an adapter (the way the browser import reaches Node), so the binary stays stdlib-only
and the tool works without the web layer too. Either way the book of record stays plain, committable,
and secret-free.
That division is the whole trust story — a model proposes, code writes, and every change is an
auditable git diff — and it only holds because the engine underneath is exactly what it is.

The larger focus after those is **future projections**: a recurring fact (a `periodic` — rent on the
first, a monthly mortgage) and a `project` fold that carries it forward to show projected cash flow
and balances, with expected-vs-actual reconciliation ("which rent has not landed") as its follow-on.
Like every other view it is a fold over the log, so it adds a projection, not stored state.

Backlog, until asked: remembered CSV import profiles (so a file's account and columns need not be
retyped each month), an aggregator or FDX official-API adapter if a free one appears (realistically
2027), reading the share quantity straight off a brokerage statement so a trade need not be typed,
and out-of-tree connectors as installable plugins.

## Layout

Ports and adapters: a domain core, with the outside world reached only through adapters.

```text
lib/model/            the normalized shapes: Amount, Transaction, Posting, Entry
lib/books/            the commands and folds: Import, AddRule, Categorize, Raise, Settle, Ledger, ...
lib/rules/            the deterministic categorization engine
lib/costbasis/        folds acquisitions and disposals into a cost base (ACB, FIFO)
lib/eventlog/         the append-only log and its storage
lib/store/            locating and opening a .bookkeeper book of record
lib/adapters/source/  imports statements: CSV, ledger files, and banks (via a Playwright script)
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

## Building on it

For an agent or a person writing code against bookkeeper, four conventions cover almost everything.
Follow the nearest existing example; each capability has exactly one.

- **A new view (report, document).** Fold `books.Ledger(log)` into a plain view struct (a pure
  function, no I/O), render it, and print it. Renderers default to **text** and take
  **`-format html`** for a self-contained, print-to-PDF page built with the standard library's
  `html/template`; `-out` writes a file through the shared `writeOut`. `report` and `invoice` are
  the pattern. Keep the fold pure and the render dumb, so both are testable without a store.
- **A new input.** The input port is `Source func() ([]model.Transaction, error)` in the CLI: every
  input is one, and `importFrom` runs it and hands the lines to `books.Import`, deduped by
  fingerprint. The adapters that yield those lines live in `lib/adapters/source` — a file reader
  (`ReadCSV`, `ReadLedger`) or a bank (`ReadBank`), each fingerprinting with `source.Identify`. A new
  input is a reader plus a `sourceFor`/`fetcherFor` case that binds it into a `Source`; the core
  never reaches the network, only an adapter does.
- **A new authored fact.** Rules, sources, policies, and account metadata are all the same shape: a
  collection in the log, keyed by identity, folded on read (see `lib/books/rules.go`,
  `policy.go`, `accounts.go`). One command, one event, latest fact wins.
- **A new destination.** An adapter that writes out (see `lib/adapters/rentapp`), driven by the CLI
  and keyed by a stable fingerprint so a re-run records nothing twice.

The design rules above are the guardrails: deterministic where money is recorded, idempotent end to
end, say only what is known, stdlib-only. Every change is test-first, and the git hooks run
`gofmt`, `go vet`, the tests, and markdownlint on commit.

## Development

```sh
go test ./...
go vet ./...
gofmt -l .
go build ./cli
```
