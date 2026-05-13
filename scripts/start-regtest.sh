#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="${DATA_DIR:-$ROOT_DIR/.tmp-bitcoind}"
RPC_BIND="${RPC_BIND:-127.0.0.1}"
RPC_PORT="${RPC_PORT:-19443}"
RPC_USER="${RPC_USER:-regtest}"
RPC_PASS="${RPC_PASS:-regtestpass}"
BITCOIND_BIN="${BITCOIND_BIN:-$(command -v bitcoind || true)}"
PID_FILE="$DATA_DIR/bitcoind.pid"
LOG_FILE="$DATA_DIR/regtest/debug.log"

if [[ -z "$BITCOIND_BIN" ]]; then
  echo "bitcoind not found. Set BITCOIND_BIN or add bitcoind to PATH." >&2
  exit 1
fi

mkdir -p "$DATA_DIR"

cat >"$DATA_DIR/bitcoin.conf" <<EOF
server=1
daemon=1
txindex=1
regtest=1
fallbackfee=0.0002
[regtest]
rpcbind=$RPC_BIND
rpcallowip=127.0.0.1
rpcuser=$RPC_USER
rpcpassword=$RPC_PASS
rpcport=$RPC_PORT
EOF

if [[ -f "$PID_FILE" ]]; then
  OLD_PID="$(cat "$PID_FILE" || true)"
  if [[ -n "$OLD_PID" ]] && kill -0 "$OLD_PID" 2>/dev/null; then
    echo "bitcoind already running with pid $OLD_PID"
    echo "RPC: http://$RPC_BIND:$RPC_PORT"
    exit 0
  fi
  rm -f "$PID_FILE"
fi

"$BITCOIND_BIN" -datadir="$DATA_DIR" -nosettings

for _ in $(seq 1 40); do
  if [[ -f "$PID_FILE" ]]; then
    break
  fi
  sleep 0.25
done

if [[ ! -f "$PID_FILE" ]]; then
  echo "bitcoind started but pid file was not created. Check log: $LOG_FILE" >&2
  exit 1
fi

PID="$(cat "$PID_FILE")"

cat <<EOF
bitcoind regtest started

PID: $PID
Data dir: $DATA_DIR
RPC URL: http://$RPC_BIND:$RPC_PORT
RPC user: $RPC_USER
RPC pass: $RPC_PASS
Log: $LOG_FILE

Export for this project:
  export REGTEST_RPC_URL=http://$RPC_BIND:$RPC_PORT
  export REGTEST_RPC_USER=$RPC_USER
  export REGTEST_RPC_PASS=$RPC_PASS

Stop:
  bitcoin-cli -regtest -datadir="$DATA_DIR" -rpcuser="$RPC_USER" -rpcpassword="$RPC_PASS" stop
EOF
