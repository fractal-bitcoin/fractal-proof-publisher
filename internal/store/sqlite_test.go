package store

import (
	"context"
	"path/filepath"
	"testing"

	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/model"
)

func TestSeedInitialUTXOs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	seed := []config.InitialUTXO{{
		TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		Vout:         0,
		AmountSat:    1000,
		Address:      "addr1",
		ScriptPubKey: "0014abcd",
		AddressType:  "p2wpkh",
	}}
	if err := s.SeedInitialUTXOs(ctx, seed); err != nil {
		t.Fatalf("SeedInitialUTXOs() error = %v", err)
	}
	utxos, err := s.ListAvailableUTXOs(ctx)
	if err != nil {
		t.Fatalf("ListAvailableUTXOs() error = %v", err)
	}
	if len(utxos) != 1 {
		t.Fatalf("available utxos = %d, want 1", len(utxos))
	}
}

func TestGetLatestMessageByType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latest-message.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	firstID, err := s.CreateMessage(ctx, "register", "payload1", nil, "")
	if err != nil {
		t.Fatalf("CreateMessage() first error = %v", err)
	}
	secondID, err := s.CreateMessage(ctx, "register", "payload2", nil, "")
	if err != nil {
		t.Fatalf("CreateMessage() second error = %v", err)
	}
	if firstID == secondID {
		t.Fatalf("expected different register ids, got %d", firstID)
	}

	message, err := s.GetLatestMessageByType(ctx, "register", "building")
	if err != nil {
		t.Fatalf("GetLatestMessageByType() error = %v", err)
	}
	if message.ID != secondID {
		t.Fatalf("latest message id = %d, want %d", message.ID, secondID)
	}
}

func TestMarkMessageBroadcastedKeepsExistingChangeUTXOIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "change-align.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(100)
	messageID, err := s.CreateMessage(ctx, model.MessageTypeProve, "payload", &height, "100:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	changePlaceholder := model.UTXO{
		TxID:         "placeholder-txid",
		Vout:         1,
		AmountSat:    1234,
		Address:      "bc1ptest",
		ScriptPubKey: "5120abcd",
		AddressType:  "p2tr",
		Status:       model.UTXOStatusPending,
		Source:       model.UTXOSourceChange,
	}
	if err := s.InsertChangeUTXO(ctx, messageID, changePlaceholder); err != nil {
		t.Fatalf("InsertChangeUTXO() error = %v", err)
	}

	realTxID := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if err := s.MarkMessageBroadcasted(ctx, messageID, realTxID); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}

	var gotTxID string
	var gotReservedBy int64
	if err := s.DB.QueryRowContext(ctx, `SELECT txid, reserved_by_message_id FROM utxos WHERE txid = ? AND vout = ?`, changePlaceholder.TxID, changePlaceholder.Vout).Scan(&gotTxID, &gotReservedBy); err != nil {
		t.Fatalf("query change utxo error = %v", err)
	}
	if gotTxID != changePlaceholder.TxID {
		t.Fatalf("change utxo txid = %q, want %q", gotTxID, changePlaceholder.TxID)
	}
	if gotReservedBy != messageID {
		t.Fatalf("change utxo reserved_by_message_id = %d, want %d", gotReservedBy, messageID)
	}
}

func TestMarkMessageSignedWithRevealStoresRevealOnSubmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reveal-message.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(100)
	messageID, err := s.CreateMessage(ctx, model.MessageTypeProve, "payload", &height, "100:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "commit-hex", "reveal-hex", "reveal-txid"); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.RevealTxID != "reveal-txid" {
		t.Fatalf("parent reveal txid = %q, want %q", message.RevealTxID, "reveal-txid")
	}
	if message.RevealRawTxHex != "reveal-hex" {
		t.Fatalf("parent reveal raw tx = %q, want %q", message.RevealRawTxHex, "reveal-hex")
	}
	if message.RevealConfirmHeight != 0 {
		t.Fatalf("reveal confirm height = %d, want 0", message.RevealConfirmHeight)
	}
}

func TestCreateBroadcastAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broadcast-attempts.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	messageID, err := s.CreateMessage(ctx, model.MessageTypeProve, func() string { return "payload" }(), func() *uint64 { v := uint64(100); return &v }(), "100:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}

	if err := s.CreateBroadcastAttempt(ctx, messageID, "reveal", "reveal prepared"); err != nil {
		t.Fatalf("CreateBroadcastAttempt() error = %v", err)
	}

	attempts, err := s.ListBroadcastAttemptsByMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("ListBroadcastAttemptsByMessage() error = %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("broadcast attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Phase != "reveal" {
		t.Fatalf("broadcast attempt phase = %q, want %q", attempts[0].Phase, "reveal")
	}
	if attempts[0].MessageID != messageID {
		t.Fatalf("broadcast attempt message id = %d, want %d", attempts[0].MessageID, messageID)
	}
	if attempts[0].ErrorMessage != "reveal prepared" {
		t.Fatalf("broadcast attempt error = %q, want %q", attempts[0].ErrorMessage, "reveal prepared")
	}
}

func TestResetMessageToBuildingWithPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reset-with-payload.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(100)
	messageID, err := s.CreateMessage(ctx, model.MessageTypeProve, "old-payload", &height, "100:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "commit-hex", "reveal-hex", "reveal-txid"); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "commit-txid"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}

	updated, err := s.ResetMessageToBuildingWithPayload(ctx, messageID, "new-payload")
	if err != nil {
		t.Fatalf("ResetMessageToBuildingWithPayload() error = %v", err)
	}
	if !updated {
		t.Fatal("ResetMessageToBuildingWithPayload() updated = false, want true")
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusBuilding {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusBuilding)
	}
	if message.PayloadText != "new-payload" {
		t.Fatalf("payload = %q, want %q", message.PayloadText, "new-payload")
	}
	if message.TxID != "" || message.RawTxHex != "" || message.RevealTxID != "" || message.RevealRawTxHex != "" {
		t.Fatalf("tx fields not cleared txid=%q raw=%q reveal_txid=%q reveal_raw=%q", message.TxID, message.RawTxHex, message.RevealTxID, message.RevealRawTxHex)
	}
	if message.ConfirmHeight != 0 || message.RevealConfirmHeight != 0 {
		t.Fatalf("confirm heights not cleared confirm=%d reveal_confirm=%d", message.ConfirmHeight, message.RevealConfirmHeight)
	}
}
