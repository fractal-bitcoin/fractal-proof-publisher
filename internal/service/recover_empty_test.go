package service

import (
	"context"
	"path/filepath"
	"testing"

	"fractal-proof-publisher/internal/store"
)

func TestRecoverOnceWithNoPendingMessages(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover-empty.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	engine := Engine{Store: s}
	if err := engine.RecoverOnce(context.Background()); err != nil {
		t.Fatalf("RecoverOnce() error = %v", err)
	}
}
