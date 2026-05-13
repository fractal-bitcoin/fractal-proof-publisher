# Usage

This document answers one question: **how to actually run this repository today**.

- If you only need to know whether the code compiles and the CLI is sane, start with **Sanity check**.
- If you want to drive the daemon manually on regtest and inspect SQLite, follow **Manual regtest**.
- If you plan mainnet, read **Mainnet rollout** first; several production guardrails are still operator-owned.
- If you want a single scripted replay, use **Automation script**.

**Headless / sandboxed environments:** real validation expects `bitcoind` and local HTTP mocks on `127.0.0.1`. In some agent sandboxes localhost is unreachable even when it works on the host. Run the same `./scripts/regtest-cli.sh getblockcount` from a normal host shell before trusting sandbox results.

On Windows PowerShell, set config path like: `$env:PUBLISHER_CONFIG=".\config.regtest.json"` then `go run ./cmd/publisher run`.

## 1. What this program does today

Single-process Go daemon:

- Local private-key signing
- Broadcasts raw transactions via Bitcoin Core RPC
- If `indexer_id` is missing locally, sends `register` once
- Scans for blocks matching `scan.target_block_version` and `scan.required_confirmations`, then submits `prove`
- Each logical submission uses inscription **commit + reveal**
- SQLite stores messages, blocks, UTXOs, and chain singletons

CLI modes:

- `run` (default)
- `register`

Entry:

```bash
go run ./cmd/publisher
```

Equivalent to:

```bash
go run ./cmd/publisher run
```

Register only:

```bash
go run ./cmd/publisher register
```

## 2. Semantics you should memorize first

### `dry_run`

- When `runtime.dry_run=true`, `RunMode()` returns immediately at the top.
- It does **not** open the database, scan, build transactions, or write state.
- This is **not** a preview mode; it is “exit immediately”.

### `disable_broadcast`

- When `runtime.disable_broadcast=true`, transactions are still built and signed.
- RPC broadcast is skipped, but local state may still advance as if broadcast happened, and inputs can be marked spent locally.
- Use only with a **throwaway** SQLite file for isolated drills — never against a production database.

### `register` confirmation semantics

- After **`register` commit** confirms, the code writes `indexer_id`.
- You do **not** need to wait for `register` reveal confirmation before `prove` can start.

### `prove` / `reveal` advancement

- After `prove` **commit** is broadcast, `reveal` is **not** broadcast immediately.
- Only after `prove` **commit** confirms does `ProgressOnce()` broadcast `reveal`.
- If the process restarts after commit confirms but before reveal sends, the next `ProgressOnce()` resumes reveal.

### Legacy names

- `RecoverOnce()` / `ConfirmOnce()` still exist for tests/manual stepping.
- The live loop is centered on `ProgressOnce()`.
- Read logs and code as **one** state machine, not parallel “recover” vs “confirm” systems.

## 3. Configuration

Configuration path: environment variable `PUBLISHER_CONFIG` pointing at a JSON file; if unset, the program reads `config.json` in the current working directory.

**Important:** `time.Duration` fields in JSON are **nanoseconds as integers**, not strings like `"5s"`.

Examples:

- `5 * time.Second` → `5000000000`
- `3 * time.Second` → `3000000000`

Field names and semantics are defined in code (`internal/config/config.go`). Summary:

### `bitcoin_rpc`

- `url`, `user`, `password`, `network`

### `signing`

- `private_key_wif` **or** `private_key_hex`
- `change_address`
- `initial_utxos`

### `state_api`

- `base_url`, `timeout`, `auth`, `provider`

Default request shape:

- `GET {base_url}/{height}`

JSON body:

```json
{
  "blockhash": "64-char-hex",
  "statehash": "64-char-hex"
}
```

Notes:

- The API must cover the lookback implied by `scan.max_reorg_depth`.
- Missing heights surface as HTTP errors (not JSON parse failures).

If you use `query-fip101`, set:

- `state_api.provider = "query-fip101"`

Then requests become:

- `GET {base_url}/brc20/statehash?start={height}&end={height}`

…and `blockhash` / `statehash` are read from `data.detail[0]`.

### `fee_api`

- `base_url`, `timeout`, `strategy`
- `min_fee_rate_sat_vb`, `max_fee_rate_sat_vb`
- `fixed_fee_rate_sat_vb`

Default request:

- `GET {base_url}/api/v1/fees/recommended`

Example body:

