# Overview

## 1. Main narrative

Treat this repository as a **publisher for `register` / `prove` inscriptions**.

The mental model is closer to “a slightly stronger batch mint-style inscription CLI” than a generic on-chain messaging platform:

- Read configuration
- Spend controlled UTXOs
- Build raw transactions
- Broadcast via RPC or print `bitcoin-cli sendrawtransaction ...`
- Persist the minimum state needed to avoid duplicate submissions and to survive restarts

There are only two **business** responsibilities:

- Publish a `register` inscription to obtain an `indexer_id`
- Publish a `prove` inscription to submit a state proof for eligible block heights

`reveal` is **not** a third business message type. It is the second execution step of the inscription **commit → reveal** pipeline.

## 2. Current scope

The project deliberately stays within these boundaries:

- Local private-key signing; no dependence on `bitcoind` wallet
- Bitcoin RPC for chain reads and raw transaction broadcast
- External state API for `blockhash` / `statehash`
- Mempool.space-style fee API for fee rates
- SQLite for deduplication, UTXO bookkeeping, and recovery state

Out of scope for now:

- `ratio` flows
- Multi-account / multi-actor / multi-signer operation
- Automatically discovering external UTXOs
- Full production monitoring and alerting
- A generic future-proof on-chain messaging platform

## 3. Implementation status

Already in place:

- FIP101 text encoding
- Inscription envelope and taproot **commit + reveal** construction
- Local signing and RPC broadcast
- SQLite persistence and UTXO lifecycle
- Real regtest happy path for `register` and `prove`
- `reveal` only broadcast after the parent **commit** confirms
- Basic recovery, lookback, and limited reorg handling

Historical complexity that may still show up in code and schema:

- The primary persisted object is still named `messages`, not the clearer `submissions`
- Commit and reveal phases are still represented as auxiliary columns on one row rather than a single staged state machine
- `RecoverOnce()` / `ConfirmOnce()` remain as compatibility aliases and can mislead readers into thinking there are two separate advancement systems
- Older top-level notes may describe superseded status names; **treat `docs/` plus the code as the source of truth**

These are implementation leftovers and should not define the product story going forward.

## 4. Suggested reading order

1. `docs/overview.md` (this file)
2. `docs/usage.md`
3. `docs/repo-map.md`
4. `docs/architecture.md`
5. `docs/workflows.md`
6. `docs/status.md`
7. `docs/testing.md`
8. `docs/core-redesign.md` (historical rationale for simplifying the model; does not override facts in the code)

## 5. Relationship to protocol docs

- `docs/protocol.md` — external FIP101-oriented reference (terminology and rules)
- `docs/core-redesign.md` — why the codebase may still converge toward a slimmer `submission` model; read as design history, not as a spec of current behavior
- For responsibilities, boundaries, and what is actually shipped today, prefer the other `docs/` files and the implementation
