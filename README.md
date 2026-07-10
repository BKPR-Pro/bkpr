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
| `transaction.matched` | this line is the same movement as another; do not book it twice |
| `rule.added` | a pattern should be handled |
| `rule.changed` | a rule's answer is wrong |
| `rule.removed` | a rule should stop firing |
| `rule.moved` | two rules fire in the wrong order |

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

### Nothing has to be acknowledged

There is no `confirmed` event, and there is nothing to clear. Both would build an inbox: a list
that only empties if you work it. The queue is the friction this tool exists to remove.

**Silence is the confirmation.** If you did not correct a line, the rule stood. The signal that
tells a good rule from a bad one is corrections divided by how often the rule fired, and both of
those are already folds over the log. Six corrections out of six firings means go fix the rule. Six
out of six hundred means it is fine.

### Commands and folds

A command captures one intent, guards a precondition, and emits one event. `ImportStatement`,
`AddRule`, `ChangeRule`, `RemoveRule`, `MoveRule`, `Categorize`, `Match`. Nothing else writes.

`TrackOnce` appends a fact that can only be true once and reports `ErrAlreadyTracked` otherwise,
which is how re-importing an overlapping statement becomes a no-op rather than a second rent
payment. `Track` appends a fact that may recur. Which facts are once-only is the caller's
business, so storage carries no domain knowledge: ordinary events leave `once_key` NULL, SQLite
counts NULLs as distinct in a unique index, and only once-only events collide. The idempotency
invariant is physical rather than remembered.

Rules do not interleave with transactions in one chronological fold. If they did, a rule added in
June would not reach a transaction imported in March, and fixing a rule would not fix history,
which is the whole point of regenerating. So it is two folds over one log: rule events fold to the
current rule set, that set categorizes every transaction, and assertions keyed to a fingerprint
override the result.

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
2. **A model.** Only the lines that came out `Uncategorized`. It proposes postings; code writes.
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
DATE        PAYEE                AMOUNT   POSTS TO
2026-03-01  Fuel Stop            -62.40   Expenses:Consulting:Travel:Fuel
2026-03-02  Acme Hardware        -84.20   Expenses:Real Estate:Materials:Uncategorized
2026-03-05  J. Smith             1600.00  Income:Real Estate:Rent:123 Example Street
2026-03-12  UNKNOWN MERCHANT 88  -39.99   Uncategorized

9 lines posted, 2 of them uncategorized
```

## Books

`-format ledger` emits plain-text double-entry entries.

```sh
go run ./cli categorize \
  -mapping cli/testdata/mapping.json \
  -rules   cli/testdata/rules.json \
  -csv     cli/testdata/statement.csv \
  -format  ledger > books.ledger
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
  { "match": "acme hardware", "payee": "Acme Hardware",
    "category": "Expenses:Real Estate:Materials:Uncategorized" },
  { "match": "city water", "payee": "City Water Utility",
    "category": "Expenses:Real Estate:Utilities:Water:123 Example Street" }
]
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
- **Money is integer cents.** Floats never touch a ledger. Event data is raw JSON precisely so
  nothing round-trips through a float on the way in.
- **Connectors know the outside world; the core does not.**
- **Say only what is known.** Truncate an account path rather than guess a leaf, and never guess a
  kind.
- **Nothing blocks.** Every line posts, every question is a digest, and every correction is cheap
  because regenerating is cheap.

## Status

Built: the CSV source, the rules engine, the ledger writer, and the append-only event log with its
in-memory and SQLite adapters.

Next, in order: `ImportStatement` and the transaction projection, so `categorize` reads the log and
a re-import is a proven no-op. Then the rule events, which move the rule set off disk and into the
log. Then `Categorize`, which turns postings into event data. Then the model tier, the digest, and
the destinations.

## Development

```sh
cd cli
go test ./...
go vet ./...
gofmt -l .
```
