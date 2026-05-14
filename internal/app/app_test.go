package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/store"

	"github.com/btcsuite/btcd/chaincfg"
)

func testConfig(t *testing.T, dbPath string, rpcURL string) config.Config {
	t.Helper()

	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	seedScript, err := keyMaterial.P2WPKHScript(&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("P2WPKHScript() error = %v", err)
	}
	changeAddr, err := keyMaterial.Address(&chaincfg.MainNetParams, "p2tr")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}

	feeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fastestFee":12,"halfHourFee":8,"hourFee":4,"minimumFee":2}`))
	}))
	t.Cleanup(feeServer.Close)

	stateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"blockhash":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","statehash":"ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"}`))
	}))
	t.Cleanup(stateServer.Close)

	return config.Config{
		BitcoinRPC: config.BitcoinRPCConfig{URL: rpcURL, Network: "mainnet"},
		Signing: config.SigningConfig{
			PrivateKeyHex: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			ChangeAddress: changeAddr,
			InitialUTXOs: []config.InitialUTXO{{
				TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
				Vout:         0,
				AmountSat:    5000,
				Address:      "bc1qtest",
				ScriptPubKey: hex.EncodeToString(seedScript),
				AddressType:  "p2wpkh",
			}},
		},
		StateAPI: config.StateAPIConfig{BaseURL: stateServer.URL, Timeout: time.Second},
		FeeAPI:   config.FeeAPIConfig{BaseURL: feeServer.URL, Timeout: time.Second, Strategy: "half_hour", MinFeeRateSatVB: 1, MaxFeeRateSatVB: 20},
		Register: config.RegisterConfig{IndexRatioBP: 100, RewardAddrType: "p2tr", RewardAddr: changeAddr, Name: "bootstrap"},
		Scan:     config.ScanConfig{StartHeight: 0, PollInterval: time.Second, TargetBlockVersion: 0x20260100, RequiredConfirmations: 1, MaxReorgDepth: 6},
		Tx:       config.TxConfig{SendChangeMinValue: 546},
		Database: config.DatabaseConfig{SQLitePath: dbPath},
		Runtime:  config.RuntimeConfig{},
	}
}

