# Fractal Proof Publisher

Fractal Proof Publisher is a single-process Go publisher that submits two business message types on Bitcoin:

- `register` (register an indexer)
- `prove` (submit a state proof for a target block height)

Each business message is published through the inscription two-phase flow: `commit -> reveal`.
The program uses:

- Bitcoin Core JSON-RPC (chain reads + transaction broadcast)
- State HTTP API (fetch `blockhash` / `statehash` by height)
- Fee API (optional)
- SQLite (local state, UTXOs, message progress)

## Project Status

This project is experimental. Review the code, configuration, generated
transactions, and fee settings carefully before using funded mainnet UTXOs.

Do not commit private keys, RPC credentials, API keys, `config.json`, runtime
databases, or logs.

## Quick Start

### 1. Prepare config

- Copy `config.example.json` to your own config file (for example, `config.json`)
- Set `PUBLISHER_CONFIG` to that file path

PowerShell example:

```powershell
$env:PUBLISHER_CONFIG = ".\config.json"
```

If `PUBLISHER_CONFIG` is not set, the program reads `config.json` from the current directory.

### 2. Build

```bash
go build -o publisher ./cmd/publisher
```

### 3. Run

```bash
# Equivalent to run mode
./publisher

# Explicit run mode (daemon loop)
./publisher run

# Submit register once, then exit
./publisher register
```

## Runtime Behavior

### `run` mode main loop

Each loop iteration does:

1. `ProgressOnce()` to advance pending messages
2. `ensureRegistered()` to auto-create the first register if local `indexer_id` is missing
3. `ScanOnce()` to scan blocks and create prove messages when eligible
4. `ProgressOnce()` again

Polling interval comes from `scan.poll_interval`; if `<= 0`, it falls back to `30s`.

### `register` mode

- Creates and advances one register submission
- Returns the register **commit txid** on success and exits
- Does not enter the daemon loop

## Runtime Mode `runtime.mode`

`runtime.mode` controls UTXO source, broadcast backend, and confirmation semantics.

### Default mode (`""` / `default` / any other string)

- UTXOs: from `signing.initial_utxos` (at least one)
- Broadcast: `bitcoin_rpc.sendrawtransaction`
- Confirmation: commit/reveal confirmed via local Bitcoin RPC

### `unisat_open_api` mode

- Required: `runtime.unisat_open_api_url`, `runtime.unisat_open_api_key`
- `signing.initial_utxos` may be empty
- UTXO source: `GET /v1/indexer/address/{change_address}/available-utxo-data`
- Broadcast: `POST /v1/indexer/local_pushtx`
- Reveal confirmation: check tx visibility via `GET /v1/indexer/tx/{reveal_txid}`

Note: block scanning still depends on `bitcoin_rpc` even in UniSat mode.

## Critical Config Validation Rules

Startup fails if any of these are not satisfied:

- `bitcoin_rpc.url` is required
- `bitcoin_rpc.network` is required
- At least one of `signing.private_key_wif` or `signing.private_key_hex` is required
- In non-UniSat mode, `signing.initial_utxos` must contain at least one item
- `state_api.base_url` is required
- If `fee_api.fixed_fee_rate_sat_vb <= 0`, `fee_api.base_url` is required
- `database.sqlite_path` is required
- `scan.required_confirmations > 0`

## Config Field Index

### `bitcoin_rpc`

- `url`, `user`, `password`, `network`

### `signing`

- `private_key_wif` or `private_key_hex`
- `change_address`
- `initial_utxos[]`: `txid`, `vout`, `amount_sat`, `address`, `script_pub_key`, `address_type`

### `state_api`

- `base_url`, `timeout`, `auth`, `provider`
- If `provider="query-fip101"`, request path:
  - `GET {base_url}/brc20/statehash?start={height}&end={height}`
- Otherwise:
  - `GET {base_url}/{height}`

### `fee_api`

- `base_url`, `timeout`, `strategy`
- `min_fee_rate_sat_vb`, `max_fee_rate_sat_vb`, `fixed_fee_rate_sat_vb`

### `register`

- `index_ratio_bp`, `reward_addr_type`, `reward_addr`, `name`, `indexer_id`
- If `register.indexer_id` is non-empty, it is written into local state at startup and auto-register is skipped

### `scan`

- `start_height`, `poll_interval`, `target_block_version`
- `required_confirmations`, `max_reorg_depth`

### `tx`

- `send_change_min_value`

### `database`

- `sqlite_path`

### `runtime`

- `mode`
- `unisat_open_api_url`, `unisat_open_api_key`
- `dry_run`
- `disable_broadcast`
- `health_addr`

## Duration Format in JSON

All `time.Duration` fields in JSON must be integers in nanoseconds, not strings like `"5s"`.

Examples:

- `5s` -> `5000000000`
- `30s` -> `30000000000`

## Health Endpoints

When `runtime.health_addr` is non-empty, the program starts an HTTP server:

- `GET /healthz` -> `ok`
- `GET /status` -> JSON (pending/done/failed counts, `indexer_id`, scan heights, etc.)

## On-Chain Text Format

- Register:
  - `fip101,1,register_indexer,<index_ratio_bp>,<reward_addr>,<name>`
- Prove:
  - `fip101,1,submit_proof,<indexer_id>,<prove_height>,<prove_hash_hex>`

`prove_hash` computation:

```text
payload = normalize(indexer_id) + ":" + normalize(blockhash) + ":" + normalize(statehash)
prove_hash = 64-char lowercase hex of sha256(payload)
```

`normalize` means trim spaces and convert to lowercase.

## Related Docs

For detailed workflows and testing:

- `docs/overview.md`
- `docs/usage.md`
- `docs/architecture.md`
- `docs/workflows.md`
- `docs/regtest.md`
- `docs/testing.md`
- `docs/repo-map.md`

## Note

If this README differs from runtime behavior, the current code is the source of truth.
