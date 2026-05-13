# Regtest plan

## 1. Current conclusion

Real `bitcoind -regtest` coverage is no longer purely theoretical.

Already demonstrated:

- Node startup / connectivity
- Miner wallet, mining, and funding flows
- Real `initial_utxos`
- Real `register` / `prove` inscription happy path
- Real mempool ordering checks

Still missing:

- `testmempoolaccept` policy checks as first-class tests
- Stronger reporting around `getrawtransaction` / `getrawmempool`
- Automated deep reorg scenarios

## 2. Existing scripts

- `scripts/start-regtest.sh`
- `scripts/stop-regtest.sh`
- `scripts/regtest-cli.sh`

Defaults:

- RPC URL: `http://127.0.0.1:19443`
- RPC user: `regtest`
- RPC password: `regtestpass`

## 3. What real tests already prove

### Happy path

- Register commit is accepted and confirmed by a real node
- `indexer_id` is backfilled correctly
- Matching blocks produce a `prove`
- `reveal` only broadcasts after prove commit confirms
- `reveal` is accepted and confirmed

### Mempool ordering

- `register` shows up in mempool after broadcast
- `prove` commit shows up in mempool after broadcast
- `reveal` does not race ahead
- After prove commit confirms, `reveal` appears in mempool

## 4. Execution order for the next milestones

### Step 1

Extend the RPC client with:

- `testmempoolaccept`
- `getrawmempool`
- `getrawtransaction`

### Step 2

Add observability-focused tests:

- Policy allows commit/reveal before broadcast
- Post-confirmation raw transactions are queryable

### Step 3

Add real reorg tests:

- Register confirmed then orphaned
- Prove confirmed then orphaned
- Reveal broadcast/confirmed then reorged

## 5. Acceptance criteria

### Minimum regtest bar

- Happy path stays green
- Mempool ordering stays green
- Test logs include txids, addresses, heights, and mempool snapshots

### Stronger regtest bar

- `testmempoolaccept` approves both commit and reveal templates
- `getrawtransaction` proves inclusion in a block
- Real reorgs flip messages to failed and roll back UTXOs as designed
