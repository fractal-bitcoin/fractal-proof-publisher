package service

import (
	"context"
	"path/filepath"
	"testing"

	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/store"
)

func TestRecoverOnceBroadcastedWithoutRPCDoesNotCrash(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover-broadcasted-no-rpc.db")
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
	if err := s.MarkMessageBroadcasted(ctx, messageID, "deadbeef"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}

	engine := Engine{Store: s, Config: config.Config{Runtime: config.RuntimeConfig{DisableBroadcast: true}}}
	if err := engine.RecoverOnce(ctx); err != nil {
		t.Fatalf("RecoverOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != "commit_sent" {
		t.Fatalf("message status = %q, want commit_sent", message.Status)
	}
}
