#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${RUN_DIR:-$ROOT_DIR/.tmp-regtest-e2e-$(date +%Y%m%d-%H%M%S)}"
RPC_SCRIPT="$ROOT_DIR/scripts/regtest-cli.sh"
FEE_SCRIPT="$ROOT_DIR/scripts/mock-fee-api.py"
STATE_SCRIPT="$ROOT_DIR/scripts/mock-state-api.py"
PUBLISHER_BIN="$RUN_DIR/publisher"
CONFIG_PATH="$RUN_DIR/config.regtest.json"
DB_PATH="$RUN_DIR/publisher.db"
RUN_LOG="$RUN_DIR/run.log"
FREEZE_TARGET_VERSION="${FREEZE_TARGET_VERSION:-539361536}"
PUBLISHER_PRIVKEY_HEX="${PUBLISHER_PRIVKEY_HEX:-00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff}"
PUBLISHER_ADDRESS="${PUBLISHER_ADDRESS:-bcrt1qj3w06t6hanknkgx28ghlrg6amyv0anxxa33w2w}"
PUBLISHER_SCRIPT_PUB_KEY="${PUBLISHER_SCRIPT_PUB_KEY:-0014945cfd2f57eced3b20ca3a2ff1a35dd918feccc6}"
PUBLISHER_ADDRESS_TYPE="${PUBLISHER_ADDRESS_TYPE:-p2wpkh}"
REGISTER_NAME="${REGISTER_NAME:-regtest-e2e}"
REGISTER_INDEX_RATIO_BP="${REGISTER_INDEX_RATIO_BP:-100}"
FUND_AMOUNT_BTC="${FUND_AMOUNT_BTC:-1.0}"
FEE_API_PORT="${FEE_API_PORT:-18081}"
STATE_API_PORT="${STATE_API_PORT:-18080}"
POLL_INTERVAL_NS="${POLL_INTERVAL_NS:-1000000000}"
REQUIRED_CONFIRMATIONS="${REQUIRED_CONFIRMATIONS:-1}"
MAX_REORG_DEPTH="${MAX_REORG_DEPTH:-6}"
SEND_CHANGE_MIN_VALUE="${SEND_CHANGE_MIN_VALUE:-546}"

FEE_PID=""
STATE_PID=""
PUBLISHER_PID=""
CLEANUP_DONE=0

mkdir -p "$RUN_DIR"

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" | tee -a "$RUN_LOG"
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    log "missing required command: $cmd"
    exit 1
  fi
}

rpc() {
  "$RPC_SCRIPT" "$@"
}

sqlite_query() {
  local query="$1"
  sqlite3 -cmd ".timeout 5000" "$DB_PATH" "$query"
}

cleanup() {
  local status="${1:-0}"

  set +e
  if [[ "$CLEANUP_DONE" -eq 1 ]]; then
    return 0
  fi
  CLEANUP_DONE=1

  stop_publisher
  terminate_pid "mock fee api" "$FEE_PID"
  terminate_pid "mock state api" "$STATE_PID"

  if [[ $status -ne 0 ]]; then
    log "run failed; inspect $RUN_DIR"
  fi
}

on_exit() {
  local status=$?
  cleanup "$status"
}

on_signal() {
  local signal="$1"
  local status="$2"
  log "received $signal, shutting down"
  exit "$status"
}

trap on_exit EXIT
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

terminate_pid() {
  local name="$1"
  local pid="$2"
  local signal
  local grace_checks=8

  if [[ -z "$pid" ]]; then
    return 0
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    wait "$pid" 2>/dev/null || true
    return 0
  fi

  for signal in TERM KILL; do
    kill "-$signal" "$pid" 2>/dev/null || true
    for _ in $(seq 1 "$grace_checks"); do
      if ! kill -0 "$pid" 2>/dev/null; then
        wait "$pid" 2>/dev/null || true
        return 0
      fi
      sleep 0.25
    done
  done

  log "$name pid=$pid did not exit cleanly"
  wait "$pid" 2>/dev/null || true
}

stop_publisher() {
  terminate_pid "publisher" "$PUBLISHER_PID"
  PUBLISHER_PID=""
}

