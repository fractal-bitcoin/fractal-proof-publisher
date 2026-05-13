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

func TestRecoverOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	messageID, err := s.CreateMessage(ctx, "register", "fip101,1,register_indexer,100,bc1...,name", nil, "")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "abcd", "ef01", "revealtxid"); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "deadbeef"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_scanned_height", "100"); err != nil {
		t.Fatalf("SetChainState() error = %v", err)
	}

	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "getblockhash":
			if len(req.Params) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch int(req.Params[0].(float64)) {
			case 100:
				_, _ = w.Write([]byte(`{"result":"blockhash100","error":null}`))
			case 101:
				_, _ = w.Write([]byte(`{"result":"blockhash101","error":null}`))
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "getblock":
			if len(req.Params) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch req.Params[0].(string) {
			case "blockhash100":
				_, _ = w.Write([]byte(`{"result":{"hash":"blockhash100","height":100,"confirmations":2,"version":539361536,"tx":["deadbeef"]},"error":null}`))
			case "blockhash101":
				_, _ = w.Write([]byte(`{"result":{"hash":"blockhash101","height":101,"confirmations":1,"version":539361536,"tx":["revealtxid"]},"error":null}`))
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "sendrawtransaction":
			_, _ = w.Write([]byte(`{"result":"revealtxid","error":null}`))
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
		Config:      config.Config{},
		KeyMaterial: keyMaterial,
	}

	if err := engine.RecoverOnce(ctx); err != nil {
		t.Fatalf("RecoverOnce() error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_scanned_height", "101"); err != nil {
		t.Fatalf("SetChainState() second error = %v", err)
	}
	if err := engine.RecoverOnce(ctx); err != nil {
		t.Fatalf("RecoverOnce() second error = %v", err)
	}

	indexerID, err := s.GetChainState(ctx, "indexer_id")
	if err != nil {
		t.Fatalf("GetChainState() error = %v", err)
	}
	if indexerID != "101:0" {
		t.Fatalf("indexer_id = %q, want 101:0", indexerID)
	}
}

func TestRecoverOnceFindsConfirmationAfterRelatedHeight(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover-late-confirm.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(100)
	messageID, err := s.CreateMessage(ctx, "prove", "payload", &height, "100:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSigned(ctx, messageID, "abcd"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "deadbeef"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_scanned_height", "102"); err != nil {
		t.Fatalf("SetChainState() error = %v", err)
	}

	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "getblockhash":
			if len(req.Params) > 0 {
				switch int(req.Params[0].(float64)) {
				case 100:
					_, _ = w.Write([]byte(`{"result":"blockhash100","error":null}`))
				case 101:
					_, _ = w.Write([]byte(`{"result":"blockhash101","error":null}`))
				case 102:
					_, _ = w.Write([]byte(`{"result":"blockhash102","error":null}`))
				default:
					w.WriteHeader(http.StatusBadRequest)
				}
			}
		case "getblock":
			if len(req.Params) > 0 {
				switch req.Params[0].(string) {
				case "blockhash100":
					_, _ = w.Write([]byte(`{"result":{"hash":"blockhash100","height":100,"confirmations":3,"version":539361536,"tx":["othertx"]},"error":null}`))
				case "blockhash101":
					_, _ = w.Write([]byte(`{"result":{"hash":"blockhash101","height":101,"confirmations":2,"version":539361536,"tx":["deadbeef"]},"error":null}`))
				case "blockhash102":
					_, _ = w.Write([]byte(`{"result":{"hash":"blockhash102","height":102,"confirmations":1,"version":539361536,"tx":[]},"error":null}`))
				default:
					w.WriteHeader(http.StatusBadRequest)
				}
			}
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
		Config:      config.Config{},
		KeyMaterial: keyMaterial,
	}

	if err := engine.RecoverOnce(ctx); err != nil {
		t.Fatalf("RecoverOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != "commit_confirmed" {
		t.Fatalf("message status = %q, want commit_confirmed", message.Status)
	}
	if message.ConfirmHeight != 101 {
		t.Fatalf("confirm height = %d, want 101", message.ConfirmHeight)
	}
}
