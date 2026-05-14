package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA busy_timeout=5000;`,
		`PRAGMA foreign_keys=ON;`,
	}
	for _, stmt := range pragmas {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec sqlite pragma: %w", err)
		}
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS chain_blocks (
			height INTEGER PRIMARY KEY,
			block_hash TEXT NOT NULL,
			version INTEGER NOT NULL,
			confirmations INTEGER NOT NULL DEFAULT 0,
			eligible INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			payload_text TEXT NOT NULL,
			related_height INTEGER,
			indexer_id TEXT,
			txid TEXT,
			raw_tx_hex TEXT,
			broadcast_at TEXT,
			confirm_height INTEGER,
			failure_reason TEXT,
			parent_message_id INTEGER,
			reveal_txid TEXT,
			reveal_raw_tx_hex TEXT,
			reveal_broadcast_at TEXT,
			reveal_confirm_height INTEGER,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS utxos (
			txid TEXT NOT NULL,
			vout INTEGER NOT NULL,
			amount_sat INTEGER NOT NULL,
			address TEXT NOT NULL,
			script_pub_key TEXT NOT NULL,
			address_type TEXT NOT NULL,
			status TEXT NOT NULL,
			source TEXT NOT NULL,
			reserved_by_message_id INTEGER,
			reserved_at TEXT,
			spent_by_txid TEXT,
			confirm_height INTEGER NOT NULL DEFAULT 0,
			last_seen_height INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (txid, vout)
		);`,
		`CREATE TABLE IF NOT EXISTS chain_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS broadcast_attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id INTEGER NOT NULL,
			phase TEXT NOT NULL DEFAULT 'commit',
			attempted_at TEXT NOT NULL,
			error_message TEXT NOT NULL
		);`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec schema: %w", err)
		}
	}

	migrations := []string{
		`ALTER TABLE messages ADD COLUMN parent_message_id INTEGER`,
		`ALTER TABLE messages ADD COLUMN reveal_txid TEXT`,
		`ALTER TABLE messages ADD COLUMN reveal_raw_tx_hex TEXT`,
		`ALTER TABLE messages ADD COLUMN reveal_broadcast_at TEXT`,
		`ALTER TABLE messages ADD COLUMN reveal_confirm_height INTEGER`,
		`ALTER TABLE broadcast_attempts ADD COLUMN phase TEXT NOT NULL DEFAULT 'commit'`,
		`UPDATE messages SET status = 'building' WHERE status = 'prepared'`,
		`UPDATE messages SET status = 'commit_signed' WHERE status = 'signed'`,
		`UPDATE messages SET status = 'commit_sent' WHERE status = 'broadcasted'`,
		`UPDATE messages
		 SET status = CASE
			WHEN IFNULL(reveal_confirm_height, 0) > 0 THEN 'done'
			WHEN reveal_broadcast_at IS NOT NULL THEN 'reveal_sent'
			ELSE 'commit_confirmed'
		 END
		 WHERE status = 'confirmed'`,
	}
	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				_ = db.Close()
				return nil, fmt.Errorf("exec migration: %w", err)
			}
		}
	}

	return &Store{DB: db}, nil
}

func (s *Store) SeedInitialUTXOs(ctx context.Context, utxos []config.InitialUTXO) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, utxo := range utxos {
		_, err := s.DB.ExecContext(ctx, `
			INSERT INTO utxos (
				txid, vout, amount_sat, address, script_pub_key, address_type, status, source,
				confirm_height, last_seen_height, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
			ON CONFLICT(txid, vout) DO NOTHING
		`, utxo.TxID, utxo.Vout, utxo.AmountSat, utxo.Address, utxo.ScriptPubKey, utxo.AddressType, model.UTXOStatusAvailable, model.UTXOSourceConfig, now, now)
		if err != nil {
			return fmt.Errorf("seed utxo %s:%d: %w", utxo.TxID, utxo.Vout, err)
		}
	}
	return nil
}