start_publisher() {
  local phase="$1"
  local log_path="$RUN_DIR/$phase.publisher.log"

  stop_publisher

  log "starting publisher phase=$phase"
  PUBLISHER_CONFIG="$CONFIG_PATH" "$PUBLISHER_BIN" run >"$log_path" 2>&1 &
  PUBLISHER_PID=$!
  echo "$PUBLISHER_PID" >"$RUN_DIR/$phase.publisher.pid"
}

wait_for_sql_eq() {
  local description="$1"
  local query="$2"
  local expected="$3"
  local timeout_sec="$4"
  local phase="$5"
  local start_ts
  local current
  local phase_log="$RUN_DIR/$phase.publisher.log"

  start_ts="$(date +%s)"
  while true; do
    current="$(sqlite_query "$query" | tr -d '\r')"
    if [[ "$current" == "$expected" ]]; then
      log "condition satisfied: $description => $current"
      return 0
    fi

    if [[ -n "$PUBLISHER_PID" ]] && ! kill -0 "$PUBLISHER_PID" 2>/dev/null; then
      log "publisher phase=$phase exited before condition: $description"
      if [[ -f "$phase_log" ]]; then
        tail -n 40 "$phase_log" | sed 's/^/[publisher] /' | tee -a "$RUN_LOG"
      fi
      exit 1
    fi

    if (( $(date +%s) - start_ts >= timeout_sec )); then
      log "timeout waiting for condition: $description (current=$current expected=$expected)"
      if [[ -f "$phase_log" ]]; then
        tail -n 40 "$phase_log" | sed 's/^/[publisher] /' | tee -a "$RUN_LOG"
      fi
      exit 1
    fi
    sleep 1
  done
}

wait_for_sql_nonempty() {
  local description="$1"
  local query="$2"
  local timeout_sec="$3"
  local phase="$4"
  local start_ts
  local current
  local phase_log="$RUN_DIR/$phase.publisher.log"

  start_ts="$(date +%s)"
  while true; do
    current="$(sqlite_query "$query" | tr -d '\r')"
    if [[ -n "$current" ]]; then
      log "condition satisfied: $description => $current"
      return 0
    fi

    if [[ -n "$PUBLISHER_PID" ]] && ! kill -0 "$PUBLISHER_PID" 2>/dev/null; then
      log "publisher phase=$phase exited before condition: $description"
      if [[ -f "$phase_log" ]]; then
        tail -n 40 "$phase_log" | sed 's/^/[publisher] /' | tee -a "$RUN_LOG"
      fi
      exit 1
    fi

    if (( $(date +%s) - start_ts >= timeout_sec )); then
      log "timeout waiting for condition: $description"
      if [[ -f "$phase_log" ]]; then
        tail -n 40 "$phase_log" | sed 's/^/[publisher] /' | tee -a "$RUN_LOG"
      fi
      exit 1
    fi
    sleep 1
  done
}

snapshot() {
  local name="$1"

  sqlite_query "select key, value from chain_state order by key;" >"$RUN_DIR/$name.chain_state.txt"
  sqlite_query "select id, type, status, related_height, indexer_id, txid, confirm_height, reveal_txid, reveal_confirm_height, payload_text from messages order by id;" >"$RUN_DIR/$name.messages.txt"
  sqlite_query "select txid, vout, amount_sat, status, source, reserved_by_message_id, spent_by_txid, confirm_height from utxos order by txid, vout;" >"$RUN_DIR/$name.utxos.txt"
  rpc getrawmempool >"$RUN_DIR/$name.mempool.json"

  log "snapshot=$name"
  sed 's/^/[chain_state] /' "$RUN_DIR/$name.chain_state.txt" | tee -a "$RUN_LOG"
  sed 's/^/[messages] /' "$RUN_DIR/$name.messages.txt" | tee -a "$RUN_LOG"
  sed 's/^/[utxos] /' "$RUN_DIR/$name.utxos.txt" | tee -a "$RUN_LOG"
  sed 's/^/[mempool] /' "$RUN_DIR/$name.mempool.json" | tee -a "$RUN_LOG"
}

