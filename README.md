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
| `transaction.discarded` | that line was garbage; keep it out of the books |
| `rule.added` | a pattern should be handled |
| `rule.changed` | a rule's answer is wrong |
| `rule.removed` | a rule should stop firing |
| `rule.moved` | two rules fire in the wrong order |
| `source.added` | an account, and how to read its statements |
| `source.changed` | we were reading an account wrong |
| `source.removed` | stop reading an account |

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

You still edit a file, which is the right surface for an ordered document. `rules load` folds the
current set out of the log, diffs the file against it, and emits the intents the diff implies, so
loading an unchanged file records nothing. `rules list` prints what the log says.

### Sources are doors, not folds

A source says how to read one account's statements, so it is authored knowledge and belongs in the
log for the same reason a rule does. But it behaves differently, and conflating the two would
mislead:

- Fix a **rule**, regenerate, and history reclassifies. A rule is an input to every fold.
- Fix a **source**, regenerate, and nothing happens. It was used once, at the door.

`transaction.imported` stores the transaction already normalized. Re-reading the log never
re-parses a CSV. It could have stored the raw row and normalized on read, which would make a source
a fold input and let a fixed mapping repair history. It does not, because the fingerprint is built
from the normalized fields: re-normalizing would move every fingerprint and silently orphan every
correction keyed to one. Fingerprinting the raw row instead is worse, since banks re-export the
same line with different columns, and that breaks the deduplication that runs every day.

So a source is logged for provenance, not for folding. A mapping that drifts in a file changes how
lines normalize, which changes their fingerprints, which silently books a second rent payment.
Recording which source read a line makes that diagnosable and bounded, and `transaction.discarded`
is how a badly-normalized import leaves the books.

A source's identity is its **account**, so `import` names an account rather than a file, and
importing against an account nobody has taught bookkeeper to read is an error that lists the
accounts it does know.

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

`init` creates a set of books in the current directory, marked by a `.bookkeeper` directory the way
a git repository is marked by `.git`. Every other command finds it by walking up, so you can run
them from anywhere inside your project.

```sh
bk init
# Initialized a book of record in /your/project/.bookkeeper
```

`sources load` teaches bookkeeper which accounts exist and how to read their statements:

```sh
bk sources load -file sources.json
# 1 sources: 1 added, 0 changed, 0 removed
```

`import` records what a statement said. It writes facts, and it is safe to run twice:

```sh
bk import -source "Assets:Bank:Chequing" -csv statements/march.csv
# 9 lines read: 9 imported, 0 already in the log

bk import -source "Assets:Bank:Chequing" -csv statements/march.csv
# 9 lines read: 0 imported, 9 already in the log
```

`rules load` records a rule file into the log as the per-rule intents its diff implies. Loading an
unchanged file records nothing:

```sh
bk rules load -file rules.json
# 8 rules: 8 added, 0 changed, 0 removed, 0 moved

bk rules load -file rules.json
# 8 rules: 0 added, 0 changed, 0 removed, 0 moved
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

### Fixing a rule fixes history

Learn that every hardware receipt was Unit 1, and say so once:

```sh
bk rules load -file rules.json --why "the receipts were all Unit 1"
# 8 rules: 0 added, 1 changed, 0 removed, 0 moved
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

Both files below are editing surfaces. `sources load` and `rules load` record them into the log,
which is what `import` and `books` read.

A **source** is one account and how to read its statements. Banks disagree about column names, date
formats, and whether amounts are one signed column or a debit/credit pair. The currency belongs to
the account, not to whoever runs a render.

```json
[
  {
    "account": "Assets:Bank:Chequing",
    "currency": "CAD",
    "date": "Date",
    "description": "Description",
    "amount": "Amount",
    "date_format": "2006-01-02"
  }
]
```

A **rule** matches a description and supplies a payee, an account to post to, or both. Rules are
ordered, and for each field the first rule that supplies it wins. `match` is the rule's identity,
so no two may share one. Feed the file to `rules load`; the log is what `books` reads.

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
- **An amount is an exact quantity of a commodity.** Held as integer minor units, so no float ever
  touches the books, and the commodity may be a currency or a share. Ledger-cli's model, which is
  why the books can hold anything ledger can.
- **Connectors know the outside world; the core does not.**
- **Say only what is known.** Truncate an account path rather than guess a leaf, and never guess a
  kind.
- **Nothing blocks.** Every line posts, every question is a digest, and every correction is cheap
  because regenerating is cheap.

## Status

Everything lives in a `.bookkeeper` directory found by walking up, the way git finds `.git`. The
log is `log.jsonl`, committed, one event per line; the ledger is its committed artifact.
Re-importing an overlapping statement is a proven no-op, two renders of the same log are
byte-identical, changing a rule reclassifies history in one appended event, correcting a single
line overrides the rule for that line only and survives later rule changes, and a wrong working
directory is refused rather than turned into a new empty book of record.

Amounts are an integer `Amount` (quantity, scale, commodity), serialized in the log as a
ledger-style string like `84.20 CAD` or `10 AAPL`. No float touches money at any boundary; the CSV
reader's old float is gone. An entry is single-commodity for now: mixing commodities balances only
through a price, which is refused until that slice exists.

Next, in order: `Discard`, `Match`, the CSV views (`bk transactions --csv` and friends, for the
tabular parts of the data), the model tier, the digest, and the destinations. Prices and cost basis
(so a brokerage account can hold shares against cash) are a later slice; the `Amount` type is ready
for them.

## Development

```sh
cd cli
go test ./...
go vet ./...
gofmt -l .
```
