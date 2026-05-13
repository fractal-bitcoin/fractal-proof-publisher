# Security Policy

This project builds and signs Bitcoin transactions locally. Treat every
configuration file as sensitive when it contains private keys, RPC credentials,
API tokens, funded UTXOs, or production endpoints.

## Reporting a Vulnerability

Please do not open a public issue for vulnerabilities that could expose funds,
credentials, or infrastructure. Report privately to the project maintainers
through the security contact configured on the GitHub repository.

Include:

- Affected version or commit
- Reproduction steps
- Expected and actual behavior
- Any logs or transaction IDs needed to understand the issue

## Operational Guidance

- Do not commit `config.json`, wallet keys, API keys, RPC credentials, SQLite
  runtime databases, or generated logs.
- Test with regtest before using funded mainnet UTXOs.
- Use a dedicated signing key and only fund it with the amount needed for the
  publisher.
- Review transaction hex and fee settings before enabling production broadcast.

