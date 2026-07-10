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

## Design rules

- **Deterministic where money is recorded.** A model may propose categories; code does the writing,
  the deduplication, and the arithmetic. The books are a pure function of statements, rules, and
  decisions, so regenerating them is boring and a diff line means something actually changed.
- **Idempotent end to end.** Every line carries a stable fingerprint, so a re-import is always
  safe. Two genuinely identical charges on one day stay two charges.
- **Money is integer cents.** Floats never touch a ledger.
- **Connectors know the outside world; the core does not.** The core understands only normalized
  transactions. CSV is the default transport because every bank exports it and it needs no
  credentials.

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

## Development

```sh
cd cli
go test ./...
go vet ./...
gofmt -l .
```