write_config() {
  local start_height="$1"
  local target_version="$2"
  local fund_txid="$3"
  local fund_vout="$4"
  local fund_amount_sat="$5"

  cat >"$CONFIG_PATH" <<EOF
{
  "bitcoin_rpc": {
    "url": "http://127.0.0.1:19443",
    "user": "regtest",
    "password": "regtestpass",
    "network": "regtest"
  },
  "signing": {
    "private_key_hex": "$PUBLISHER_PRIVKEY_HEX",
    "change_address": "$PUBLISHER_ADDRESS",
    "initial_utxos": [
      {
        "txid": "$fund_txid",
        "vout": $fund_vout,
        "amount_sat": $fund_amount_sat,
        "address": "$PUBLISHER_ADDRESS",
        "script_pub_key": "$PUBLISHER_SCRIPT_PUB_KEY",
        "address_type": "$PUBLISHER_ADDRESS_TYPE"
      }
    ]
  },
  "state_api": {
    "base_url": "http://127.0.0.1:$STATE_API_PORT",
    "timeout": 5000000000,
    "auth": ""
  },
  "fee_api": {
    "base_url": "http://127.0.0.1:$FEE_API_PORT",
    "timeout": 5000000000,
    "strategy": "half_hour",
    "min_fee_rate_sat_vb": 1,
    "max_fee_rate_sat_vb": 100
  },
  "register": {
    "index_ratio_bp": $REGISTER_INDEX_RATIO_BP,
    "reward_addr_type": "$PUBLISHER_ADDRESS_TYPE",
    "reward_addr": "$PUBLISHER_ADDRESS",
    "name": "$REGISTER_NAME"
  },
  "scan": {
    "start_height": $start_height,
    "poll_interval": $POLL_INTERVAL_NS,
    "target_block_version": $target_version,
    "required_confirmations": $REQUIRED_CONFIRMATIONS,
    "max_reorg_depth": $MAX_REORG_DEPTH
  },
  "tx": {
    "send_change_min_value": $SEND_CHANGE_MIN_VALUE
  },
  "database": {
    "sqlite_path": "$DB_PATH"
  },
  "runtime": {
    "dry_run": false,
    "disable_broadcast": false
  }
}
EOF
}

ensure_miner_wallet() {
  local wallets
  wallets="$(rpc listwallets)"
  if printf '%s' "$wallets" | jq -e '.[] | select(. == "miner")' >/dev/null 2>&1; then
    log "wallet miner already loaded"
    return 0
  fi

  if rpc loadwallet miner >/dev/null 2>&1; then
    log "wallet miner loaded"
    return 0
  fi

  rpc createwallet miner >/dev/null
  log "wallet miner created"
}

