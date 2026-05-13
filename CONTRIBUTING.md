# Contributing

## Development

Use Go 1.25 or newer.

```bash
go test ./...
go vet ./...
```

Regtest validation requires `bitcoind`, `bitcoin-cli`, Python 3, and `sqlite3`.
See `docs/regtest.md` and `docs/testing.md`.

## Pull Requests

- Keep changes scoped to one behavior or cleanup.
- Add or update tests for transaction building, state transitions, persistence,
  or recovery changes.
- Do not commit local configs, keys, runtime databases, logs, or generated
  regtest directories.
- Document user-visible configuration or behavior changes in `README.md` or
  `docs/`.