func TestRunModeRegisterCreatesRegisterMessage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "register.db")
	sendRawCalls := 0
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "sendrawtransaction":
			sendRawCalls++
			_, _ = w.Write([]byte(`{"result":"regtxid","error":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer rpcServer.Close()

	cfg := testConfig(t, dbPath, rpcServer.URL)
	if err := RunMode(context.Background(), cfg, "register"); err != nil {
		t.Fatalf("RunMode(register) error = %v", err)
	}
	if sendRawCalls != 1 {
		t.Fatalf("sendrawtransaction calls = %d, want 1", sendRawCalls)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	message, err := s.GetLatestMessageByType(context.Background(), "register", "commit_sent")
	if err != nil {
		t.Fatalf("GetLatestMessageByType() error = %v", err)
	}
	if message.ID == 0 {
		t.Fatal("expected register message to be created")
	}
	if message.TxID != "regtxid" {
		t.Fatalf("register txid = %q, want regtxid", message.TxID)
	}
}

func TestRunAutoBootstrapSkipsDuplicateRegister(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bootstrap.db")
	sendRawCalls := 0
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "sendrawtransaction":
			sendRawCalls++
			_, _ = w.Write([]byte(`{"result":"regtxid","error":null}`))
		case "getblockcount":
			_, _ = w.Write([]byte(`{"result":1,"error":null}`))
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"block1","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"block1","height":1,"confirmations":1,"version":0},"error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"block1","height":1,"confirmations":1,"version":0,"tx":["regtxid"]},"error":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer rpcServer.Close()

	cfg := testConfig(t, dbPath, rpcServer.URL)
	cfg.Runtime.DryRun = true
	ctx := context.Background()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run() first error = %v", err)
	}
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run() second error = %v", err)
	}
	if sendRawCalls != 0 {
		t.Fatalf("sendrawtransaction calls = %d, want 0 in dry-run", sendRawCalls)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("db path exists = %v, want no file created in dry-run", err == nil)
	}
}

func TestEnsureRegisteredUsesConfiguredIndexerID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "configured-indexer.db")
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
			_, _ = w.Write([]byte(`{"result":1,"error":null}`))
		case "getblockhash":
			_, _ = w.Write([]byte(`{"result":"block1","error":null}`))
		case "getblockheader":
			_, _ = w.Write([]byte(`{"result":{"hash":"block1","height":1,"confirmations":1,"version":0},"error":null}`))
		case "getblock":
			_, _ = w.Write([]byte(`{"result":{"hash":"block1","height":1,"confirmations":1,"version":0,"tx":[]},"error":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer rpcServer.Close()

	cfg := testConfig(t, dbPath, rpcServer.URL)
	cfg.Register.IndexerID = "42457:2"
	cfg.Scan.PollInterval = time.Hour

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := RunMode(ctx, cfg, "run"); err != nil {
		t.Fatalf("RunMode(run) error = %v", err)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	indexerID, err := s.GetChainState(context.Background(), "indexer_id")
	if err != nil {
		t.Fatalf("GetChainState() error = %v", err)
	}
	if indexerID != "42457:2" {
		t.Fatalf("indexer_id = %q, want 42457:2", indexerID)
	}
}

func TestStartHealthServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "health.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	if err := s.SetChainState(ctx, "indexer_id", "42457:2"); err != nil {
		t.Fatalf("SetChainState(indexer_id) error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_scanned_height", "42457"); err != nil {
		t.Fatalf("SetChainState(last_scanned_height) error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_seen_tip", "42458"); err != nil {
		t.Fatalf("SetChainState(last_seen_tip) error = %v", err)
	}

	if _, err := s.CreateMessage(ctx, "register", "payload1", nil, ""); err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	doneID, err := s.CreateMessage(ctx, "prove", "payload2", nil, "")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageConfirmed(ctx, doneID, 100); err != nil {
		t.Fatalf("MarkMessageConfirmed() error = %v", err)
	}
	if err := s.MarkRevealBroadcasted(ctx, doneID, "revealtxid"); err != nil {
		t.Fatalf("MarkRevealBroadcasted() error = %v", err)
	}
	if err := s.MarkRevealConfirmed(ctx, doneID, 101); err != nil {
		t.Fatalf("MarkRevealConfirmed() error = %v", err)
	}

	if err := startHealthServer(ctx, "127.0.0.1:18089", s, "run"); err != nil {
		t.Fatalf("startHealthServer() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:18089/healthz")
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "ok" {
			t.Fatalf("body = %q, want ok", string(body))
		}
		break
	}
	if lastErr != nil {
		t.Fatalf("health endpoint not ready: %v", lastErr)
	}

	statusResp, err := http.Get("http://127.0.0.1:18089/status")
	if err != nil {
		t.Fatalf("GET /status error = %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("/status code = %d, want 200", statusResp.StatusCode)
	}
	var status struct {
		OK                   bool   `json:"ok"`
		Mode                 string `json:"mode"`
		ProgressPendingCount int64  `json:"progress_pending_count"`
		DoneCount            int64  `json:"done_count"`
		FailedCount          int64  `json:"failed_count"`
		IndexerID            string `json:"indexer_id"`
		LastScannedHeight    string `json:"last_scanned_height"`
		LastSeenTip          string `json:"last_seen_tip"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /status response error = %v", err)
	}
	if !status.OK {
		t.Fatal("/status ok = false, want true")
	}
	if status.Mode != "run" {
		t.Fatalf("/status mode = %q, want run", status.Mode)
	}
	if status.ProgressPendingCount != 1 {
		t.Fatalf("/status progress_pending_count = %d, want 1", status.ProgressPendingCount)
	}
	if status.DoneCount != 1 {
		t.Fatalf("/status done_count = %d, want 1", status.DoneCount)
	}
	if status.FailedCount != 0 {
		t.Fatalf("/status failed_count = %d, want 0", status.FailedCount)
	}
	if status.IndexerID != "42457:2" {
		t.Fatalf("/status indexer_id = %q, want 42457:2", status.IndexerID)
	}
	if status.LastScannedHeight != "42457" {
		t.Fatalf("/status last_scanned_height = %q, want 42457", status.LastScannedHeight)
	}
	if status.LastSeenTip != "42458" {
		t.Fatalf("/status last_seen_tip = %q, want 42458", status.LastSeenTip)
	}
}

func TestMessageRebuildAdminEndpointResetsMessageOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "message-rebuild.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	if err := s.SetChainState(ctx, "last_scanned_height", "1764271"); err != nil {
		t.Fatalf("SetChainState(last_scanned_height) error = %v", err)
	}
	height := uint64(1764241)
	messageID, err := s.CreateMessage(ctx, model.MessageTypeProve, "payload", &height, "1761438:1")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSignedWithReveal(ctx, messageID, "commitraw", "revealraw", "revealtxid"); err != nil {
		t.Fatalf("MarkMessageSignedWithReveal() error = %v", err)
	}

	if err := startHealthServer(ctx, "127.0.0.1:18091", s, "run"); err != nil {
		t.Fatalf("startHealthServer() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Post(
			"http://127.0.0.1:18091/admin/message/rebuild",
			"application/json",
			bytes.NewBufferString(`{"message_id":`+strconv.FormatInt(messageID, 10)+`}`),
		)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("POST /admin/message/rebuild error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/admin/message/rebuild code = %d body=%q, want 200", resp.StatusCode, string(body))
	}

	message, err := s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.Status != model.MessageStatusBuilding {
		t.Fatalf("message status = %q, want %q", message.Status, model.MessageStatusBuilding)
	}
	if message.RawTxHex != "" || message.RevealRawTxHex != "" || message.RevealTxID != "" {
		t.Fatalf("signed tx fields were not cleared: raw=%q reveal_raw=%q reveal_txid=%q", message.RawTxHex, message.RevealRawTxHex, message.RevealTxID)
	}
	lastScanned, err := s.GetChainState(ctx, "last_scanned_height")
	if err != nil {
		t.Fatalf("GetChainState(last_scanned_height) error = %v", err)
	}
	if lastScanned != "1764271" {
		t.Fatalf("last_scanned_height = %q, want unchanged 1764271", lastScanned)
	}
}
