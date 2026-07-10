# bookkeeper

Turns bank and card statements into a set of books.

The goal is Mint's touch with a real ledger's resolution: you set it up, it runs, and the only
recurring work is re-categorizing a couple of things every once in a while.

## Follow the rules; correct later

**Every line posts. Nothing blocks on a question.** Re-categorizing presupposes a category was
assigned, so the machine always assigns one. A gate would be exactly the friction this tool exists
to remove.

Where a rule's category is a defensible default rather than a fact, the rule is marked `uncertain`
and its entry is written as **pending** (`!`) with the reason as a comment. Your correction list is
one command you already know:

```sh
ledger -f books.ledger print --uncleared
```

### The guardrail is depth, not certainty

- A wrong **leaf** (Unit 1 vs Unit 2) costs insight only. Guess freely.
- A wrong **kind** (an expense booked as income, a transfer booked as an expense) breaks the books
  and does not self-correct. Never guess here: a line whose kind is unknown posts to a top-level
  `Suspense` account, which keeps the `Income` and `Expenses` totals honest until you resolve it.

Some attributions are simply not in the data. A hardware store charge could serve any property;
that fact lives on the receipt, not in the description. No amount of matching recovers it, so the
rule defaults and flags rather than pretending.

### Rules are defaults; corrections are about one line

- **Rules** produce defaults, keyed by pattern. Always followed. Never learned automatically.
- **Corrections** are facts about one transaction, keyed by its fingerprint. They never generalize,
  so correcting a single hardware charge does not silently re-pin every future one.

Rules are data, not code. A different set of books means a different rules file, not a different
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
| a model's answer | no, it is nondeterministic and it cost money | in the log |
| your judgment | no, the receipt is in your truck | in the log |
| a rule | yes, it is deterministic data | in git |
| a categorization | yes, it is `rules(transaction)` | derived on read |
| a transfer pairing | yes, from the movement key | derived on read |
| the ledger file | yes, from the log and the rules | a generated artifact |

The last row is the one that changes how you work. **The ledger file is read-only output.** You
correct the books by recording a fact and regenerating, not by editing the artifact. That is what
buys the property everything else rests on: the books are a pure function of the log and the
rules, so regenerating them is boring, and a changed diff line means something actually changed.

### Events

Events are immutable, past-tense facts, in the collection `transaction`, keyed by the
transaction's fingerprint.

| event | what it means |
| --- | --- |
| `transaction.imported` | a statement line was read in. Once per fingerprint, ever |
| `transaction.categorized` | a line that had no categorization now has one |
| `transaction.confirmed` | a flagged default was reviewed and kept |
| `transaction.recategorized` | a categorization was reviewed and replaced |
| `transaction.matched` | this line is the same movement as another; do not book it twice |

An event's name says **what happened**. Its `actor` says **who**: you, a model, or a rule set at a
given commit. Reading `actor` should never be necessary to know what kind of fact you are looking
at, which is why there is no `categorized_by_model`. A person answering a `Suspense` line and a
model answering one are doing the same thing, and the log should say so.

Nothing is ever edited. Correcting a line twice appends two facts, the later fold wins, and how a
categorization came to be survives next to what it currently is.

### Why `confirmed` and `recategorized` are separate

They fold identically. A single `settled` event would produce the same books, and it would be
wrong.

Count them per rule instead. Six confirmations against the same `uncertain` rule mean the default
is good and it should stop flagging. Six recategorizations mean the rule is wrong and you should
go edit it. That is the only signal that ever tells you which, and a correction still never
promotes itself to a rule, because the attribution is context the description does not contain.

Collapse the two and the signal is gone permanently: the log is append-only, and you cannot
recover a distinction you never wrote down. This is the real cost of a vague event name.

### Commands and folds

A command captures one intent, guards a precondition, and emits one event. `ImportStatement`,
`Categorize`, `Confirm`, `Recategorize`, `Match`. Nothing else writes.

`TrackOnce` appends a fact that can only be true once and reports `ErrAlreadyTracked` otherwise,
which is how re-importing an overlapping statement becomes a no-op rather than a second rent
payment. `Track` appends a fact that may recur. Which facts are once-only is the caller's
business, so storage carries no domain knowledge: ordinary events leave `once_key` NULL, SQLite
counts NULLs as distinct in a unique index, and only once-only events collide. The idempotency
invariant is physical rather than remembered.

Derived state stays derived. The pending flag is not stored: a line is pending when its
categorization came from an `uncertain` rule and no `confirmed` or `recategorized` event follows
it.

### Storage

