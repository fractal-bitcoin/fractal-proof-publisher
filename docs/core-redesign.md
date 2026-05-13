# Core redesign (design history)

## 1. Narrowing the goals

The narrowed goals for this daemon are:

1. Continuously follow the Bitcoin chain
2. Submit proofs only for blocks whose `version` matches the configured target (for example `0x20260100`) **and** that have at least one confirmation
3. Use **inscriptions** as the on-chain carrier, not bare `OP_RETURN` payloads
4. Sign locally and broadcast raw transactions via RPC, without relying on the node wallet
5. Use a local database for deduplication, UTXO management, and restart-safe recovery

From that, the practical shipping scope is:

- A `register` / `prove` inscription publisher
- A single-signer, single-indexer daemon
- On startup, ensure a `register` completes once
- Then continuously submit `prove` for eligible heights
- Each logical submission is one business object; underneath it is still `commit + reveal`, but the business layer should see **one submission**

## 1.1 Baseline: smallest working mint-style CLI

A better baseline than “generic message engine” is the smallest CLI that already works in practice:

- Config names an initial UTXO
- Config names receive/change addresses
- Locally build a raw transaction
- Print `bitcoin-cli sendrawtransaction <rawtx>` for the operator to run
- Operators batch those commands to broadcast

That baseline already proves:

- An inscription-style publisher does not need to become a platform first

This repository should be read as a **controlled extension** of that baseline, not a clean-slate abstract system.

## 1.2 Only add what the baseline forces

From that CLI baseline, the real deltas are small:

1. Turn a one-shot tool into a chain-following daemon
2. Turn manual batching into automatic `prove` triggers for matching blocks
3. Turn a single seed UTXO into a small locally maintained UTXO pool
4. Turn “print CLI commands” into “optionally call `sendrawtransaction` directly”
5. Turn a stateless script into persisted height/UTXO/confirmation state with restart recovery
6. Turn a toy inscription into the protocol-required `register` / `prove` inscription format

Beyond that list, avoid adding extra “systems”.

## 1.3 What should not grow out of the baseline

These are not required extensions of the baseline:

- Generic message bus abstractions
- Pre-wired hooks for future protocol verbs unrelated to shipping goals
- Modeling `reveal` as its own business message type
- Many auxiliary tables purely for audit theater
- Empty service layers kept “for later”

Rule of thumb:

- If a capability is not **forced** while evolving the simple CLI into a `register` / `prove` publisher, defer it

Explicitly out of core scope:

- `ratio` or other future protocol actions
- Multi-account / multi-indexer / multi-signer operation
- Generic message engines
- Report-grade failure archives
- Premature “extensible workflow platform” designs

## 2. Proposed target shape

### 2.1 Principles

Keep exactly two business verbs:

- `register`
- `prove`

Keep exactly one business aggregate:

- `submission`

`reveal` is not a business verb; it is stage-two execution detail inside a `submission`.

Other principles:

- Default mental model: “small publisher that builds and sends raw txs”
- Persist state only where restart safety / dedupe / UTXOs require it
- Add a scan loop only where automatic triggering requires it
- Do not platformize ahead of need

### 2.2 Minimal runtime loop

The main loop should be only four steps:

1. `recoverPendingSubmissions()`
2. `ensureRegistered()`
3. `scanEligibleBlocks()`
4. `submitPendingProves()`

Meaning:

- Step 1 covers “signed but not broadcast”, “broadcast but not confirmed”, and “commit confirmed but reveal not sent”
- Step 2 only cares whether `indexer_id` exists
- Step 3 finds heights that are eligible and not yet submitted
- Step 4 builds inscriptions, selects UTXOs, signs, and broadcasts

This collapses the older interleaving of `Reconcile / Recover / Confirm / Scan / Bootstrap`.

### 2.3 Core schema (target)

Converge on three conceptual tables.

#### `submissions`

One row is one end-to-end submission. Suggested columns:

- `id`
- `kind` — `register | prove`
- `target_height` — empty for `register`, proven height for `prove`
- `payload_text`
- `indexer_id`
- `prove_block_hash`, `prove_state_hash`, `prove_hash`
- `commit_txid`, `commit_raw_tx_hex`, `commit_confirm_height`
- `reveal_txid`, `reveal_raw_tx_hex`, `reveal_confirm_height`
- `state`, `failure_reason`
- `created_at`, `updated_at`

Suggested `state` values:

- `building`, `commit_signed`, `commit_sent`, `commit_confirmed`, `reveal_sent`, `done`, `failed`

#### `utxos`

Keep the table, but simplify statuses toward:

- `available`, `locked`, `spent`, `change_pending`, `invalid`

#### `kv_state`

Singleton keys such as:

- `indexer_id`
- `last_scanned_height`
- `last_seen_tip`

#### Optional: `recent_blocks`

If you need bounded reorg lookback, store only the last `N` heights:

- `height`, `block_hash`, `version`, `confirmations`

This table should only support lookback, not general business state.

### 2.4 Flows

#### Register

1. Read `kv_state.indexer_id` on startup
2. If empty, check for an in-flight `register` submission; create one if needed
3. Build inscription commit/reveal
4. Broadcast commit
5. After commit confirms, derive `indexer_id` from `(height, txindex)`
6. Broadcast reveal
7. After reveal confirms, mark submission `done`

