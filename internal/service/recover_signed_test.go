package service

import (
	"context"
	"path/filepath"
	"testing"

	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/store"
)

func TestRecoverOnceBroadcastsSignedMessageWhenDisabledBroadcastModeGeneratesTxid(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover-signed.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	messageID, err := s.CreateMessage(ctx, "prove", "payload", func() *uint64 { v := uint64(123); return &v }(), "123:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSigned(ctx, messageID, "01000000000000000000"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
	}

	engine := Engine{Store: s, Config: config.Config{Runtime: config.RuntimeConfig{DisableBroadcast: true}}}
	if err := engine.RecoverOnce(ctx); err != nil {
		t.Fatalf("RecoverOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != "commit_sent" && message.Status != "commit_confirmed" && message.Status != "reveal_sent" {
		t.Fatalf("message status = %q, want commit_sent/commit_confirmed/reveal_sent", message.Status)
	}
	if message.TxID == "" {
		t.Fatal("expected txid to be set after recovery broadcast")
	}
}

func TestRecoverOnceKeepsSignedMessageWhenRPCAvailableIsRequiredButMissing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover-signed-no-rpc.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	messageID, err := s.CreateMessage(ctx, "prove", "payload", func() *uint64 { v := uint64(123); return &v }(), "123:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSigned(ctx, messageID, "abcd1234"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
	}

	engine := Engine{Store: s, Config: config.Config{Runtime: config.RuntimeConfig{DisableBroadcast: false}}}
	if err := engine.RecoverOnce(ctx); err != nil {
		t.Fatalf("RecoverOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != "commit_signed" {
		t.Fatalf("message status = %q, want commit_signed", message.Status)
	}
	if message.TxID != "" {
		t.Fatalf("txid = %q, want empty", message.TxID)
	}
}

func TestRecoverOnceBroadcastsRevealForConfirmedSubmission(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover-confirmed-reveal.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(123)
	messageID, err := s.CreateMessage(ctx, model.MessageTypeProve, "payload", &height, "123:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "01000000000000000000", "02000000000000000000", ""); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "committxid"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}
	if err := s.MarkMessageConfirmed(ctx, messageID, height); err != nil {
		t.Fatalf("MarkMessageConfirmed() error = %v", err)
	}

	engine := Engine{Store: s, Config: config.Config{Runtime: config.RuntimeConfig{DisableBroadcast: true}}}
	if err := engine.RecoverOnce(ctx); err != nil {
		t.Fatalf("RecoverOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusDone {
		t.Fatalf("message status = %q, want done", message.Status)
	}
	if message.RevealTxID == "" {
		t.Fatal("reveal txid is empty")
	}
}