```json
{
  "fastestFee": 9,
  "halfHourFee": 5,
  "hourFee": 3,
  "minimumFee": 1
}
```

If `fixed_fee_rate_sat_vb > 0`, the client skips HTTP and uses that feerate.

To reuse an existing indexer without sending a new `register`:

- set `register.indexer_id` (non-empty) — it is written into `chain_state` and auto-register is skipped.

### `register`

- `index_ratio_bp`, `reward_addr_type`, `reward_addr`, `name`, `indexer_id`

### `scan`

- `start_height`, `poll_interval`, `target_block_version`, `required_confirmations`, `max_reorg_depth`

### `tx`

- `send_change_min_value`

### `database`

- `sqlite_path`

### `runtime`

- `dry_run`, `disable_broadcast`

## 4. Example regtest config

Minimal template for manual demos. Replace `initial_utxos` with a real funding output from your node.

**Important:**

- `target_block_version` must be a **decimal** integer in JSON.
- `initial_utxos` must match keys you actually control.

```json
{
  "bitcoin_rpc": {
    "url": "http://127.0.0.1:19443",
    "user": "regtest",
    "password": "regtestpass",
    "network": "regtest"
  },
  "signing": {
    "private_key_hex": "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
    "change_address": "bcrt1qj3w06t6hanknkgx28ghlrg6amyv0anxxa33w2w",
    "initial_utxos": [
      {
        "txid": "REPLACE_WITH_FUNDING_TXID",
        "vout": 0,
        "amount_sat": 100000000,
        "address": "bcrt1qj3w06t6hanknkgx28ghlrg6amyv0anxxa33w2w",
        "script_pub_key": "0014945cfd2f57eced3b20ca3a2ff1a35dd918feccc6",
        "address_type": "p2wpkh"
      }
    ]
  },
  "state_api": {
    "base_url": "http://127.0.0.1:18080",
    "timeout": 5000000000,
    "auth": "",
    "provider": ""
  },
  "fee_api": {
    "base_url": "http://127.0.0.1:18081",
    "timeout": 5000000000,
    "strategy": "half_hour",
    "min_fee_rate_sat_vb": 1,
    "max_fee_rate_sat_vb": 100,
    "fixed_fee_rate_sat_vb": 0
  },
  "register": {
    "index_ratio_bp": 100,
    "reward_addr_type": "p2wpkh",
    "reward_addr": "bcrt1qj3w06t6hanknkgx28ghlrg6amyv0anxxa33w2w",
    "name": "regtest-indexer",
    "indexer_id": ""
  },
  "scan": {
    "start_height": 0,
    "poll_interval": 3000000000,
    "target_block_version": 536870912,
    "required_confirmations": 1,
    "max_reorg_depth": 6
  },
  "tx": {
    "send_change_min_value": 546
  },
  "database": {
    "sqlite_path": "./publisher-regtest.db"
  },
  "runtime": {
    "dry_run": false,
    "disable_broadcast": false
  }
}
```

The sample key material matches tests:

- private key hex: `00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff`
- regtest address: `bcrt1qj3w06t6hanknkgx28ghlrg6amyv0anxxa33w2w`
- scriptPubKey hex: `0014945cfd2f57eced3b20ca3a2ff1a35dd918feccc6`

## 4.1 Docker

The repo ships a minimal skeleton:

- `Dockerfile`
- `docker-compose.yaml`
- `config.example.json`

Compose conventions:

- Real config at repo root: `config.json`
- SQLite/runtime data under `./data`
- `PUBLISHER_CONFIG=/app/config/config.json` inside the container
- Default access to host services via `host.docker.internal`

```bash
docker compose build
docker compose up -d
docker compose logs -f publisher
```

## 5. Sanity check

This branch is still easiest to validate with **compile + manual regtest**; integration tests may lag refactors.

### 5.1 Build the CLI

```bash
GOCACHE=$(pwd)/.gocache go build ./cmd/publisher
```

### 5.2 About tests

There are:

- `integration/regtest_external_test.go`
- `integration/regtest_test.go`

Until the suite is fully green on your machine, prefer:

- `go build ./cmd/publisher` as a hard gate
- Manual regtest steps in this document
- `docs/testing.md` and `docs/regtest.md` when fixing tests

## 6. Manual regtest

### 6.1 Start `bitcoind` regtest

```bash
./scripts/start-regtest.sh
```

Defaults:

- RPC `http://127.0.0.1:19443`
- user `regtest`, password `regtestpass`

Stop:

```bash
./scripts/stop-regtest.sh
```

### 6.2 Create miner wallet and coins

```bash
./scripts/regtest-cli.sh createwallet miner
MINER_ADDR=$(./scripts/regtest-cli.sh -rpcwallet=miner getnewaddress "" bech32)
./scripts/regtest-cli.sh -rpcwallet=miner generatetoaddress 101 "$MINER_ADDR"
```

### 6.3 Fund the publisher address

Publisher address used in docs/tests:

- `bcrt1qj3w06t6hanknkgx28ghlrg6amyv0anxxa33w2w`

Send 1 BTC:

```bash
PUB_ADDR=bcrt1qj3w06t6hanknkgx28ghlrg6amyv0anxxa33w2w
FUND_TXID=$(./scripts/regtest-cli.sh -rpcwallet=miner sendtoaddress "$PUB_ADDR" 1.0)
./scripts/regtest-cli.sh -rpcwallet=miner generatetoaddress 1 "$MINER_ADDR"
echo "$FUND_TXID"
```

### 6.4 Build `initial_utxos`

Example scriptPubKey:

- `0014945cfd2f57eced3b20ca3a2ff1a35dd918feccc6`

Inspect the funding tx:

```bash
./scripts/regtest-cli.sh getrawtransaction "$FUND_TXID" true
```

With `jq`:

```bash
SCRIPT=0014945cfd2f57eced3b20ca3a2ff1a35dd918feccc6
VOUT=$(./scripts/regtest-cli.sh getrawtransaction "$FUND_TXID" true | jq -r '.vout[] | select(.scriptPubKey.hex=="'"$SCRIPT"'") | .n')
AMOUNT_SAT=$(./scripts/regtest-cli.sh getrawtransaction "$FUND_TXID" true | jq -r '.vout[] | select(.scriptPubKey.hex=="'"$SCRIPT"'") | (.value * 100000000 | floor)')
echo "vout=$VOUT amount_sat=$AMOUNT_SAT"
```

Copy `FUND_TXID`, `VOUT`, `AMOUNT_SAT` into `config.regtest.json`.

### 6.5 Pick `target_block_version`

Easiest path: match whatever version your regtest node actually mines (not crafting custom version bits unless you know how).

```bash
TIP=$(./scripts/regtest-cli.sh getblockcount)
TIP_HASH=$(./scripts/regtest-cli.sh getblockhash "$TIP")
./scripts/regtest-cli.sh getblockheader "$TIP_HASH" true
```

With `jq`:

```bash
TARGET_VERSION=$(./scripts/regtest-cli.sh getblockheader "$TIP_HASH" true | jq -r '.version')
echo "$TARGET_VERSION"
```

Put that decimal into `scan.target_block_version`.

Note: some deployments target `0x20260100` → decimal `539361536`.

### 6.6 Mock fee API

```bash
python3 scripts/mock-fee-api.py --port 18081
```

### 6.7 Mock state API

```bash
python3 scripts/mock-state-api.py --port 18080
```

Deterministic fake data per height:

- `blockhash = sha256("mock-blockhash:<height>")`
- `statehash = sha256("mock-statehash:<height>")`

Optional overrides:

```bash
python3 scripts/mock-state-api.py --port 18080 --data ./state-map.json
```

```json
{
  "123": {
    "blockhash": "64-char-hex",
    "statehash": "64-char-hex"
  }
}
```

### 6.8 Write config

Recommended filename: `config.regtest.json` — copy the example JSON above, replace `initial_utxos`, set `target_block_version`.

### 6.9 Send `register` alone

```bash
PUBLISHER_CONFIG=./config.regtest.json go run ./cmd/publisher register
```

This constructs and broadcasts **`register` commit** only (no polling loop).

### 6.10 Confirm `register` commit, then run daemon

Mine one block:

```bash
./scripts/regtest-cli.sh -rpcwallet=miner generatetoaddress 1 "$MINER_ADDR"
```

Run:

```bash
PUBLISHER_CONFIG=./config.regtest.json go run ./cmd/publisher run
```

After commit confirms, SQLite should contain `chain_state.indexer_id`.

Inspect:

```bash
sqlite3 publisher-regtest.db 'select key, value from chain_state order by key;'
sqlite3 publisher-regtest.db 'select id, type, status, related_height, indexer_id, txid, confirm_height, reveal_txid, reveal_confirm_height from messages order by id;'
```

### 6.11 Target height / state API

