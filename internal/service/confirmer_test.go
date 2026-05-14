package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fractal-proof-publisher/internal/bitcoinrpc"
	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/feeapi"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/stateapi"
	"fractal-proof-publisher/internal/store"

	"github.com/btcsuite/btcd/chaincfg"
)

func TestConfirmProveWritesRevealAuditContext(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "confirm-prove-reveal.db")
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
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "01000000000000000000", "02000000000000000000", "revealtxid"); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "committxid"); err != nil {
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
			_, _ = w.Write([]byte(`{"result":"blockhash","error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"blockhash","height":100,"confirmations":1,"version":539361536,"tx":["committxid"]},"error":null}`))
		case "sendrawtransaction":
			_, _ = w.Write([]byte(`{"result":"revealtxid","error":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer rpcServer.Close()

	engine := Engine{Store: s, RPC: bitcoinrpc.New(rpcServer.URL, "", "")}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce() error = %v", err)
	}

	attempts, err := s.ListBroadcastAttemptsByMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("ListBroadcastAttemptsByMessage() error = %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("broadcast attempts = %d, want 0 on successful reveal broadcast", len(attempts))
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusRevealSent {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusRevealSent)
	}
	if message.RevealTxID != "revealtxid" {
		t.Fatalf("reveal txid = %q, want revealtxid", message.RevealTxID)
	}
	if message.RevealConfirmHeight != 0 {
		t.Fatalf("reveal confirm height = %d, want 0", message.RevealConfirmHeight)
	}
}

func TestConfirmOnceUpdatesRegisterIndexerIDAfterRevealConfirmation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "confirm.db")
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
	if err := s.SetChainState(ctx, "last_scanned_height", "101"); err != nil {
		t.Fatalf("SetChainState() second error = %v", err)
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce() second error = %v", err)
	}

	indexerID, err := s.GetChainState(ctx, "indexer_id")
	if err != nil {
		t.Fatalf("GetChainState() error = %v", err)
	}
	if indexerID != "101:0" {
		t.Fatalf("indexer_id = %q, want 101:0", indexerID)
	}
	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.IndexerID != "101:0" {
		t.Fatalf("message indexer_id = %q, want 101:0", message.IndexerID)
	}
	if message.RelatedHeight != 101 {
		t.Fatalf("message related_height = %d, want 101", message.RelatedHeight)
	}
	if message.Status != model.MessageStatusDone {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusDone)
	}
}

func TestConfirmOncePromotesPendingChangeUTXO(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "confirm-change.db")
	s, err := store.Open(dbPath)
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
	if err := s.MarkMessageSigned(ctx, messageID, "abcd"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "deadbeef"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}
	if err := s.InsertChangeUTXO(ctx, messageID, model.UTXO{
		TxID:         "deadbeef",
		Vout:         1,
		AmountSat:    1234,
		Address:      "bc1ptest",
		ScriptPubKey: "5120abcd",
		AddressType:  "p2tr",
		Status:       model.UTXOStatusPending,
		Source:       model.UTXOSourceChange,
	}); err != nil {
		t.Fatalf("InsertChangeUTXO() error = %v", err)
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
			_, _ = w.Write([]byte(`{"result":{"hash":"blockhash","height":100,"confirmations":1,"version":539361536,"tx":["deadbeef"]},"error":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer rpcServer.Close()

	engine := Engine{
		Store:  s,
		RPC:    bitcoinrpc.New(rpcServer.URL, "", ""),
		Config: config.Config{Scan: config.ScanConfig{MaxReorgDepth: 6}},
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce() error = %v", err)
	}

	available, err := s.ListAvailableUTXOs(ctx)
	if err != nil {
		t.Fatalf("ListAvailableUTXOs() error = %v", err)
	}
	if len(available) != 1 {
		t.Fatalf("available change utxo count = %d, want 1", len(available))
	}
	if available[0].TxID != "deadbeef" {
		t.Fatalf("available change txid = %q, want deadbeef", available[0].TxID)
	}
}

func TestConfirmOnceSkipsWaitingForConfirmationInUnisatMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unisat-confirm.db")
	s, err := store.Open(dbPath)
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
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "abcd", "ef01", "revealtxid"); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "committxid"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}

	unisatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/indexer/local_pushtx":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":"revealtxid"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/indexer/tx/"):
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"txid":"revealtxid"}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer unisatServer.Close()

	engine := Engine{
		Store:         s,
		UnisatOpenAPI: NewUnisatOpenAPIClient(unisatServer.URL, "test-key", time.Second),
		Config:        config.Config{Runtime: config.RuntimeConfig{Mode: "unisat_open_api"}},
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusDone {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusDone)
	}
}

func TestConfirmOnceWaitsForUnisatCommitVisibilityBeforeReveal(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unisat-commit-wait.db")
	s, err := store.Open(dbPath)
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
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "abcd", "ef01", "revealtxid"); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "committxid"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}

	var revealPushCount int
	unisatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/indexer/tx/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/indexer/local_pushtx":
			revealPushCount++
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer unisatServer.Close()

	engine := Engine{
		Store:         s,
		UnisatOpenAPI: NewUnisatOpenAPIClient(unisatServer.URL, "test-key", time.Second),
		Config:        config.Config{Runtime: config.RuntimeConfig{Mode: "unisat_open_api"}},
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusCommitSent {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusCommitSent)
	}
	if message.RevealBroadcastAt != "" {
		t.Fatalf("reveal broadcast at = %q, want empty", message.RevealBroadcastAt)
	}
	if revealPushCount != 0 {
		t.Fatalf("reveal push count = %d, want 0", revealPushCount)
	}
}

func TestConfirmOnceRebroadcastsRevealWhenUnisatTxNotVisible(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unisat-reveal-wait.db")
	s, err := store.Open(dbPath)
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
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "abcd", "ef01", "revealtxid"); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "committxid"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}
	if err := s.MarkMessageConfirmed(ctx, messageID, height); err != nil {
		t.Fatalf("MarkMessageConfirmed() error = %v", err)
	}
	if err := s.MarkRevealBroadcasted(ctx, messageID, "revealtxid"); err != nil {
		t.Fatalf("MarkRevealBroadcasted() error = %v", err)
	}

	var pushCount int
	unisatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/indexer/tx/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/indexer/local_pushtx":
			pushCount++
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer unisatServer.Close()

	engine := Engine{
		Store:         s,
		UnisatOpenAPI: NewUnisatOpenAPIClient(unisatServer.URL, "test-key", time.Second),
		Config:        config.Config{Runtime: config.RuntimeConfig{Mode: "unisat_open_api"}},
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusRevealSent {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusRevealSent)
	}
	if pushCount != 1 {
		t.Fatalf("push count = %d, want 1", pushCount)
	}
}
