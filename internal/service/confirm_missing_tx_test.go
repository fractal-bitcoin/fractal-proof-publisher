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

	"github.com/btcsuite/btcd/chaincfg"
)

func TestConfirmOnceKeepsBroadcastedMessageWhenTxNotInBlock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "confirm-missing-tx.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	messageID, err := s.CreateMessage(ctx, "register", "fip101,v1,register,100,p2tr,bc1...,name", nil, "")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSigned(ctx, messageID, "abcd"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
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
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"blockhash","error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"blockhash","height":100,"confirmations":1,"version":539361536,"tx":["othertx"]},"error":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer rpcServer.Close()

	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	changeAddr, err := keyMaterial.Address(&chaincfg.MainNetParams, "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}

	engine := Engine{
		Store:       s,
		RPC:         bitcoinrpc.New(rpcServer.URL, "", ""),
		FeeAPI:      feeapi.New(rpcServer.URL, time.Second),
		StateAPI:    stateapi.New(rpcServer.URL, "", time.Second, ""),
		Config:      config.Config{Signing: config.SigningConfig{ChangeAddress: changeAddr}},
		KeyMaterial: keyMaterial,
	}

	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != "commit_sent" {
		t.Fatalf("message status = %q, want commit_sent", message.Status)
	}
}