func (s *Store) GetChainState(ctx context.Context, key string) (string, error) {
	var value string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM chain_state WHERE key = ?`, key).Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("get chain state %s: %w", key, err)
	}
	return value, nil
}

func (s *Store) SetChainState(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO chain_state(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	if err != nil {
		return fmt.Errorf("set chain state %s: %w", key, err)
	}
	return nil
}

func (s *Store) ListAvailableUTXOs(ctx context.Context) ([]model.UTXO, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT txid, vout, amount_sat, address, script_pub_key, address_type, status, source, spent_by_txid, confirm_height
		FROM utxos WHERE status = ? ORDER BY amount_sat ASC, txid ASC, vout ASC
	`, model.UTXOStatusAvailable)
	if err != nil {
		return nil, fmt.Errorf("query available utxos: %w", err)
	}
	defer rows.Close()

	var result []model.UTXO
	for rows.Next() {
		var item model.UTXO
		var spentByTxID sql.NullString
		if err := rows.Scan(&item.TxID, &item.Vout, &item.AmountSat, &item.Address, &item.ScriptPubKey, &item.AddressType, &item.Status, &item.Source, &spentByTxID, &item.ConfirmHeight); err != nil {
			return nil, fmt.Errorf("scan utxo: %w", err)
		}
		if spentByTxID.Valid {
			item.SpentByTxID = spentByTxID.String
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ReserveUTXOs(ctx context.Context, messageID int64, utxos []model.UTXO) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reserve tx: %w", err)
	}
	defer tx.Rollback()

	for _, utxo := range utxos {
		res, err := tx.ExecContext(ctx, `
			UPDATE utxos
			SET status = ?, reserved_by_message_id = ?, reserved_at = ?, updated_at = ?
			WHERE txid = ? AND vout = ? AND status = ?
		`, model.UTXOStatusReserved, messageID, now, now, utxo.TxID, utxo.Vout, model.UTXOStatusAvailable)
		if err != nil {
			return fmt.Errorf("reserve utxo %s:%d: %w", utxo.TxID, utxo.Vout, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("reserve utxo %s:%d rows affected: %w", utxo.TxID, utxo.Vout, err)
		}
		if affected != 1 {
			return fmt.Errorf("utxo %s:%d is not available", utxo.TxID, utxo.Vout)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reserve tx: %w", err)
	}
	return nil
}

func (s *Store) ReleaseReservedUTXOs(ctx context.Context, messageID int64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE utxos
		SET status = ?, reserved_by_message_id = NULL, reserved_at = NULL, updated_at = ?
		WHERE reserved_by_message_id = ? AND status = ?
	`, model.UTXOStatusAvailable, time.Now().UTC().Format(time.RFC3339), messageID, model.UTXOStatusReserved)
	if err != nil {
		return fmt.Errorf("release reserved utxos: %w", err)
	}
	return nil
}

func (s *Store) CreateMessage(ctx context.Context, messageType model.MessageType, payload string, relatedHeight *uint64, indexerID string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var height any
	if relatedHeight != nil {
		height = *relatedHeight
	}

	if messageType == model.MessageTypeProve && relatedHeight != nil {
		var existingID int64
		err := s.DB.QueryRowContext(ctx, `
			SELECT id FROM messages
			WHERE type = ? AND related_height = ? AND parent_message_id IS NULL AND status IN (?, ?, ?, ?, ?, ?)
			ORDER BY id ASC LIMIT 1
		`, messageType, *relatedHeight, model.MessageStatusBuilding, model.MessageStatusCommitSigned, model.MessageStatusCommitSent, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageStatusDone).Scan(&existingID)
		if err == nil {
			return existingID, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("query existing prove message: %w", err)
		}
	}

	res, err := s.DB.ExecContext(ctx, `
		INSERT INTO messages(type, status, payload_text, related_height, indexer_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
	`, messageType, model.MessageStatusBuilding, payload, height, indexerID, now, now)
	if err != nil {
		return 0, fmt.Errorf("create message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

func (s *Store) CreateBroadcastAttempt(ctx context.Context, messageID int64, phase, errorMessage string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO broadcast_attempts(message_id, phase, attempted_at, error_message)
		VALUES(?, ?, ?, ?)
	`, messageID, phase, time.Now().UTC().Format(time.RFC3339), errorMessage)
	if err != nil {
		return fmt.Errorf("create broadcast attempt for message %d: %w", messageID, err)
	}
	return nil
}

func (s *Store) ListBroadcastAttemptsByMessage(ctx context.Context, messageID int64) ([]BroadcastAttemptRecord, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, message_id, phase, attempted_at, error_message
		FROM broadcast_attempts WHERE message_id = ?
		ORDER BY id ASC
	`, messageID)
	if err != nil {
		return nil, fmt.Errorf("list broadcast attempts for message %d: %w", messageID, err)
	}
	defer rows.Close()

	var records []BroadcastAttemptRecord
	for rows.Next() {
		var record BroadcastAttemptRecord
		if err := rows.Scan(&record.ID, &record.MessageID, &record.Phase, &record.AttemptedAt, &record.ErrorMessage); err != nil {
			return nil, fmt.Errorf("scan broadcast attempt: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

type MessageRecord struct {
	ID                  int64
	Type                model.MessageType
	Status              model.MessageStatus
	PayloadText         string
	RelatedHeight       uint64
	IndexerID           string
	TxID                string
	RawTxHex            string
	ConfirmHeight       uint64
	ParentMessageID     int64
	RevealTxID          string
	RevealRawTxHex      string
	RevealBroadcastAt   string
	RevealConfirmHeight uint64
}

type BroadcastAttemptRecord struct {
	ID           int64
	MessageID    int64
	Phase        string
	AttemptedAt  string
	ErrorMessage string
}

func (s *Store) ListMessagesAwaitingReveal(ctx context.Context) ([]MessageRecord, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, type, status, payload_text, related_height, indexer_id, txid, raw_tx_hex, confirm_height, parent_message_id, reveal_txid, reveal_raw_tx_hex, reveal_broadcast_at, reveal_confirm_height
		FROM messages
		WHERE parent_message_id IS NULL
		  AND status = ?
		  AND reveal_raw_tx_hex <> ''
		  AND reveal_broadcast_at IS NULL
		  AND IFNULL(reveal_confirm_height, 0) = 0
		ORDER BY id ASC
	`, model.MessageStatusCommitConfirmed)
	if err != nil {
		return nil, fmt.Errorf("list messages awaiting reveal: %w", err)
	}
	defer rows.Close()

	var records []MessageRecord
	for rows.Next() {
		var record MessageRecord
		var relatedHeight sql.NullInt64
		var indexerID sql.NullString
		var txid sql.NullString
		var rawTxHex sql.NullString
		var confirmHeight sql.NullInt64
		var parentMessageID sql.NullInt64
		var revealTxID sql.NullString
		var revealRawTxHex sql.NullString
		var revealBroadcastAt sql.NullString
		var revealConfirmHeight sql.NullInt64
		if err := rows.Scan(&record.ID, &record.Type, &record.Status, &record.PayloadText, &relatedHeight, &indexerID, &txid, &rawTxHex, &confirmHeight, &parentMessageID, &revealTxID, &revealRawTxHex, &revealBroadcastAt, &revealConfirmHeight); err != nil {
			return nil, fmt.Errorf("scan awaiting reveal record: %w", err)
		}
		if relatedHeight.Valid {
			record.RelatedHeight = uint64(relatedHeight.Int64)
		}
		if indexerID.Valid {
			record.IndexerID = indexerID.String
		}
		if txid.Valid {
			record.TxID = txid.String
		}
		if rawTxHex.Valid {
			record.RawTxHex = rawTxHex.String
		}
		if confirmHeight.Valid {
			record.ConfirmHeight = uint64(confirmHeight.Int64)
		}
		if parentMessageID.Valid {
			record.ParentMessageID = parentMessageID.Int64
		}
		if revealTxID.Valid {
			record.RevealTxID = revealTxID.String
		}
		if revealRawTxHex.Valid {
			record.RevealRawTxHex = revealRawTxHex.String
		}
		if revealBroadcastAt.Valid {
			record.RevealBroadcastAt = revealBroadcastAt.String
		}
		if revealConfirmHeight.Valid {
			record.RevealConfirmHeight = uint64(revealConfirmHeight.Int64)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ListMessagesWithBroadcastedReveal(ctx context.Context) ([]MessageRecord, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, type, status, payload_text, related_height, indexer_id, txid, raw_tx_hex, confirm_height, parent_message_id, reveal_txid, reveal_raw_tx_hex, reveal_broadcast_at, reveal_confirm_height
		FROM messages
		WHERE parent_message_id IS NULL
		  AND status = ?
		  AND reveal_broadcast_at IS NOT NULL
		  AND IFNULL(reveal_confirm_height, 0) = 0
		ORDER BY id ASC
	`, model.MessageStatusRevealSent)
	if err != nil {
		return nil, fmt.Errorf("list messages with broadcasted reveal: %w", err)
	}
	defer rows.Close()

	var records []MessageRecord
	for rows.Next() {
		var record MessageRecord
		var relatedHeight sql.NullInt64
		var indexerID sql.NullString
		var txid sql.NullString
		var rawTxHex sql.NullString
		var confirmHeight sql.NullInt64
		var parentMessageID sql.NullInt64
		var revealTxID sql.NullString
		var revealRawTxHex sql.NullString
		var revealBroadcastAt sql.NullString
		var revealConfirmHeight sql.NullInt64
		if err := rows.Scan(&record.ID, &record.Type, &record.Status, &record.PayloadText, &relatedHeight, &indexerID, &txid, &rawTxHex, &confirmHeight, &parentMessageID, &revealTxID, &revealRawTxHex, &revealBroadcastAt, &revealConfirmHeight); err != nil {
			return nil, fmt.Errorf("scan broadcasted reveal record: %w", err)
		}
		if relatedHeight.Valid {
			record.RelatedHeight = uint64(relatedHeight.Int64)
		}
		if indexerID.Valid {
			record.IndexerID = indexerID.String
		}
		if txid.Valid {
			record.TxID = txid.String
		}
		if rawTxHex.Valid {
			record.RawTxHex = rawTxHex.String
		}
		if confirmHeight.Valid {
			record.ConfirmHeight = uint64(confirmHeight.Int64)
		}
		if parentMessageID.Valid {
			record.ParentMessageID = parentMessageID.Int64
		}
		if revealTxID.Valid {
			record.RevealTxID = revealTxID.String
		}
		if revealRawTxHex.Valid {
			record.RevealRawTxHex = revealRawTxHex.String
		}
		if revealBroadcastAt.Valid {
			record.RevealBroadcastAt = revealBroadcastAt.String
		}
		if revealConfirmHeight.Valid {
			record.RevealConfirmHeight = uint64(revealConfirmHeight.Int64)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ListPendingMessages(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id FROM messages WHERE parent_message_id IS NULL AND status = ? ORDER BY id ASC
	`, model.MessageStatusCommitSent)
	if err != nil {
		return nil, fmt.Errorf("query pending messages: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan pending message id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) ListMessagesByStatus(ctx context.Context, statuses ...model.MessageStatus) ([]MessageRecord, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	query := `SELECT id, type, status, payload_text, related_height, indexer_id, txid, raw_tx_hex, confirm_height, parent_message_id, reveal_txid, reveal_raw_tx_hex, reveal_broadcast_at, reveal_confirm_height FROM messages WHERE parent_message_id IS NULL AND status IN (`
	args := make([]any, 0, len(statuses))
	for i, status := range statuses {
		if i > 0 {
			query += ","
		}
		query += "?"
		args = append(args, status)
	}
	query += `) ORDER BY id ASC`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list messages by status: %w", err)
	}
	defer rows.Close()

	var records []MessageRecord
	for rows.Next() {
		var record MessageRecord
		var relatedHeight sql.NullInt64
		var indexerID sql.NullString
		var txid sql.NullString
		var rawTxHex sql.NullString
		var confirmHeight sql.NullInt64
		var parentMessageID sql.NullInt64
		var revealTxID sql.NullString
		var revealRawTxHex sql.NullString
		var revealBroadcastAt sql.NullString
		var revealConfirmHeight sql.NullInt64
		if err := rows.Scan(&record.ID, &record.Type, &record.Status, &record.PayloadText, &relatedHeight, &indexerID, &txid, &rawTxHex, &confirmHeight, &parentMessageID, &revealTxID, &revealRawTxHex, &revealBroadcastAt, &revealConfirmHeight); err != nil {
			return nil, fmt.Errorf("scan message record: %w", err)
		}
		if relatedHeight.Valid {
			record.RelatedHeight = uint64(relatedHeight.Int64)
		}
		if indexerID.Valid {
			record.IndexerID = indexerID.String
		}
		if txid.Valid {
			record.TxID = txid.String
		}
		if rawTxHex.Valid {
			record.RawTxHex = rawTxHex.String
		}
		if confirmHeight.Valid {
			record.ConfirmHeight = uint64(confirmHeight.Int64)
		}
		if parentMessageID.Valid {
			record.ParentMessageID = parentMessageID.Int64
		}
		if revealTxID.Valid {
			record.RevealTxID = revealTxID.String
		}
		if revealRawTxHex.Valid {
			record.RevealRawTxHex = revealRawTxHex.String
		}
		if revealBroadcastAt.Valid {
			record.RevealBroadcastAt = revealBroadcastAt.String
		}
		if revealConfirmHeight.Valid {
			record.RevealConfirmHeight = uint64(revealConfirmHeight.Int64)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) CountMessagesByStatus(ctx context.Context, statuses ...model.MessageStatus) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	query := `SELECT COUNT(1) FROM messages WHERE parent_message_id IS NULL AND status IN (`
	args := make([]any, 0, len(statuses))
	for i, status := range statuses {
		if i > 0 {
			query += ","
		}
		query += "?"
		args = append(args, status)
	}
	query += `)`

	var count int64
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count messages by status: %w", err)
	}
	return count, nil
}

func (s *Store) GetLatestMessageByType(ctx context.Context, messageType model.MessageType, statuses ...model.MessageStatus) (MessageRecord, error) {
	if len(statuses) == 0 {
		return MessageRecord{}, nil
	}

	query := `
		SELECT id, type, status, payload_text, related_height, indexer_id, txid, raw_tx_hex, confirm_height, parent_message_id, reveal_txid, reveal_raw_tx_hex, reveal_broadcast_at, reveal_confirm_height
		FROM messages WHERE type = ? AND parent_message_id IS NULL AND status IN (`
	args := make([]any, 0, len(statuses)+1)
	args = append(args, messageType)
	for i, status := range statuses {
		if i > 0 {
			query += ","
		}
		query += "?"
		args = append(args, status)
	}
	query += `) ORDER BY id DESC LIMIT 1`

	var record MessageRecord
	var relatedHeight sql.NullInt64
	var indexerID sql.NullString
	var txid sql.NullString
	var rawTxHex sql.NullString
	var confirmHeight sql.NullInt64
	var parentMessageID sql.NullInt64
	var revealTxID sql.NullString
	var revealRawTxHex sql.NullString
	var revealBroadcastAt sql.NullString
	var revealConfirmHeight sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&record.ID, &record.Type, &record.Status, &record.PayloadText, &relatedHeight, &indexerID, &txid, &rawTxHex, &confirmHeight, &parentMessageID, &revealTxID, &revealRawTxHex, &revealBroadcastAt, &revealConfirmHeight); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MessageRecord{}, nil
		}
		return MessageRecord{}, fmt.Errorf("get latest message by type: %w", err)
	}
	if relatedHeight.Valid {
		record.RelatedHeight = uint64(relatedHeight.Int64)
	}
	if indexerID.Valid {
		record.IndexerID = indexerID.String
	}
	if txid.Valid {
		record.TxID = txid.String
	}
	if rawTxHex.Valid {
		record.RawTxHex = rawTxHex.String
	}
	if confirmHeight.Valid {
		record.ConfirmHeight = uint64(confirmHeight.Int64)
	}
	if parentMessageID.Valid {
		record.ParentMessageID = parentMessageID.Int64
	}
	if revealTxID.Valid {
		record.RevealTxID = revealTxID.String
	}
	if revealRawTxHex.Valid {
		record.RevealRawTxHex = revealRawTxHex.String
	}
	if revealBroadcastAt.Valid {
		record.RevealBroadcastAt = revealBroadcastAt.String
	}
	if revealConfirmHeight.Valid {
		record.RevealConfirmHeight = uint64(revealConfirmHeight.Int64)
	}
	return record, nil
}

func (s *Store) GetMessage(ctx context.Context, messageID int64) (MessageRecord, error) {
	var record MessageRecord
	var relatedHeight sql.NullInt64
	var indexerID sql.NullString
	var txid sql.NullString
	var rawTxHex sql.NullString
	var confirmHeight sql.NullInt64
	var parentMessageID sql.NullInt64
	var revealTxID sql.NullString
	var revealRawTxHex sql.NullString
	var revealBroadcastAt sql.NullString
	var revealConfirmHeight sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `
		SELECT id, type, status, payload_text, related_height, indexer_id, txid, raw_tx_hex, confirm_height, parent_message_id, reveal_txid, reveal_raw_tx_hex, reveal_broadcast_at, reveal_confirm_height
		FROM messages WHERE id = ?
	`, messageID).Scan(&record.ID, &record.Type, &record.Status, &record.PayloadText, &relatedHeight, &indexerID, &txid, &rawTxHex, &confirmHeight, &parentMessageID, &revealTxID, &revealRawTxHex, &revealBroadcastAt, &revealConfirmHeight); err != nil {
		return MessageRecord{}, fmt.Errorf("get message %d: %w", messageID, err)
	}
	if relatedHeight.Valid {
		record.RelatedHeight = uint64(relatedHeight.Int64)
	}
	if indexerID.Valid {
		record.IndexerID = indexerID.String
	}
	if txid.Valid {
		record.TxID = txid.String
	}
	if rawTxHex.Valid {
		record.RawTxHex = rawTxHex.String
	}
	if confirmHeight.Valid {
		record.ConfirmHeight = uint64(confirmHeight.Int64)
	}
	if parentMessageID.Valid {
		record.ParentMessageID = parentMessageID.Int64
	}
	if revealTxID.Valid {
		record.RevealTxID = revealTxID.String
	}
	if revealRawTxHex.Valid {
		record.RevealRawTxHex = revealRawTxHex.String
	}
	if revealBroadcastAt.Valid {
		record.RevealBroadcastAt = revealBroadcastAt.String
	}
	if revealConfirmHeight.Valid {
		record.RevealConfirmHeight = uint64(revealConfirmHeight.Int64)
	}
	return record, nil
}

