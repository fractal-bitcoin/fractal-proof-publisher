package service

import (
	"context"
	"database/sql"
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

func TestScanOnceFailsConfirmedMessageOnReorg(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reorg-confirmed-msg.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(100)
	if err := s.UpsertBlock(ctx, height, "oldhash", 539361536, 1, true, "confirmed"); err != nil {
		t.Fatalf("UpsertBlock() error = %v", err)
	}
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
	if err := s.MarkMessageConfirmed(ctx, messageID, 100); err != nil {
		t.Fatalf("MarkMessageConfirmed() error = %v", err)
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
		case "getblockcount":
			_, _ = w.Write([]byte(`{"result":100,"error":null}`))
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"newhash","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536},"error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536,"tx":[]},"error":null}`))
		default:
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-32601,"message":"method not found"}}`))
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
	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusCommitConfirmed {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusCommitConfirmed)
	}
	if message.ConfirmHeight != height {
		t.Fatalf("confirm height = %d, want %d", message.ConfirmHeight, height)
	}
}

func TestScanOnceKeepsChangeUTXOOnConfirmedProveReorg(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reorg-confirmed-prove-change.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(100)
	if err := s.UpsertBlock(ctx, height, "oldhash", 539361536, 1, true, "confirmed"); err != nil {
		t.Fatalf("UpsertBlock() error = %v", err)
	}
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
	if err := s.MarkMessageConfirmed(ctx, messageID, height); err != nil {
		t.Fatalf("MarkMessageConfirmed() error = %v", err)
	}
	changeUTXO := model.UTXO{
		TxID:         "deadbeef",
		Vout:         1,
		AmountSat:    1234,
		Address:      "bc1ptest",
		ScriptPubKey: "5120abcd",
		AddressType:  "p2tr",
		Status:       model.UTXOStatusAvailable,
		Source:       model.UTXOSourceChange,
	}
	if err := s.InsertChangeUTXO(ctx, messageID, changeUTXO); err != nil {
		t.Fatalf("InsertChangeUTXO() error = %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE utxos SET reserved_by_message_id = ?, confirm_height = ? WHERE txid = ? AND vout = ?`, messageID, height, changeUTXO.TxID, changeUTXO.Vout); err != nil {
		t.Fatalf("seed change utxo linkage error = %v", err)
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
		case "getblockcount":
			_, _ = w.Write([]byte(`{"result":100,"error":null}`))
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"newhash","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536},"error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536,"tx":[]},"error":null}`))
		default:
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-32601,"message":"method not found"}}`))
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

	var status string
	var confirmHeight uint64
	var spentByTxID sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT status, confirm_height, spent_by_txid FROM utxos WHERE txid = ? AND vout = ?`, changeUTXO.TxID, changeUTXO.Vout).Scan(&status, &confirmHeight, &spentByTxID); err != nil {
		t.Fatalf("query change utxo error = %v", err)
	}
	if status != string(model.UTXOStatusAvailable) {
		t.Fatalf("change utxo status = %q, want %q", status, model.UTXOStatusAvailable)
	}
	if confirmHeight != height {
		t.Fatalf("change utxo confirm_height = %d, want %d", confirmHeight, height)
	}
	if !spentByTxID.Valid || spentByTxID.String != "" {
		t.Fatalf("change utxo spent_by_txid = %v, want empty string", spentByTxID)
	}
}

func TestScanOnceKeepsChangeUTXOOnDeepReorgByConfirmHeight(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reorg-deep-prove-change-confirm-height.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	relatedHeight := uint64(100)
	confirmHeight := uint64(101)
	if err := s.UpsertBlock(ctx, confirmHeight, "oldhash", 539361536, 1, true, "confirmed"); err != nil {
		t.Fatalf("UpsertBlock() error = %v", err)
	}
	messageID, err := s.CreateMessage(ctx, "prove", "payload", &relatedHeight, "100:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSigned(ctx, messageID, "abcd"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, "deadbeef"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}
	if err := s.MarkMessageConfirmed(ctx, messageID, confirmHeight); err != nil {
		t.Fatalf("MarkMessageConfirmed() error = %v", err)
	}
	changeUTXO := model.UTXO{
		TxID:          "deadbeef",
		Vout:          1,
		AmountSat:     1234,
		Address:       "bc1ptest",
		ScriptPubKey:  "5120abcd",
		AddressType:   "p2tr",
		Status:        model.UTXOStatusSpentConfirmed,
		Source:        model.UTXOSourceChange,
		SpentByTxID:   "followup-txid",
		ConfirmHeight: confirmHeight,
	}
	if err := s.InsertChangeUTXO(ctx, messageID, changeUTXO); err != nil {
		t.Fatalf("InsertChangeUTXO() error = %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE utxos SET reserved_by_message_id = ? WHERE txid = ? AND vout = ?`, messageID, changeUTXO.TxID, changeUTXO.Vout); err != nil {
		t.Fatalf("seed change utxo linkage error = %v", err)
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
		case "getblockcount":
			_, _ = w.Write([]byte(`{"result":101,"error":null}`))
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"newhash","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":101,"confirmations":1,"version":539361536},"error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":101,"confirmations":1,"version":539361536,"tx":[]},"error":null}`))
		default:
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-32601,"message":"method not found"}}`))
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
		Config:      config.Config{Scan: config.ScanConfig{StartHeight: 101, TargetBlockVersion: 539361536, RequiredConfirmations: 1}},
		KeyMaterial: keyMaterial,
	}

	if err := engine.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusCommitConfirmed {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusCommitConfirmed)
	}
	if message.ConfirmHeight != confirmHeight {
		t.Fatalf("message confirm height = %d, want %d", message.ConfirmHeight, confirmHeight)
	}

	var status string
	var gotConfirmHeight uint64
	var spentByTxID sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT status, confirm_height, spent_by_txid FROM utxos WHERE txid = ? AND vout = ?`, changeUTXO.TxID, changeUTXO.Vout).Scan(&status, &gotConfirmHeight, &spentByTxID); err != nil {
		t.Fatalf("query change utxo error = %v", err)
	}
	if status != string(model.UTXOStatusSpentConfirmed) {
		t.Fatalf("change utxo status = %q, want %q", status, model.UTXOStatusSpentConfirmed)
	}
	if gotConfirmHeight != confirmHeight {
		t.Fatalf("change utxo confirm_height = %d, want %d", gotConfirmHeight, confirmHeight)
	}
	if !spentByTxID.Valid || spentByTxID.String != "followup-txid" {
		t.Fatalf("change utxo spent_by_txid = %v, want followup-txid", spentByTxID)
	}
}

func TestScanOnceKeepsIndexerIDWhenConfirmedRegisterIsReorged(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reorg-register-indexer.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(100)
	if err := s.UpsertBlock(ctx, height, "oldhash", 539361536, 1, true, "confirmed"); err != nil {
		t.Fatalf("UpsertBlock() error = %v", err)
	}
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
	if err := s.MarkMessageConfirmed(ctx, messageID, height); err != nil {
		t.Fatalf("MarkMessageConfirmed() error = %v", err)
	}
	if err := s.UpdateMessageConfirmationDetails(ctx, messageID, height, "100:0"); err != nil {
		t.Fatalf("UpdateMessageConfirmationDetails() error = %v", err)
	}
	if err := s.MarkRevealBroadcasted(ctx, messageID, "reveal-deadbeef"); err != nil {
		t.Fatalf("MarkRevealBroadcasted() error = %v", err)
	}
	if err := s.MarkRevealConfirmed(ctx, messageID, height); err != nil {
		t.Fatalf("MarkRevealConfirmed() error = %v", err)
	}
	if err := s.SetIndexerID(ctx, "100:0"); err != nil {
		t.Fatalf("SetIndexerID() error = %v", err)
	}
	storedIndexerID, err := s.GetChainState(ctx, "indexer_id")
	if err != nil {
		t.Fatalf("GetChainState() seed error = %v", err)
	}
	if storedIndexerID != "100:0" {
		t.Fatalf("seed indexer_id = %q, want 100:0", storedIndexerID)
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
		case "getblockcount":
			_, _ = w.Write([]byte(`{"result":100,"error":null}`))
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"newhash","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536},"error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536,"tx":[]},"error":null}`))
		default:
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-32601,"message":"method not found"}}`))
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
		Config:      config.Config{Scan: config.ScanConfig{StartHeight: 100, TargetBlockVersion: 1, RequiredConfirmations: 1}},
		KeyMaterial: keyMaterial,
	}

	if err := engine.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce() error = %v", err)
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusDone {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusDone)
	}
	if message.IndexerID != "100:0" {
		t.Fatalf("message indexer_id = %q, want 100:0", message.IndexerID)
	}
	if message.ConfirmHeight != height {
		t.Fatalf("confirm height = %d, want %d", message.ConfirmHeight, height)
	}
	indexerID, err := s.GetChainState(ctx, "indexer_id")
	if err != nil {
		t.Fatalf("GetChainState() error = %v", err)
	}
	if indexerID != "100:0" {
		t.Fatalf("indexer_id = %q, want 100:0", indexerID)
	}
}

func TestScanOnceKeepsReservedUTXOOnReorg(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reorg-utxo-msg.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	height := uint64(100)
	if err := s.UpsertBlock(ctx, height, "oldhash", 539361536, 1, true, "ready"); err != nil {
		t.Fatalf("UpsertBlock() error = %v", err)
	}
	if err := s.SeedInitialUTXOs(ctx, []config.InitialUTXO{{
		TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		Vout:         0,
		AmountSat:    5000,
		Address:      "bc1qtest",
		ScriptPubKey: "0014abcd",
		AddressType:  "p2wpkh",
	}}); err != nil {
		t.Fatalf("SeedInitialUTXOs() error = %v", err)
	}
	messageID, err := s.CreateMessage(ctx, "prove", "payload", &height, "100:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.ReserveUTXOs(ctx, messageID, []model.UTXO{{TxID: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", Vout: 0}}); err != nil {
		t.Fatalf("ReserveUTXOs() error = %v", err)
	}
	if err := s.MarkMessageSigned(ctx, messageID, "abcd"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
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
		case "getblockcount":
			_, _ = w.Write([]byte(`{"result":100,"error":null}`))
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"newhash","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536},"error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash","height":100,"confirmations":1,"version":539361536,"tx":[]},"error":null}`))
		default:
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-32601,"message":"method not found"}}`))
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
	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusCommitSigned {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusCommitSigned)
	}
	var status string
	var reservedByMessageID sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT status, reserved_by_message_id FROM utxos WHERE txid = ? AND vout = ?`, "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", 0).Scan(&status, &reservedByMessageID); err != nil {
		t.Fatalf("query utxo error = %v", err)
	}
	if status != string(model.UTXOStatusReserved) {
		t.Fatalf("utxo status = %q, want %q", status, model.UTXOStatusReserved)
	}
	if !reservedByMessageID.Valid || reservedByMessageID.Int64 != messageID {
		t.Fatalf("reserved_by_message_id = %v, want %d", reservedByMessageID, messageID)
	}
}

