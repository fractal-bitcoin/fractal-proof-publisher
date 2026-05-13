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
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/stateapi"
	"fractal-proof-publisher/internal/store"
)

func TestScanOnceRetriesWhenStateAPIIsTemporarilyBehind(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "scan-retry.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	if err := s.SetChainState(ctx, "indexer_id", "100:1"); err != nil {
		t.Fatalf("SetChainState(indexer_id) error = %v", err)
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
			_, _ = w.Write([]byte(`{"result":"block100","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"block100","height":100,"confirmations":1,"version":539361536},"error":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer rpcServer.Close()

	stateReady := false
	stateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !stateReady {
			http.Error(w, "latest state not ready", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"blockhash":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","statehash":"ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"}`))
	}))
	defer stateServer.Close()

	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	engine := Engine{
		Store:       s,
		RPC:         bitcoinrpc.New(rpcServer.URL, "", ""),
		FeeAPI:      feeapi.New(rpcServer.URL, time.Second),
		StateAPI:    stateapi.New(stateServer.URL, "", time.Second, ""),
		Config:      config.Config{Scan: config.ScanConfig{StartHeight: 100, TargetBlockVersion: 539361536, RequiredConfirmations: 1}},
		KeyMaterial: keyMaterial,
	}

	if err := engine.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce() retryable error = %v", err)
	}
	lastScanned, err := s.GetChainState(ctx, "last_scanned_height")
	if err != nil {
		t.Fatalf("GetChainState(last_scanned_height) error = %v", err)
	}
	if lastScanned != "" {
		t.Fatalf("last_scanned_height = %q, want empty while waiting for state api", lastScanned)
	}
	existingID, err := s.FindMessageByHeightAndType(ctx, 100, model.MessageTypeProve)
	if err != nil {
		t.Fatalf("FindMessageByHeightAndType() error = %v", err)
	}
	if existingID != 0 {
		t.Fatalf("prove message id = %d, want 0 before state api is ready", existingID)
	}

	stateReady = true
	if err := engine.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce() after state ready error = %v", err)
	}
	lastScanned, err = s.GetChainState(ctx, "last_scanned_height")
	if err != nil {
		t.Fatalf("GetChainState(last_scanned_height) after ready error = %v", err)
	}
	if lastScanned != "100" {
		t.Fatalf("last_scanned_height = %q, want 100 after successful prove creation", lastScanned)
	}
	existingID, err = s.FindMessageByHeightAndType(ctx, 100, model.MessageTypeProve)
	if err != nil {
		t.Fatalf("FindMessageByHeightAndType() after ready error = %v", err)
	}
	if existingID == 0 {
		t.Fatal("expected prove message to be created after state api becomes ready")
	}
}
