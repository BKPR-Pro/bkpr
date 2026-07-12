---
name: bookkeeper
description: Operate the bookkeeper CLI to turn imported bank and card statements into double-entry, ledger-cli-compatible books kept in git. Use when importing, categorizing, correcting, transferring, or reporting on transactions, invoices, bills, cost basis, or capital gains, or when generating a statement or receipt for a set of books.
---

# Operating bookkeeper

bookkeeper turns bank and card statements into double-entry books. The books are a pure fold over an
append-only log of facts committed to git, and they read as ledger-cli. You drive it with the `bk`
CLI. This file is the operating procedure. The README is the reference and the source of truth for
anything here; `bk help` and `bk help <command>` give exact flags.

## The one rule that governs everything

The log holds only what cannot be recomputed. Everything else is a fold. So you never store a total,
a balance, a gain, or a basis: you record a fact (an imported line, a rule, a categorization, an
invoice) and regenerate, and any number that can be derived is derived on read. Before writing
anything, ask whether it can be recomputed from what the log already holds. If it can, do not write
it, expose it through a fold instead.

## Say who is acting

Every authoring command takes `-actor`, and the log records it against the fact. It defaults to
`human`. Set it to whoever is operating: a person, or the name of the automation acting on their
behalf. It changes nothing about what the command does; it records who decided, so every change in
the git diff is attributable.

```sh
bk categorize <id> -category "Expenses:Utilities:Power" -actor "<who>"
```

## The loop

1. `bk init` once, to create the books in the current directory.
2. `bk import <source>` reads statements in, where a source is a CSV file, a ledger file, or a
   registered connector. Every input is deduped by fingerprint, so re-importing is safe.
3. `bk rules` authors the standing categorization; `bk categorize` asserts a single line when no rule
   fits. Prefer fixing the rule: a rule reclassifies the whole history at once, a per-line assertion
   corrects only that line.
4. `bk books` prints every line and where it posted (`-format json` for a machine,
   `-account Uncategorized` for just the lines the rules could not place).
5. `bk report` (income statement, balance sheet, and the `-gains` capital-gains schedule) and
   `bk receipt` render output. Both default to text; `-format html` prints. `bk report -basis
   accrual` reads through the accrual lens.
6. `bk export <connector>` writes booked lines out to a live system.

Other verbs, each its own fact: `bk invoice` and `bk bill` record value earned or incurred before
its cash; `bk void` undoes a bad line without deleting it; `bk match` forces or breaks a transfer
pairing the automatic fold got wrong; `bk policy` sets the cost-basis method (ACB or FIFO); `bk
connectors` registers a live source; `bk accounts`, `bk books`, and `bk docs` round it out.

## Correct by recording, never by editing

The ledger file and every report are read-only output. Do not edit them. To fix anything, record the
fact that supersedes it (a better rule, a re-categorization, a void) and regenerate. Nothing is ever
deleted: the mistake and its correction both survive in the log.

## Say only what is known

Do not guess. When the rules cannot place a line it stops at `Uncategorized`, which is the honest
answer, not a warning to silence. Never invent an amount, a date, a party, or a rate. A transfer, a
settlement, or a pairing that cannot be derived with certainty is asserted as its own fact on
purpose, so authority for it is recorded rather than assumed.

## Read more

The README is the reference. For the reasoning behind the procedure above, read its sections: "The
log is the book of record" (the fold-and-fact model and the full event list), "Every line posts, and
nothing is guessed" (rules and categorization), "Cash and accrual are one log read two ways",
"Transfers between your own accounts", and "Two tiers: rules, and whoever operates them".