func TestScanOnceRewindsFromLastScannedAndCreatesNewMessageAfterReorg(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reorg-rewind-new-message.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	reorgHeight := uint64(100)
	if err := s.SetChainState(ctx, "last_scanned_height", "130"); err != nil {
		t.Fatalf("SetChainState(last_scanned_height) error = %v", err)
	}
	if err := s.SetIndexerID(ctx, "90:1"); err != nil {
		t.Fatalf("SetIndexerID() error = %v", err)
	}
	if err := s.UpsertBlock(ctx, reorgHeight, "oldhash", 539361536, 1, true, "ready"); err != nil {
		t.Fatalf("UpsertBlock() error = %v", err)
	}
	oldMessageID, err := s.CreateMessage(ctx, model.MessageTypeProve, "old-payload", &reorgHeight, "90:1")
	if err != nil {
		t.Fatalf("CreateMessage(old) error = %v", err)
	}
	if err := s.MarkMessageSigned(ctx, oldMessageID, "abcd"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, oldMessageID, "deadbeef"); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}
	if err := s.MarkMessageConfirmed(ctx, oldMessageID, reorgHeight); err != nil {
		t.Fatalf("MarkMessageConfirmed() error = %v", err)
	}

	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"blockhash":"newhash100","statehash":"state100"}`))
			return
		}
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "getblockcount":
			_, _ = w.Write([]byte(`{"result":130,"error":null}`))
		case "getblockhash":
			height := int(req.Params[0].(float64))
			if height == int(reorgHeight) {
				_, _ = w.Write([]byte(`{"result":"newhash100","error":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"result":"hash","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash100","height":100,"confirmations":31,"version":539361536},"error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"newhash100","height":100,"confirmations":31,"version":539361536,"tx":[]},"error":null}`))
		default:
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-32601,"message":"method not found"}}`))
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
		Config:      config.Config{Scan: config.ScanConfig{TargetBlockVersion: 539361536, RequiredConfirmations: 1, MaxReorgDepth: 30}},
		KeyMaterial: keyMaterial,
	}

	if err := engine.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce() error = %v", err)
	}

	oldMessage, err := s.GetMessage(ctx, oldMessageID)
	if err != nil {
		t.Fatalf("GetMessage(old) error = %v", err)
	}
	if oldMessage.Status != model.MessageStatusCommitConfirmed {
		t.Fatalf("old message status = %q, want %q", oldMessage.Status, model.MessageStatusCommitConfirmed)
	}

	var newMessageID int64
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM messages WHERE type = ? AND related_height = ? AND id != ?`, model.MessageTypeProve, reorgHeight, oldMessageID).Scan(&newMessageID); err != nil {
		t.Fatalf("query new message error = %v", err)
	}
	if newMessageID == oldMessageID || newMessageID == 0 {
		t.Fatalf("new message id = %d, old message id = %d", newMessageID, oldMessageID)
	}
}
