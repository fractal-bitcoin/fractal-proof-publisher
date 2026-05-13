## 1. Purpose

This document is an **external protocol reference**. It is not the authoritative definition of this repository’s internal responsibilities, nor a precise snapshot of every implementation detail in the current code.

Use it as follows:

- When you need FIP101 field names, terminology, or validation rules, refer here.
- When you need this publisher’s scope, system design, and what is actually implemented today, prefer `docs/overview.md`, `docs/architecture.md`, `docs/workflows.md`, and `docs/status.md`.

This project’s responsibilities are focused on:

- Publishing a `register` inscription
- Publishing a `prove` inscription

In this codebase, `reveal` is the **second execution phase** of an inscription submission (commit → reveal), not a third independent on-chain “business action”.

## 3. Protocol overview

### 3.1 On-chain message carrier

The chain expects exactly one valid FIP101 inscription in the transaction:

- Content type: `plain/text` (or `text/plain` in practice; see implementation)
- Protocol: `fip101`
- Actor identity: derived from the x-only pubkey in the inscription’s tap script, mapped to an actor address

Product and integration docs typically describe this as “the user submits an FIP101 inscription transaction”.

### 3.2 Supported operation types

The wider protocol defines several actions. **This repository implements** the flows needed for:

| Action   | Tag                 | Purpose                          |
| -------- | ------------------- | -------------------------------- |
| register | `FIP_101_REGISTER`  | Register an indexer              |
| ratio    | `FIP_101_ALLOCAT_RATIO` | Update indexer/staker split (not implemented as a publisher action here) |
| prove    | `FIP_101_PROVE_STAKE`   | Submit a proof for a given height |

### 3.3 Indexer ID rules

The indexer ID is **not** a user-chosen string. It is derived from where the registration transaction lands:

```text
indexer_id = <registration_block_height>:<registration_tx_index>
```

Example: `123456:7`

Downstream flows (`stake`, `ratio`, `prove`, `claim`, etc.) reference this ID.

## 4. On-chain interaction design

### 4.1 Register indexer

The `register` operation’s on-chain payload (in this project: CSV-like text, not CBOR) carries at least:

| Field              | Description |
| ------------------ | ----------- |
| `index_ratio_bp`   | Operator share in basis points, range 0–10000 |
| `reward_addr_type` | Reward address type; commonly `p2tr` or `p2wpkh` |
| `reward_addr`      | Operator reward address, must match the type and be valid for the network |
| `name`             | Indexer name, up to 64 characters |

After confirmation, important derived state includes:

- `indexer_id`
- reward address
- index ratio
- register txid
- registered block height
- name

Notes:

- The registration **owner** is the inscription actor address, not the reward address.
- Later `ratio` / `prove` actions are authorized by the indexer owner at registration time; the reward address is only where rewards are sent.

### 4.2 Update split ratio (`ratio`)

The `ratio` operation requires:

- `indexer_id`
- a new `index_ratio_bp`
- submission by the indexer owner

Semantic meaning (protocol-level):

- `index_ratio` is the share of the indexer’s first-layer rewards that goes to the operator reward address; the remainder is distributed among stakers under that indexer by stake weight.

Example:

- `index_ratio = 0.15` means that when the indexer receives 100 units of first-layer rewards, 15 go to the operator reward address and 85 are split among staker addresses by weight.

### 4.5 Submit proof (`prove`)

The `prove` operation requires:

| Field          | Description |
| -------------- | ----------- |
| `indexer_id`   | Indexer submitting the proof |
| `prove_height` | Block height being proven |
| `prove_hash`   | Hash160, hex-encoded (40 characters) |

Validation rules (protocol intent):

- Must be submitted by that indexer’s owner
- `prove_height` must be present
- `prove_hash` must be valid 40-character hex
- Outside mempool-only scenarios, `prove_height` must not exceed the current chain height

### 5.2 Proof validity (ecosystem semantics)

Implementations may use a `standard_indexer_id` baseline to filter proofs for a height:

- proof window: `proof_window` (default 144 blocks)
- per indexer, only the **latest** proof within the window is considered
- older proofs for the same indexer may be marked duplicate invalid
- if the standard indexer has not published a proof yet, other proofs may stay pending
- if proof hash matches the standard indexer → valid
- if it does not match → invalid hash

This is **not** majority voting; it is comparison against a designated standard indexer.
