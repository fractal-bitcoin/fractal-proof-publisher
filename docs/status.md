# Current status

## 1. Where the project stands

The code has moved past “protocol skeleton + stub-only tests” into a stage where a **real regtest happy path** for proofs is demonstrated.

This is not a paper design or an implementation that only works against fake RPC. Today it includes:

- Local private-key signing
- Inscription commit/reveal construction
- SQLite persistence
- `register` / `prove` submission flows
- Basic recovery, confirmation handling, and reorg lookback
- Real `bitcoind -regtest` happy-path validation
- Real mempool ordering checks

## 2. Important problems already addressed

### Reveal racing ahead

Older recovery paths could incorrectly advance `reveal` too early.

Now:

- `signed` reveals are not broadcast like ordinary follow-up messages
- `reveal` is only broadcast after the parent **commit** confirms
- Rows with a confirmed commit but no reveal broadcast are driven forward by `ProgressOnce()`

### Misleading `dry_run`

Previously `dry_run` skipped polling but still had side effects.

Now:

- `RunMode()` returns immediately at the entrypoint when `dry_run` is enabled
- No DB open, no scan, no broadcast, no state writes in dry-run mode

### Reorg lookback not applied

`MaxReorgDepth` was not fully wired through.

Now:

- Scanning and confirmation use a lookback window
- Heights inside the recent window are re-evaluated when the chain changes

### Opaque state API errors

404/HTML responses often surfaced as JSON decode failures.

Now:

- Non-`200 OK` responses surface as explicit HTTP errors
- Logs distinguish “mock coverage gap” from “upstream outage” more clearly

### Change marked spendable too early

Change could become `available` right after the commit was only signed.

Now:

- Change starts as `pending`
- It becomes `available` only after the **commit** confirms

### Reveal script rejected on real nodes

Real nodes could reject script-path spends.

Now:

- Reveal script includes the correct signature checks
- Real `bitcoind -regtest` accepts and confirms reveal transactions

## 3. Important gaps

### P0

- Automated tests for **real** deep reorgs are still incomplete
- Finer-grained retry / compensation semantics for failed reveals are still incomplete

### P1

- `testmempoolaccept` is not yet a fixed part of the test/report story
- `getrawtransaction` / `getrawmempool` observability is still thin
- The historical `messages + reveal helper fields` model still wants consolidation
- Compatibility names like `RecoverOnce()` / `ConfirmOnce()` still add noise

### P2

- A few naming and documentation mismatches from older iterations remain

## 4. Risks to watch

The largest remaining risks are no longer “reveal races the mempool” or “dry-run accidentally broadcasts”, but:

1. Behavior under **real** reorgs is not fully covered by automation
2. The submission state machine is still dragged down by the older `messages + reveal_*` representation
3. Compensation when `reveal` fails is still relatively weak
4. Legacy entrypoints can mislead readers into modeling two parallel advancement systems

## 5. Suggested next steps

1. Keep the public narrative anchored on **`register` / `prove` inscription publisher`**
2. Continue tightening the submission state machine and storage model
3. Add real reorg tests and harden recovery details last