type BlockRecord struct {
	Height        uint64
	BlockHash     string
	Version       uint32
	Confirmations uint64
	Eligible      bool
	Status        model.BlockStatus
}

func (s *Store) UpsertBlock(ctx context.Context, height uint64, blockHash string, version uint32, confirmations uint64, eligible bool, status model.BlockStatus) error {
	now := time.Now().UTC().Format(time.RFC3339)
	eligibleInt := 0
	if eligible {
		eligibleInt = 1
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO chain_blocks(height, block_hash, version, confirmations, eligible, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(height) DO UPDATE SET
			block_hash = excluded.block_hash,
			version = excluded.version,
			confirmations = excluded.confirmations,
			eligible = excluded.eligible,
			status = excluded.status,
			updated_at = excluded.updated_at
	`, height, blockHash, version, confirmations, eligibleInt, status, now, now)
	if err != nil {
		return fmt.Errorf("upsert block %d: %w", height, err)
	}
	return nil
}

func (s *Store) FindMessageByHeightAndType(ctx context.Context, height uint64, messageType model.MessageType) (int64, error) {
	var id int64
	if err := s.DB.QueryRowContext(ctx, `
		SELECT id FROM messages WHERE parent_message_id IS NULL AND related_height = ? AND type = ? AND status IN (?, ?, ?, ?, ?, ?) ORDER BY id ASC LIMIT 1
	`, height, messageType, model.MessageStatusBuilding, model.MessageStatusCommitSigned, model.MessageStatusCommitSent, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageStatusDone).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("find message by height/type: %w", err)
	}
	return id, nil
}

func (s *Store) GetBlock(ctx context.Context, height uint64) (BlockRecord, error) {
	var record BlockRecord
	var eligibleInt int
	if err := s.DB.QueryRowContext(ctx, `
		SELECT height, block_hash, version, confirmations, eligible, status FROM chain_blocks WHERE height = ?
	`, height).Scan(&record.Height, &record.BlockHash, &record.Version, &record.Confirmations, &eligibleInt, &record.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BlockRecord{}, nil
		}
		return BlockRecord{}, fmt.Errorf("get block %d: %w", height, err)
	}
	record.Eligible = eligibleInt == 1
	return record, nil
}

func (s *Store) MarkBlockOrphaned(ctx context.Context, height uint64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE chain_blocks SET status = ?, updated_at = ? WHERE height = ?
	`, model.BlockStatusFailed, time.Now().UTC().Format(time.RFC3339), height)
	if err != nil {
		return fmt.Errorf("mark block orphaned at %d: %w", height, err)
	}
	return nil
}

func (s *Store) MarkMessagesFailedByHeight(ctx context.Context, height uint64, reason string) error {
	indexerID, err := s.GetChainState(ctx, "indexer_id")
	if err != nil {
		return err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark messages failed tx: %w", err)
	}
	defer tx.Rollback()

	if indexerID != "" {
		var confirmedRegisterCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(1) FROM messages
			WHERE (related_height = ? OR confirm_height = ? OR reveal_confirm_height = ?) AND type = ? AND status IN (?, ?, ?) AND indexer_id = ?
		`, height, height, height, model.MessageTypeRegister, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageStatusDone, indexerID).Scan(&confirmedRegisterCount); err != nil {
			return fmt.Errorf("count confirmed register messages at height %d: %w", height, err)
		}
		if confirmedRegisterCount > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM chain_state WHERE key = ?`, "indexer_id"); err != nil {
				return fmt.Errorf("clear indexer_id at height %d: %w", height, err)
			}
		}
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE messages
		SET status = ?,
			failure_reason = ?,
			confirm_height = CASE WHEN status IN (?, ?, ?) THEN 0 ELSE confirm_height END,
			reveal_broadcast_at = NULL,
			reveal_confirm_height = 0,
			indexer_id = CASE WHEN type = ? AND status IN (?, ?, ?) THEN NULL ELSE indexer_id END,
			updated_at = ?
		WHERE parent_message_id IS NULL
		  AND (related_height = ? OR confirm_height = ? OR reveal_confirm_height = ?)
		  AND status IN (?, ?, ?, ?, ?, ?)
	`, model.MessageStatusFailed, reason, model.MessageStatusCommitSent, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageTypeRegister, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageStatusDone, time.Now().UTC().Format(time.RFC3339), height, height, height, model.MessageStatusBuilding, model.MessageStatusCommitSigned, model.MessageStatusCommitSent, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageStatusDone)
	if err != nil {
		return fmt.Errorf("mark messages failed at height %d: %w", height, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark messages failed tx: %w", err)
	}
	return nil
}

func (s *Store) RollbackOrphanedUTXOsByHeight(ctx context.Context, height uint64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE utxos
		SET status = ?, reserved_by_message_id = NULL, reserved_at = NULL, spent_by_txid = NULL, confirm_height = 0, updated_at = ?
		WHERE reserved_by_message_id IN (
			SELECT id FROM messages WHERE parent_message_id IS NULL AND (related_height = ? OR confirm_height = ? OR reveal_confirm_height = ?) AND status IN (?, ?, ?, ?, ?, ?)
		) AND status IN (?, ?)
	`, model.UTXOStatusAvailable, time.Now().UTC().Format(time.RFC3339), height, height, height, model.MessageStatusBuilding, model.MessageStatusCommitSigned, model.MessageStatusCommitSent, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageStatusDone, model.UTXOStatusReserved, model.UTXOStatusSpentPending)
	if err != nil {
		return fmt.Errorf("rollback orphaned utxos at height %d: %w", height, err)
	}
	return nil
}

