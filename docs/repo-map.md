# Repo map

## 1. Where to start reading code

Fastest path to understanding:

1. `cmd/publisher/main.go`
2. `internal/app/app.go`
3. `internal/service/workflow.go`
4. `internal/service/messages.go`
5. `internal/service/engine.go`
6. `internal/service/confirmer.go`
7. `internal/store/sqlite.go`
8. `internal/txbuilder/builder.go`
9. `internal/inscription/envelope.go`

## 2. Directory responsibilities

### `cmd/`

- `cmd/publisher/main.go` — CLI entry and mode dispatch

### `internal/app/`

- Wiring layer: load config, open SQLite, construct clients, assemble services
- `RunMode()` is the daemon entrypoint

### `internal/service/`

Primary business logic.

Important files:

- `workflow.go` — high-level actions such as `RunRegister()` / `RunProve()`
- `messages.go` — inscription commit/reveal construction, signing, persistence, change reservation
- `engine.go` — scanning, reorg lookback, and `RecoverOnce()` compatibility shims
- `confirmer.go` — `ProgressOnce()` advances commit/reveal stages; still exposes `ConfirmOnce()` for compatibility

Reality check:

- The shipped responsibility is still a `register` / `prove` inscription publisher
- `reveal_*` columns reflect an implementation that has not fully converged on a single `submission` state machine yet

### `internal/store/`

- SQLite schema and queries
- Persists messages, blocks, UTXOs, chain singletons, broadcast attempts
- Consolidation direction: see `docs/core-redesign.md`

### `internal/txbuilder/`

- Coin selection
- Commit/reveal funding planning
- Signing and finalization
- Reveal witness construction

### `internal/inscription/`

- Inscription envelope assembly
- Reveal script and taproot leaf / control block / commit output derivation

### `internal/protocol/`

- FIP101 text encoding
- `prove_hash` computation

### `internal/bitcoinrpc/`

- Bitcoin Core JSON-RPC wrapper (blocks + broadcast + helpers used in tests)

### `internal/stateapi/` and `internal/feeapi/`

- HTTP clients for external services

### `integration/`

- `regtest_test.go` — stub-style integration coverage
- `regtest_external_test.go` — real `bitcoind -regtest` coverage

### `scripts/`

- `start-regtest.sh`, `stop-regtest.sh`, `regtest-cli.sh` — local regtest lifecycle helpers

## 3. Primary data flow

1. `app` loads config and keys
2. `workflow` kicks off register/prove creation
3. `messages` builds and signs inscription transactions and writes rows
4. The daemon loop runs `ProgressOnce()`, `ensureRegistered()`, `ScanOnce()`, then `ProgressOnce()` again
5. `engine` scans new blocks and reconciles the lookback window
6. `confirmer` advances commit/reveal, fills `indexer_id`, activates change
7. `store` persists every transition

## 4. Highest-signal tests

- `internal/service/messages_test.go`
- `internal/service/confirmer_test.go`
- `internal/service/reorg_test.go`
- `integration/regtest_external_test.go`

To answer “what does this actually prove on-chain today?”, start with `integration/regtest_external_test.go`.
