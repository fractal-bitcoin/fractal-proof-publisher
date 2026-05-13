package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"fractal-proof-publisher/internal/bitcoinrpc"
	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/feeapi"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/stateapi"
	"fractal-proof-publisher/internal/store"
)

func TestScanOnceMarksOrphanedOnHashChange(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orphan.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	if err := s.UpsertBlock(ctx, 100, "oldhash", 539361536, 1, true, "ready"); err != nil {
		t.Fatalf("UpsertBlock() seed error = %v", err)
	}

	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "getblockcount":
			_, _ = w.Write([]byte(`{"result":100,"error":null}`))
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"newhash","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536},"error":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer rpcServer.Close()

	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	engine := Engine{
		Store:       s,
		RPC:         bitcoinrpc.New(rpcServer.URL, "", ""),
		FeeAPI:      feeapi.New(rpcServer.URL, time.Second),
		StateAPI:    stateapi.New(rpcServer.URL, "", time.Second, ""),
		Config:      config.Config{Scan: config.ScanConfig{StartHeight: 100, TargetBlockVersion: 539361536, RequiredConfirmations: 1}},
		KeyMaterial: keyMaterial,
	}

	if err := engine.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce() error = %v", err)
	}

	block, err := s.GetBlock(ctx, 100)
	if err != nil {
		t.Fatalf("GetBlock() error = %v", err)
	}
	if block.BlockHash != "newhash" {
		t.Fatalf("block hash = %q, want newhash", block.BlockHash)
	}
}