func (s *Store) InvalidateOrphanedChangeUTXOsByHeight(ctx context.Context, height uint64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE utxos
		SET status = ?, spent_by_txid = NULL, confirm_height = 0, updated_at = ?
			WHERE source = ?
			  AND reserved_by_message_id IN (
				SELECT id FROM messages WHERE parent_message_id IS NULL AND (related_height = ? OR confirm_height = ? OR reveal_confirm_height = ?) AND status IN (?, ?, ?, ?, ?, ?)
			  )
			  AND status IN (?, ?, ?, ?)
		`, model.UTXOStatusInvalid, time.Now().UTC().Format(time.RFC3339), model.UTXOSourceChange, height, height, height, model.MessageStatusBuilding, model.MessageStatusCommitSigned, model.MessageStatusCommitSent, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageStatusDone, model.UTXOStatusPending, model.UTXOStatusAvailable, model.UTXOStatusSpentPending, model.UTXOStatusSpentConfirmed)
	if err != nil {
		return fmt.Errorf("invalidate orphaned change utxos at height %d: %w", height, err)
	}
	return nil
}

func (s *Store) MarkMessageSigned(ctx context.Context, messageID int64, rawTxHex string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE messages SET status = ?, raw_tx_hex = ?, updated_at = ? WHERE id = ?
	`, model.MessageStatusCommitSigned, rawTxHex, time.Now().UTC().Format(time.RFC3339), messageID)
	if err != nil {
		return fmt.Errorf("mark message signed: %w", err)
	}
	return nil
}

