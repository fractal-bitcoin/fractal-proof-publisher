# Architecture

## 1. System shape

This is a single-process Go daemon. Each polling cycle roughly:

1. Reads the latest chain height and blocks
2. Re-scans a recent window for potential reorgs
3. Identifies blocks that match the target version and confirmation depth
4. Builds `prove` submissions for eligible heights
5. Broadcasts **commit** transactions
6. After commit confirms, broadcasts the matching **reveal**
7. Maintains submission state, per-height chain metadata, and UTXO state

The story should read as:

- The system publishes `register` inscriptions
- The system publishes `prove` inscriptions
- `reveal` is an execution stage of the inscription pipeline

## 2. Run modes

Entry lives in `internal/app/app.go`.

Primary modes:

- `run`
- `register`

`RunMode()` ordering (simplified):

1. If `runtime.dry_run=true`, return immediately (no side effects)
2. Open SQLite
3. Seed initial UTXOs from config
4. Load private key material
5. Initialize RPC / state API / fee API clients
6. Run `ProgressOnce()`
7. Run `ensureRegistered()`
8. Run `ScanOnce()`
9. Run `ProgressOnce()` again
10. Enter the polling loop

Notes:

- There is no separate `bootstrap-register` mode in code today
- There is no standalone `Reconcile()` phase
- `ensureRegistered()` consults `chain_state.indexer_id` and recent `register` rows to decide whether a registration is still required
- `RecoverOnce()` / `ConfirmOnce()` remain mostly as compatibility aliases for tests and manual stepping; the live loop is centered on `ProgressOnce()`

## 3. External dependencies

### Bitcoin RPC

Used for:

- `getblockcount`
- `getblockhash`
- `getblockheader`
- `getblock`
- `sendrawtransaction`
- Additional helpers needed by regtest integration tests

### State API

Inputs:

- `blockhash`
- `statehash`

Derived value:

- `prove_hash = RIPEMD160(SHA256(blockhash_bytes || statehash_bytes))`

Operational constraints:

- `ScanOnce()` walks `MaxReorgDepth` into the past
- The state API must therefore cover that lookback window
- Non-`200 OK` responses are surfaced as explicit HTTP errors (not JSON parse noise)

### Fee API

Provides a recommended feerate, optionally clipped by local min/max strategy before fee estimation for commit/reveal.

## 4. Business model

### `register`

Text shape:

```text
fip101,v1,register,<index_ratio_bp>,<reward_addr_type>,<reward_addr>,<name>
```

Purpose:

- Register an indexer
- After confirmation, `indexer_id = <height>:<txindex>` is derived from the confirming block

### `prove`

Text shape:

```text
fip101,v1,prove,<indexer_id>,<prove_height>,<prove_hash>
```

Purpose:

- Submit a state proof for a target height

### `reveal`

- Not a separate business payload type
- Not a separate plaintext message
- Only the second on-chain transaction of the inscription **commit + reveal** pair

## 5. Transaction model

### Two-step structure

The system uses the standard inscription commit/reveal pattern.

#### Commit transaction

- Spends controlled UTXOs
- Creates the taproot commit output
- May include a change output

#### Reveal transaction

- Spends the commit output
- Reveals the inscription via script path
- Sends postage to the reveal recipient

### Funding plan

Important ordering:

- Estimate reveal fee first
- Size the commit output to cover `reveal_postage + reveal_fee`
- Change only graduates from `pending` to `available` after the parent **commit** confirms

## 6. Reveal script essentials

The reveal script matches what real nodes expect for script-path spends:

- Push x-only pubkey
- `OP_CHECKSIG`
- Continue into the inscription envelope segment

This fixes the class of failures that looked like:

```text
Stack size must be exactly one after execution
```

## 7. Persistence model

Core tables / concepts today:

- `messages`
- `chain_blocks`
- `utxos`
- `chain_state`
- `broadcast_attempts`

Important clarifications:

- Each `messages` row represents one `register` or `prove` **submission**
- `reveal` is not its own row; it is represented via embedded columns such as `reveal_txid`, `reveal_raw_tx_hex`, `reveal_broadcast_at`, `reveal_confirm_height`
- `utxos` stores both configured seed UTXOs and change outputs produced by commits
- `broadcast_attempts` records commit/reveal broadcast failures
- This describes **how the code stores data today**, not necessarily the end state envisioned in `docs/core-redesign.md`

Conceptually, the cleaner core object is a `register`/`prove` **submission**; see `docs/core-redesign.md` for the consolidation direction.

## 8. Reorg and recovery design

### Done

- `MaxReorgDepth` participates in `ScanOnce()` lookback
- `ProgressOnce()` applies the lookback window even when `related_height=0`
- `reveal` never broadcasts before its parent commit confirms
- If commit is confirmed but reveal is not yet sent, the next `ProgressOnce()` continues the pipeline

### Still tightening

- Automated proof for realistic reorgs on regtest/signet
- Further simplification of the submission orchestration
- Recovery semantics that stay powerful without turning into a mini workflow engine