With default mock, mining a new block usually suffices; the mock answers every numeric height.

For fixed overrides, write `state-map.json` as shown earlier.

### 6.12 Confirm `prove` commit

When the daemon finds an eligible block it inserts `prove` and broadcasts **prove commit**.

Check:

```bash
sqlite3 publisher-regtest.db 'select id, type, status, related_height, indexer_id, txid, confirm_height, reveal_txid, reveal_confirm_height from messages order by id;'
./scripts/regtest-cli.sh getrawmempool
```

Mine one block to confirm prove commit; the daemon should then broadcast `reveal`.

### 6.13 Confirm `reveal`

Mine another block:

```bash
./scripts/regtest-cli.sh -rpcwallet=miner generatetoaddress 1 "$MINER_ADDR"
```

Verify:

```bash
sqlite3 publisher-regtest.db 'select id, type, status, related_height, indexer_id, txid, confirm_height, reveal_txid, reveal_confirm_height from messages order by id;'
sqlite3 publisher-regtest.db 'select txid, vout, amount_sat, status, source, confirm_height from utxos order by created_at;'
```

Expect `register` and `prove` rows, `prove.reveal_confirm_height` set, and change UTXO `available`.

## 7. Automation script

```bash
./scripts/regtest-e2e.sh
```

High level:

1. Talks to your existing regtest node
2. Ensures `miner` wallet
3. Builds `cmd/publisher`
4. Starts mock fee/state APIs
5. Funds the fixed publisher address
6. Runs `register`, mines, runs publisher phases to complete one `prove` line
7. Freezes `target_block_version` so the daemon stops minting endless proves
8. Collects SQLite + mempool snapshots under `.tmp-regtest-e2e-*`

Prerequisites: running regtest `bitcoind`, `jq`, `sqlite3`, `python3`, `go`, free ports `18080`/`18081`.

Success markers:

- `run.log` contains `regtest e2e completed successfully`
- `final.messages.txt` includes `register|done` and `prove|done`
- Initial funding UTXO ends `spent_confirmed`, prove change ends `available`

If you see `scan paused at height=...`, verify the mock on `18080` is the one the script started (`curl -i http://127.0.0.1:18080/<height>`).

## 8. Validated runbook (single-path)

This is the same chain as manual regtest, but with explicit checkpoints.

### 8.1 Goal

Controlled verification (not long-running continuous proves):

1. `register` commit broadcasts
2. After `register` commit confirms → `indexer_id` exists
3. `register` reveal broadcasts
4. `prove` commit broadcasts
5. After `prove` commit confirms → `prove` reveal broadcasts
6. `prove` reveal confirms
7. Change UTXO returns `available`

### 8.2 Why continuous proves are awkward

If `target_block_version` matches every newly mined regtest block, the daemon will keep creating proves. For teaching/debugging, prefer:

1. `register` alone → mine → `run` until `indexer_id` + `register` reveal progress
2. Optionally adjust mocks
3. After the first `prove` is created (`commit_sent`), set `target_block_version` to a **non-matching** value and restart so the daemon only finishes confirmations/reveals

### 8.3 Useful queries

```bash
./scripts/regtest-cli.sh getblockcount
./scripts/regtest-cli.sh getrawmempool
sqlite3 publisher-regtest.db 'select id, type, status, related_height, indexer_id, txid, confirm_height, reveal_txid, reveal_confirm_height from messages order by id;'
sqlite3 publisher-regtest.db 'select key, value from chain_state order by key;'
sqlite3 publisher-regtest.db 'select txid, vout, amount_sat, status, source, reserved_by_message_id, spent_by_txid, confirm_height from utxos order by txid, vout;'
```

### 8.4 Checkpoints

**A — After `register` command**

Expect: one `register` row (`commit_sent`), commit txid populated, reveal txid populated in DB, funding input `spent_pending`, change `pending`, commit visible in mempool, **no** `register` reveal in mempool yet.

**B — After first block + first `run`**

Expect: `indexer_id` written, `last_scanned_height` advanced, `register` moves toward `reveal_sent`, reveal tx visible in mempool.

**C — State API lag**

If the state API is not ready for a height yet, logs should resemble:

```text
scan paused at height=<n>: state api not ready yet, will retry next loop: ...
```

The process should **retry**, not exit. Check `curl -i http://127.0.0.1:18080/<height>`.

**D — Lookback requests older heights**

`max_reorg_depth` makes `ScanOnce()` revisit a window, not only `last_scanned_height + 1`. If you use `--data` overrides, ensure **all** heights in that window that are still eligible still return data.

