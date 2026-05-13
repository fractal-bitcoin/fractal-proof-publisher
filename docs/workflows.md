# Workflows

## 1. Summary

The hardest part is not “how to stitch one transaction”, but how two inscription types keep moving forward:

1. `register`
2. `prove`

`reveal` is **not** a third business action. It is always the second execution step of a single inscription submission.

## 2. Register workflow

### Creation

Started from `workflow.RunRegister()`.

Steps:

1. Build the `register` text payload
2. Create the inscription envelope
3. Build commit and reveal plans
4. Select input UTXOs
5. Sign the commit transaction
6. Finalize and sign the reveal witness
7. Persist `register` submission state
8. Broadcast the **commit**

### Confirmation

After `ProgressOnce()` confirms the `register` **commit**:

- Advance to `commit_confirmed`
- Backfill `indexer_id` from `(block height, tx index)` inside the confirming block
- Update `chain_state.indexer_id`
- Activate change UTXO tied to that commit
- Then advance toward `reveal_sent` in the same or a later loop iteration

## 3. Prove workflow

### Scan trigger

`ScanOnce()` walks a lookback window and inspects candidate blocks.

A height becomes eligible when:

- Block version matches `scan.target_block_version`
- It has at least `scan.required_confirmations`
- No successful `prove` exists yet for that height
- `indexer_id` is already known locally

### Creation

Steps:

1. Call the state API for that height’s `blockhash` / `statehash`
2. Compute `prove_hash`
3. Encode the prove text payload
4. Reuse the same inscription commit/reveal path as `register`
5. Persist `prove` submission state
6. Broadcast the prove **commit**

### Confirmation

After `ProgressOnce()` confirms the prove **commit**:

- The prove submission reaches commit-confirmed state
- Trigger **reveal** broadcast
- Promote that commit’s change UTXO from `pending` to `available`

## 4. Reveal phase

### When it is prepared

`reveal` raw transactions are prepared during commit construction, but they are **not** broadcast immediately.

### Broadcast rules

- `reveal` is **never** broadcast before the parent commit confirms
- Right after commit confirms, `ProgressOnce()` attempts to broadcast `reveal`
- If the process dies between commit confirmation and reveal broadcast, the next `ProgressOnce()` resumes

### After reveal confirms

The inscription submission as a whole is considered complete.

## 5. UTXO lifecycle

### Input UTXO (commit inputs)

Typical path:

```text
available -> reserved -> spent_pending -> spent_confirmed
```

### Change UTXO

Current model:

```text
pending -> available
```

If a parent commit is orphaned/reorged out, change is marked `invalid` and must not be reused.

## 6. Recovery and restart

### `ProgressOnce`

The canonical advancement path:

- `building -> commit_signed`
- `commit_signed -> commit_sent`
- `commit_sent -> commit_confirmed`
- `commit_confirmed -> reveal_sent`
- `reveal_sent -> done`

Compatibility note:

- `RecoverOnce()` / `ConfirmOnce()` still exist as thin wrappers around `ProgressOnce()`
- The daemon loop effectively runs `ProgressOnce()` plus `ensureRegistered()`, `ScanOnce()`, and another `ProgressOnce()` pass

## 7. Reorg handling

### Implemented

- Track block hashes inside a recent window
- If a historical hash changes, mark affected messages failed and roll back related UTXOs / change outputs

### Still needs stronger automated proof

- `register` confirmed then reorged
- `prove` confirmed then reorged
- `reveal` broadcast or confirmed then reorged

## 8. Regtest timing already validated

Real tests have shown:

- `register` enters the mempool after broadcast
- `prove` commit enters the mempool after broadcast
- `reveal` does **not** appear in the mempool while the prove commit is still unconfirmed
- After prove commit confirms, `reveal` enters the mempool
- `reveal` is accepted and confirmed by a real node