func (s *Store) MarkMessageSignedWithReveal(ctx context.Context, messageID int64, rawTxHex, revealRawTxHex, revealTxID string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE messages
		SET status = ?, raw_tx_hex = ?, reveal_raw_tx_hex = ?, reveal_txid = ?, reveal_broadcast_at = NULL, reveal_confirm_height = 0, updated_at = ?
		WHERE id = ?
	`, model.MessageStatusCommitSigned, rawTxHex, revealRawTxHex, revealTxID, time.Now().UTC().Format(time.RFC3339), messageID)
	if err != nil {
		return fmt.Errorf("mark message signed with reveal: %w", err)
	}
	return nil
}

func (s *Store) ResetMessageToBuilding(ctx context.Context, messageID int64) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `
		UPDATE messages
		SET status = ?,
			txid = NULL,
			raw_tx_hex = NULL,
			broadcast_at = NULL,
			confirm_height = 0,
			failure_reason = NULL,
			reveal_txid = NULL,
			reveal_raw_tx_hex = NULL,
			reveal_broadcast_at = NULL,
			reveal_confirm_height = 0,
			updated_at = ?
		WHERE id = ? AND parent_message_id IS NULL
	`, model.MessageStatusBuilding, time.Now().UTC().Format(time.RFC3339), messageID)
	if err != nil {
		return false, fmt.Errorf("reset message %d to building: %w", messageID, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("get reset message row count %d: %w", messageID, err)
	}
	return rowsAffected > 0, nil
}

func (s *Store) MarkMessageBroadcasted(ctx context.Context, messageID int64, txid string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE messages SET status = ?, txid = ?, broadcast_at = ?, updated_at = ? WHERE id = ?
	`, model.MessageStatusCommitSent, txid, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), messageID)
	if err != nil {
		return fmt.Errorf("mark message broadcasted: %w", err)
	}
	return nil
}