**E — After prove creation**

Expect new `prove` row: `commit_sent`, `related_height` set, `indexer_id` matches `chain_state`, reveal tx id recorded but reveal not in mempool yet; prove commit visible in mempool.

**F — After block confirming prove commit (often with `target_block_version` already tweaked)**

Expect: `register` may be `done`, `prove` at `reveal_sent`, commit height set, reveal in mempool, `reveal_confirm_height` still zero.

**G — After next block**

Expect: `prove` is `done`, `reveal_confirm_height` set, mempool clean for that reveal, change UTXO `available`.

### 8.5 Stop creating more proves on regtest

1. Start with the live regtest version as `target_block_version`
2. Wait until first `prove` hits `commit_sent`
3. Change `target_block_version` to a value that **does not** match mined blocks (example decimal `539361536` if your node mines `536870912`)
4. Restart `run` and only advance confirmations

### 8.6 Example payload

```text
fip101,v1,prove,4584:2,4584,b7372020fddac99731927031e87a0398350a6cb1
```

### 8.7 Suggested order for automated agents

1. `go build ./cmd/publisher`
2. `./scripts/regtest-cli.sh getblockcount` from a host shell
3. Use a fresh `sqlite_path`
4. Use an isolated `state-map.json` when customizing API responses
5. After each step inspect `messages`, `chain_state`, `utxos`, `getrawmempool`
6. On `scan paused...`, verify the state API readiness first

## 9. Troubleshooting

**Process exits immediately** — check `runtime.dry_run`.

**No `indexer_id` after register** — ensure `register` commit mined; daemon ran `ScanOnce()`; `messages` has `txid`.

**No `prove`** — needs `indexer_id`; `target_block_version` must match actual mined version; confirmations satisfied; state API returns data for heights inside lookback.

**No `reveal`** — prove commit must confirm first; check `reveal_txid` and mempool.

**DB looks “broadcast” but chain does not** — you probably used `disable_broadcast=true`; start from a new SQLite file.

## 10. Mainnet rollout

Mainnet is not “flip `network` to `mainnet` and go”. The code can talk to mainnet, but **you** own risk, fees, key hygiene, and monitoring.

### 10.1 Preconditions

- Bitcoin Core RPC with `getblockcount` / `getblockhash` / `getblockheader` / `getblock` / `sendrawtransaction`
- Reliable state API (`blockhash` / `statehash`)
- Reliable fee API **or** `fixed_fee_rate_sat_vb`
- Keys you control + matching `initial_utxos`
- Dedicated `sqlite_path`

Runtime does **not** require wallet/index/txindex on the node, but operating a pruned or flaky remote node is still risky.

### 10.2 Config deltas vs regtest

- `bitcoin_rpc.network = mainnet` and real RPC URL/auth
- `state_api.base_url` real; add `state_api.provider = "query-fip101"` if applicable
- `register.indexer_id` if reusing an indexer
- `fee_api` real URL or fixed feerate
- Real signing material and UTXOs
- Real `scan.target_block_version`
- Persistent `database.sqlite_path`

### 10.3 Recommended rollout order

1. Manual regtest pass (this doc)
2. Isolated dry run with **new** SQLite + tiny funds
3. Micro-UTXO on mainnet
4. `register` alone first
5. Long-running `run` only after `indexer_id` looks correct

### 10.4 Do not

- Reuse production DB after `disable_broadcast=true` drills
- Configure `initial_utxos` that do not belong to your configured private key
- Blind-scan from ancient `start_height` without understanding load
- Omit working fee/state endpoints — startup paths depend on them

### 10.5 Known residual risks

- Deep reorg automation still incomplete
- Reveal failure recovery still basic
- No packaged monitoring stack
- `messages` + `reveal_*` naming still reflects older modeling

For first mainnet use: tiny dedicated UTXO, isolated datadir, watch SQLite + mempool + RPC errors closely.

## 11. Related files

- `cmd/publisher/main.go`
- `internal/app/app.go`
- `internal/service/workflow.go`
- `internal/service/messages.go`
- `internal/service/engine.go`
- `internal/service/confirmer.go`
- `scripts/start-regtest.sh`
- `scripts/regtest-cli.sh`
- `scripts/regtest-e2e.sh`
- `scripts/mock-fee-api.py`
- `scripts/mock-state-api.py`
- `docs/regtest.md`
- `docs/testing.md`
