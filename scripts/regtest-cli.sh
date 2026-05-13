#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="${DATA_DIR:-$ROOT_DIR/.tmp-bitcoind}"
RPC_USER="${RPC_USER:-regtest}"
RPC_PASS="${RPC_PASS:-regtestpass}"
RPC_PORT="${RPC_PORT:-19443}"
RPC_HOST="${RPC_HOST:-127.0.0.1}"
BITCOINCLI_BIN="${BITCOINCLI_BIN:-$(command -v bitcoin-cli || true)}"

if [[ -z "$BITCOINCLI_BIN" ]]; then
  echo "bitcoin-cli not found. Set BITCOINCLI_BIN or add bitcoin-cli to PATH." >&2
  exit 1
fi

exec "$BITCOINCLI_BIN" \
  -regtest \
  -datadir="$DATA_DIR" \
  -rpcconnect="$RPC_HOST" \
  -rpcport="$RPC_PORT" \
  -rpcuser="$RPC_USER" \
  -rpcpassword="$RPC_PASS" \
  "$@"