func (s *Store) MarkReservedUTXOsSpent(ctx context.Context, messageID int64, spentByTxID string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE utxos
		SET status = ?, spent_by_txid = ?, updated_at = ?
		WHERE reserved_by_message_id = ? AND status = ?
	`, model.UTXOStatusSpentPending, spentByTxID, time.Now().UTC().Format(time.RFC3339), messageID, model.UTXOStatusReserved)
	if err != nil {
		return fmt.Errorf("mark reserved utxos spent: %w", err)
	}
	return nil
}

func (s *Store) MarkMessageConfirmed(ctx context.Context, messageID int64, confirmHeight uint64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE messages SET status = ?, confirm_height = ?, updated_at = ? WHERE id = ?
	`, model.MessageStatusCommitConfirmed, confirmHeight, time.Now().UTC().Format(time.RFC3339), messageID)
	if err != nil {
		return fmt.Errorf("mark message confirmed: %w", err)
	}
	return nil
}

func (s *Store) MarkMessageConfirmedByTxID(ctx context.Context, txid string, confirmHeight uint64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE messages SET status = ?, txid = ?, confirm_height = ?, updated_at = ? WHERE txid = ?
	`, model.MessageStatusCommitConfirmed, txid, confirmHeight, time.Now().UTC().Format(time.RFC3339), txid)
	if err != nil {
		return fmt.Errorf("mark message confirmed by txid %s: %w", txid, err)
	}
	return nil
}

func (s *Store) UpdateMessageConfirmationDetails(ctx context.Context, messageID int64, relatedHeight uint64, indexerID string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE messages SET related_height = ?, indexer_id = ?, updated_at = ? WHERE id = ?
	`, relatedHeight, indexerID, time.Now().UTC().Format(time.RFC3339), messageID)
	if err != nil {
		return fmt.Errorf("update message confirmation details: %w", err)
	}
	return nil
}