SQLite, because this is money. An interrupted cron run must not tear a line of the log in half,
and fsync discipline for a book of record is not worth hand-rolling. `synchronous` is raised to
`FULL`, because WAL's default trades away the last commit on power loss, which is the wrong trade
here. [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) is pure Go, so deployment is
still a single cross-compiled binary.

Storage sits behind a small interface. The tests fold over an in-memory adapter; the file holds
the books.

## Three tiers, and a model that never writes

1. **Rules.** Deterministic, free, reproducible. Handles almost everything.
2. **A model.** Only the lines no rule matched. It proposes a categorization; code writes it.
3. **You.** Never blocking. A digest, not a queue.

A model may label. Code does the writing, the deduplication, and the arithmetic. Quarantining the
nondeterminism is what keeps the books regenerable, and it is why a model's answer is written to
the log: it cannot be recomputed, so it must be remembered.

## Sources and destinations

The core understands only normalized transactions. A **source** brings lines in; a **destination**
acts on them. Only a connector knows about the outside world, and CSV is the default transport
because every bank exports it and it needs no credentials.

A destination that must not act twice sends `Event.Key()` as its `Idempotency-Key`. The event is
durable before any side effect runs, so a retry rebuilds the identical key from the identical
stored event. That is the only thing that makes a retry safe when the response was the part that
got lost.

## Usage

Categorize a statement against a rule set:

```sh
go run ./cli categorize \
  -mapping cli/testdata/mapping.json \
  -rules   cli/testdata/rules.json \
  -csv     cli/testdata/statement.csv
```

Output:

```text
DATE        PAYEE                AMOUNT   POSTS TO                                    REVIEW
2026-03-01  Fuel Stop            -62.40   Expenses:Consulting:Travel:Fuel
2026-03-05  J. Smith             1600.00  Income:Real Estate:Rent:123 Example Street
2026-03-12  UNKNOWN MERCHANT 88  -39.99   Suspense                                    ! no rule supplied a category

9 lines posted: 7 confident, 2 flagged for review
```

## Books

`-format ledger` emits plain-text double-entry entries. Lines the rules are confident about are
cleared (`*`). A line that is a defensible default, or whose kind is unknown, is written as pending
(`!`) with its reason as a comment. Every line posts, so the books stay complete and balanced, and
the guesses are trivial to find.

```sh
go run ./cli categorize \
  -mapping cli/testdata/mapping.json \
  -rules   cli/testdata/rules.json \
  -csv     cli/testdata/statement.csv \
  -format  ledger > statement.ledger
```

The output is a real ledger file, so the usual tools work:

```sh
ledger -f statement.ledger bal              # balances, which sum to zero
ledger -f statement.ledger print --uncleared # only the lines still needing an answer
```

## Configuration

A **mapping** says how one institution's CSV lines up with a transaction. Banks disagree about
column names, date formats, and whether amounts are one signed column or a debit/credit pair.

```json
{
  "account": "Assets:Bank:Chequing",
  "date": "Date",
  "description": "Description",
  "amount": "Amount",
  "date_format": "2006-01-02"
}
```

A **rule** matches a description and supplies a payee, an account to post to, or both. Rules are
ordered, and for each field the first rule that supplies it wins.

```json
[
  { "match": "acme hardware", "payee": "Acme Hardware", "category": "Expenses:Repairs:Materials",
    "uncertain": true, "reason": "hardware could serve any property" },
  { "match": "city water", "payee": "City Water Utility", "category": "Expenses:Utilities:Water" }
]
```

Categories are free-form account paths, so you can go as deep as your books do, down to the
property and unit. No catch-all rule is needed: a statement line already knows which account it
came from, and a line no rule matches posts to `Suspense`.

## Design rules

- **Deterministic where money is recorded.** The books are a pure function of the log and the
  rules. A model proposes; code writes.
- **Idempotent end to end.** Every line carries a stable fingerprint, so a re-import is always
  safe. Two genuinely identical charges on one day stay two charges.
- **Money is integer cents.** Floats never touch a ledger. Event data is raw JSON precisely so
  nothing round-trips through a float on the way in.
- **Connectors know the outside world; the core does not.**
- **Nothing blocks.** Every line posts, every question is a digest, and every correction is cheap
  because regenerating is cheap.

## Status

Built: the CSV source, the rules engine, the ledger writer, and the append-only event log with its
in-memory and SQLite adapters.

Next, in order: `ImportStatement` and the transaction projection, so `categorize` reads the log and
a re-import is a proven no-op. Then `Confirm` and `Recategorize`, which turn postings into event
data and the pending flag into a fold. Then the model tier, the digest, and the destinations.

## Development

```sh
cd cli
go test ./...
go vet ./...
gofmt -l .
```