Optional ergonomics (matches the CLI baseline):

- `build-only` — print `bitcoin-cli sendrawtransaction ...`
- `broadcast` — call RPC directly

#### Prove

1. Scan for matching version + confirmations
2. For each height, skip if a `prove` submission already exists
3. Pull `blockhash` / `statehash` from the state API
4. Compute `prove_hash`
5. Create and broadcast the prove submission’s commit
6. After commit confirms, broadcast reveal
7. After reveal confirms, mark `done`

### 2.5 Recovery

Recovery should not be two parallel systems (“recover messages” vs “confirm messages”). It should be:

- `commit_signed` → broadcast if missing
- `commit_sent` → confirm commit
- `commit_confirmed` → broadcast reveal if missing
- `reveal_sent` → confirm reveal

Recovery then matches normal operation; no special parent/child coordination.

### 2.6 Reorgs

Reorgs only need **bounded self-healing**, not a workflow platform.

Suggested rules:

1. Each loop, revisit the last `max_reorg_depth` heights
2. If a stored `block_hash` changes, mark related submissions `failed`
3. Unlock UTXOs that never finalized spends
4. Invalidate untrusted change outputs
5. Let the scanner recreate `prove` submissions on the new best chain

Goal: recover safely, not build compensating orchestration machinery.

## 3. Over-design in the current implementation

These choices increase cognitive load, synchronization cost, and maintenance surface.

### 3.1 Splitting one submission into parent + child messages

In [`internal/store/sqlite.go`](../internal/store/sqlite.go), the `messages` table historically carried both:

- `register` / `prove` business rows
- `reveal` transport rows linked via `parent_message_id`

Problems:

- One business action becomes two rows
- Recovery must special-case “reveal is not another business message”
- Confirmation paths must mirror `txid/raw_tx/state` across parent/child
- The store accrues duplicated “parent columns + child columns” write paths

`reveal` belongs inside a `submission` state machine, not as a sibling business message.

### 3.2 `proof_inputs` as a separate table

[`internal/service/messages.go`](../internal/service/messages.go) and [`internal/store/sqlite.go`](../internal/store/sqlite.go) may hang prove snapshots off `proof_inputs` keyed by `message_id`.

For current needs, those inputs are **part of the prove submission itself**.

Separate tables add:

- Extra upserts and joins
- Extra write paths on broadcast/confirm
- Harder reads for “full prove context”

Unless you have a concrete audit/query requirement, keep this data on the submission row.

### 3.3 Premature “generic message engine” types

[`internal/model/types.go`](../internal/model/types.go) may still carry:

- `MessageTypeRatio`
- wide `BlockStatus` / `MessageStatus` enums

The narrowed requirement only needs:

- `register`
- `prove`
- inscription two-phase execution

Future-proof enums tend to make the code optimize for hypothetical features instead of today’s publisher.

### 3.4 Empty service files

Empty files under `internal/service/` suggest boundaries that do not exist. Delete or merge them; do not keep structural decoration.

### 3.5 Over-complicated orchestration

Historically [`internal/app/app.go`](../internal/app/app.go) interleaved:

- `Reconcile`
- `RecoverOnce`
- bootstrap register
- another `RecoverOnce`
- `ScanOnce`

…and repeated similar patterns in the loop.

When state is too fragmented, correctness depends on call order instead of a single submission state machine.

### 3.6 Config surface area

[`internal/config/config.go`](../internal/config/config.go) may expose knobs that are not first-class product concerns yet, for example:

- `signing.sender_addr_types`
- `tx.dust_limit`
- `tx.inscription_content_type`
- `runtime.backfill_batch_size`

Some never closed the loop to behavior; others are constants that should not be “product configuration”.

### 3.7 `broadcast_attempts` is not automatically P0

Failure history is useful, but early on structured logs plus a last-error column are often enough.

Maintaining [`broadcast_attempts`](../internal/store/sqlite.go) adds schema, write paths, and test obligations without a crisp consumer.

## 4. What is *not* over-design

Keep these; they are forced by the problem:

- Inscription `commit + reveal`
- Local signing
- SQLite persistence
- Local UTXO management
- Startup recovery
- Bounded reorg lookback
- Fee API integration
- State API integration for prove inputs

From the “small CLI” perspective these are **necessary upgrades**, not a different product category.

## 5. Suggested remediation order

### Phase 1 — model

1. Replace `message + reveal child + proof_inputs` with `submission`
2. Remove `MessageTypeReveal` as a business concept
3. Merge reveal columns into the submission row
4. Unify `register` / `prove` advancement into one state machine

### Phase 2 — execution

1. Collapse `RecoverOnce + ConfirmOnce + bootstrapRegister` responsibilities
2. Make the outer loop the four-step version described above
3. Delete empty services

### Phase 3 — schema and config

1. Drop config keys that never completed a behavior loop
2. Re-evaluate `broadcast_attempts`
3. Ensure `chain_blocks` only serves reorg lookback

## 6. One-line takeaway

The main issue is not “the chain math is wrong”; it is that a **single-indexer inscription submitter** was shaped too early like an **extensible messaging platform**.

The fix is not “more tables”, it is a narrower core model:

- one submission
- two business actions
- one sequential state machine