func (s *Store) MarkUTXOConfirmed(ctx context.Context, spentByTxID string, confirmHeight uint64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE utxos SET status = ?, confirm_height = ?, updated_at = ? WHERE spent_by_txid = ? AND status = ?
	`, model.UTXOStatusSpentConfirmed, confirmHeight, time.Now().UTC().Format(time.RFC3339), spentByTxID, model.UTXOStatusSpentPending)
	if err != nil {
		return fmt.Errorf("mark utxo confirmed: %w", err)
	}
	return nil
}

func (s *Store) SetIndexerID(ctx context.Context, indexerID string) error {
	return s.SetChainState(ctx, "indexer_id", indexerID)
}

func (s *Store) MarkRevealBroadcasted(ctx context.Context, messageID int64, txid string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE messages SET status = ?, reveal_txid = ?, reveal_broadcast_at = ?, updated_at = ? WHERE id = ?
	`, model.MessageStatusRevealSent, txid, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), messageID)
	if err != nil {
		return fmt.Errorf("mark reveal broadcasted: %w", err)
	}
	return nil
}

func (s *Store) MarkRevealConfirmed(ctx context.Context, messageID int64, confirmHeight uint64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE messages SET status = ?, reveal_confirm_height = ?, updated_at = ? WHERE id = ?
	`, model.MessageStatusDone, confirmHeight, time.Now().UTC().Format(time.RFC3339), messageID)
	if err != nil {
		return fmt.Errorf("mark reveal confirmed: %w", err)
	}
	return nil
}

func (s *Store) InsertChangeUTXO(ctx context.Context, messageID int64, utxo model.UTXO) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO utxos (
			txid, vout, amount_sat, address, script_pub_key, address_type, status, source,
			reserved_by_message_id, spent_by_txid, confirm_height, last_seen_height, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(txid, vout) DO NOTHING
	`, utxo.TxID, utxo.Vout, utxo.AmountSat, utxo.Address, utxo.ScriptPubKey, utxo.AddressType, utxo.Status, utxo.Source, messageID, utxo.SpentByTxID, utxo.ConfirmHeight, 0, now, now)
	if err != nil {
		return fmt.Errorf("insert change utxo: %w", err)
	}
	return nil
}

func (s *Store) MarkChangeUTXOsConfirmed(ctx context.Context, messageID int64, confirmHeight uint64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE utxos
		SET status = ?, confirm_height = ?, updated_at = ?
		WHERE reserved_by_message_id = ? AND source = ? AND status = ?
	`, model.UTXOStatusAvailable, confirmHeight, time.Now().UTC().Format(time.RFC3339), messageID, model.UTXOSourceChange, model.UTXOStatusPending)
	if err != nil {
		return fmt.Errorf("mark change utxos confirmed: %w", err)
	}
	return nil
}