main() {
  local current_tip
  local miner_addr
  local fund_txid
  local fund_vout
  local fund_amount_sat
  local register_confirm_tip
  local register_confirm_hash
  local register_confirm_version
  local target_height
  local target_hash

  require_cmd jq
  require_cmd sqlite3
  require_cmd python3
  require_cmd go

  if ! rpc getblockcount >/dev/null 2>&1; then
    log "regtest rpc is not reachable via $RPC_SCRIPT"
    exit 1
  fi

  current_tip="$(rpc getblockcount)"
  log "connected to regtest tip=$current_tip"

  ensure_miner_wallet

  log "building publisher binary"
  GOCACHE="$ROOT_DIR/.gocache" go build -o "$PUBLISHER_BIN" "$ROOT_DIR/cmd/publisher"

  log "starting mock fee api on :$FEE_API_PORT"
  python3 "$FEE_SCRIPT" --port "$FEE_API_PORT" >"$RUN_DIR/mock-fee-api.log" 2>&1 &
  FEE_PID=$!
  sleep 1

  log "starting mock state api on :$STATE_API_PORT"
  python3 "$STATE_SCRIPT" --port "$STATE_API_PORT" >"$RUN_DIR/mock-state-api.log" 2>&1 &
  STATE_PID=$!
  sleep 1

  miner_addr="$(rpc -rpcwallet=miner getnewaddress '' bech32)"
  fund_txid="$(rpc -rpcwallet=miner sendtoaddress "$PUBLISHER_ADDRESS" "$FUND_AMOUNT_BTC")"
  rpc -rpcwallet=miner generatetoaddress 1 "$miner_addr" >/dev/null

  fund_vout="$(rpc getrawtransaction "$fund_txid" true | jq -r '.vout[] | select(.scriptPubKey.hex=="'"$PUBLISHER_SCRIPT_PUB_KEY"'") | .n')"
  fund_amount_sat="$(rpc getrawtransaction "$fund_txid" true | jq -r '.vout[] | select(.scriptPubKey.hex=="'"$PUBLISHER_SCRIPT_PUB_KEY"'") | (.value * 100000000 | floor)')"

  cat >"$RUN_DIR/funding.json" <<EOF
{
  "miner_address": "$miner_addr",
  "fund_txid": "$fund_txid",
  "fund_vout": $fund_vout,
  "fund_amount_sat": $fund_amount_sat
}
EOF
  log "funded publisher txid=$fund_txid vout=$fund_vout amount_sat=$fund_amount_sat"

  write_config 0 0 "$fund_txid" "$fund_vout" "$fund_amount_sat"

  log "running register mode"
  PUBLISHER_CONFIG="$CONFIG_PATH" "$PUBLISHER_BIN" register >"$RUN_DIR/register.publisher.log" 2>&1
  snapshot "after-register"

  miner_addr="$(rpc -rpcwallet=miner getnewaddress '' bech32)"
  rpc -rpcwallet=miner generatetoaddress 1 "$miner_addr" >/dev/null
  register_confirm_tip="$(rpc getblockcount)"
  register_confirm_hash="$(rpc getblockhash "$register_confirm_tip")"
  register_confirm_version="$(rpc getblockheader "$register_confirm_hash" true | jq -r '.version')"
  target_height="$register_confirm_tip"
  target_hash="$register_confirm_hash"

  cat >"$RUN_DIR/register-confirm.json" <<EOF
{
  "miner_address": "$miner_addr",
  "tip": $register_confirm_tip,
  "hash": "$register_confirm_hash",
  "version": $register_confirm_version
}
EOF
  log "register commit confirmed at height=$register_confirm_tip version=$register_confirm_version"

  write_config "$target_height" "$register_confirm_version" "$fund_txid" "$fund_vout" "$fund_amount_sat"
  start_publisher "phase1-create-prove"
  wait_for_sql_eq "prove row created" "select count(*) from messages where type='prove';" "1" 30 "phase1-create-prove"
  wait_for_sql_nonempty "indexer id populated" "select value from chain_state where key='indexer_id';" 30 "phase1-create-prove"
  wait_for_sql_eq "prove commit sent" "select status from messages where type='prove' order by id desc limit 1;" "commit_sent" 30 "phase1-create-prove"
  snapshot "after-prove-created"
  stop_publisher

  write_config "$target_height" "$FREEZE_TARGET_VERSION" "$fund_txid" "$fund_vout" "$fund_amount_sat"
  log "target version frozen to $FREEZE_TARGET_VERSION to stop new prove creation"

  start_publisher "phase2-confirm-commit"
  miner_addr="$(rpc -rpcwallet=miner getnewaddress '' bech32)"
  rpc -rpcwallet=miner generatetoaddress 1 "$miner_addr" >/dev/null
  wait_for_sql_eq "register done after reveal confirm" "select status from messages where type='register' order by id desc limit 1;" "done" 30 "phase2-confirm-commit"
  wait_for_sql_eq "prove reveal broadcasted" "select status from messages where type='prove' order by id desc limit 1;" "reveal_sent" 30 "phase2-confirm-commit"
  snapshot "after-prove-commit-confirm"

  miner_addr="$(rpc -rpcwallet=miner getnewaddress '' bech32)"
  rpc -rpcwallet=miner generatetoaddress 1 "$miner_addr" >/dev/null
  wait_for_sql_eq "prove done after reveal confirm" "select status from messages where type='prove' order by id desc limit 1;" "done" 30 "phase2-confirm-commit"
  wait_for_sql_nonempty "prove reveal confirm height" "select reveal_confirm_height from messages where type='prove' and reveal_confirm_height != 0 order by id desc limit 1;" 30 "phase2-confirm-commit"
  snapshot "final"
  stop_publisher

  cat >"$RUN_DIR/summary.txt" <<EOF
run_dir=$RUN_DIR
target_height=$target_height
target_hash=$target_hash
target_version_initial=$register_confirm_version
target_version_frozen=$FREEZE_TARGET_VERSION
fund_txid=$fund_txid
EOF

  log "regtest e2e completed successfully"
  log "artifacts saved under $RUN_DIR"
}

main "$@"
