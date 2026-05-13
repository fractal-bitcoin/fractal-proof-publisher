# Testing

## 1. Goals

Tests are not only about line coverage. They should demonstrate:

1. Stable protocol encoding
2. Correct transaction construction and signing
3. A healthy state machine on the nominal path
4. Correct recovery / reorg / UTXO behavior on adversarial paths
5. Acceptance by real `bitcoind -regtest` and expected mempool ordering

## 2. Layers

### Unit tests

Focus:

- `internal/protocol`
- `internal/inscription`
- `internal/keys`
- `internal/txbuilder`
- `internal/store`

Already exercised:

- FIP101 text encoding
- `prove_hash`
- Reveal script chunking
- Taproot material derivations
- `p2wpkh` / `p2tr` signing
- Funding / coin selection
- SQLite basics

### Service tests

Focus:

- `BuildAndSign`
- `ProgressOnce`
- `ScanOnce`
- Reorg rollback paths

Already exercised:

- Reveal does not broadcast early
- Recovery does not broadcast reveal before commit confirms
- Recovery can finish reveal after commit is confirmed
- `register` confirmation fills `indexer_id`
- Prove commit confirmation triggers reveal
- Reorg fails messages and rolls back UTXOs as expected
- `dry_run` has no side effects

### Stub integration

Focus:

- RPC shape compatibility
- End-to-end workflow regressions with doubles

### Real regtest

The most important integration layer today.

Already demonstrated:

- Real funding and `initial_utxos` extraction
- Real `register` / `prove` inscription happy path
- Real script-path reveal spends accepted by the node
- Real mempool ordering assertions

Still missing:

- `testmempoolaccept` as a fixed gate
- Stronger `getrawtransaction` / `getrawmempool` reporting
- Real automated reorg suites

## 3. Key real-regtest tests

### `TestManagedRegtestFixtureSeedsRealInitialUTXO`

Shows:

- Connectivity to a real regtest node
- Funding the publisher address
- Extracting `initial_utxos` from an on-chain transaction

### `TestManagedRegtestRegisterProveRevealHappyPath`

Shows:

- `register` commit broadcasts and confirms
- `indexer_id` is backfilled
- Target-version blocks produce `prove`
- Prove commit must confirm before `reveal` broadcasts
- `reveal` is accepted and confirmed
- Change UTXO lifecycle behaves as expected

### `TestManagedRegtestMempoolSequence`

Shows:

- `register` transaction is visible in mempool after broadcast
- `prove` commit is visible in mempool after broadcast
- `reveal` never appears early
- After prove commit confirms, `reveal` appears in mempool

## 4. Reporting expectations

Real regtest runs should log more than pass/fail:

- RPC endpoint
- Initial funding UTXO
- Relevant addresses
- `register` / `prove` / `reveal` txids
- Target height / hash / version
- `indexer_id`
- Critical prove inputs
- Mempool snapshots at key milestones
- Final change UTXO status

`integration/regtest_external_test.go` already trends in this direction.

Additionally, `scripts/regtest-e2e.sh` chains mocks, registration, block generation, mempool observation, reveal confirmation, and log export into one repeatable regression script.

## 5. Suggested commands

Full suite:

```bash
GOCACHE=$(pwd)/.gocache go test ./...
```

Focused real regtest slice:

```bash
gofmt -w integration/regtest_external_test.go && GOCACHE=$(pwd)/.gocache go test ./integration -run 'TestManagedRegtest(RegisterProveRevealHappyPath|MempoolSequence)' -v
```

## 6. Next high-priority tests

1. `testmempoolaccept` for commit/reveal templates
2. `getrawtransaction` inclusion proofs after mining
3. Real reorg closure
4. Reveal failure + compensation paths
