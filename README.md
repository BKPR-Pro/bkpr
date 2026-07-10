# bookkeepper

Turns bank and card statements into a set of books, and asks you about the few lines it cannot
work out on its own.

The goal is Mint's touch with a real ledger's resolution: you set it up, it runs, and the only
recurring work is answering a couple of questions every once in a while.

## How it categorizes

Three tiers, cheapest first. Each tier feeds the one below it.

1. **Rules.** Ordered, deterministic, free. The same statement always produces the same books.
2. **Model.** Only the lines no rule matched. Every accepted answer is written back as a rule, so
   the same payee is never asked about twice. This tier shrinks itself.
3. **You.** Only what the model will not guess at, such as which unit a hardware charge belongs
   to. Your answer also becomes a rule.

Rules are data, not code. A different set of books means a different rules file, not a different
build.

## Design rules

- **Deterministic where money is recorded.** The model proposes categories; code does the writing,
  the deduplication, and the arithmetic. Re-importing a statement must produce identical books.
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
DATE        PAYEE                AMOUNT   CATEGORY
2026-03-01  Fuel Stop            -62.40   Expenses:Consulting:Travel:Fuel
2026-03-05  J. Smith             1600.00  Income:Real Estate:Rent:123 Example Street
2026-03-12  UNKNOWN MERCHANT 88  -39.99   NEEDS REVIEW (no rule supplied a category)

9 lines: 8 categorized, 1 need review
```

## Books

`-format ledger` emits plain-text double-entry entries. Lines drawn from real bank data are
cleared (`*`). A line nothing could categorize is written as pending (`!`) against a placeholder
account, carrying its reason as a comment, so the books stay complete and the open questions are
trivial to find. Nothing is ever guessed at.

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

A **rule** matches a description and supplies any subset of payee, category, and balancing
account. Rules are ordered, and for each field the first rule that supplies it wins. That lets a
specific rule name the payee and category while a trailing catch-all supplies the account's
default balancing posting.

```json
[
  { "match": "acme hardware", "payee": "Acme Hardware", "category": "Expenses:Repairs:Materials" },
  { "match": ".", "balance": "Liabilities:Card:Visa" }
]
```

Categories are free-form account paths, so you can go as deep as your books do, down to the
property and unit.

## Development

```sh
cd cli
go test ./...
go vet ./...
gofmt -l .
```
