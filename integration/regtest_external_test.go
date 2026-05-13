package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"fractal-proof-publisher/internal/bitcoinrpc"
	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/feeapi"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/service"
	"fractal-proof-publisher/internal/stateapi"
	"fractal-proof-publisher/internal/store"
	"fractal-proof-publisher/internal/txbuilder"

	"github.com/btcsuite/btcd/chaincfg"
)

type managedBitcoind struct {
	cmd     *exec.Cmd
	rpcURL  string
	rpcUser string
	rpcPass string
	dataDir string
}

type walletRPC struct {
	baseURL  string
	user     string
	pass     string
	wallet   string
	endpoint string
}

type fundingResult struct {
	InitialUTXO config.InitialUTXO
	MiningAddr  string
	FundingTxID string
}

func TestManagedRegtestFixtureSeedsRealInitialUTXO(t *testing.T) {
	fixture, cleanup := buildManagedExternalRegtestFixture(t)
	if fixture == nil {
		t.Skip("bitcoind binary not available and no external regtest RPC configured")
	}
	defer cleanup()

	if err := fixture.cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(fixture.cfg.Signing.InitialUTXOs) != 1 {
		t.Fatalf("initial utxo count = %d, want 1", len(fixture.cfg.Signing.InitialUTXOs))
	}

	utxo := fixture.cfg.Signing.InitialUTXOs[0]
	if len(strings.TrimSpace(utxo.TxID)) != 64 {
		t.Fatalf("initial utxo txid = %q, want 64-char hex", utxo.TxID)
	}
	if utxo.AmountSat <= 0 {
		t.Fatalf("initial utxo amount = %d, want > 0", utxo.AmountSat)
	}
	if utxo.Address != fixture.cfg.Signing.ChangeAddress {
		t.Fatalf("initial utxo address = %q, want %q", utxo.Address, fixture.cfg.Signing.ChangeAddress)
	}
	if utxo.ScriptPubKey != fixture.changeScript {
		t.Fatalf("initial utxo script = %q, want %q", utxo.ScriptPubKey, fixture.changeScript)
	}
	if utxo.AddressType != fixture.changeType {
		t.Fatalf("initial utxo address type = %q, want %q", utxo.AddressType, fixture.changeType)
	}

	s := openFixtureStore(t, fixture)
	defer s.DB.Close()
	if err := s.SeedInitialUTXOs(context.Background(), fixture.cfg.Signing.InitialUTXOs); err != nil {
		t.Fatalf("SeedInitialUTXOs() error = %v", err)
	}
	available, err := s.ListAvailableUTXOs(context.Background())
	if err != nil {
		t.Fatalf("ListAvailableUTXOs() error = %v", err)
	}
	if len(available) != 1 {
		t.Fatalf("available utxo count = %d, want 1", len(available))
	}
	if available[0].TxID != utxo.TxID {
		t.Fatalf("available utxo txid = %q, want %q", available[0].TxID, utxo.TxID)
	}
}

func buildManagedExternalRegtestFixture(t *testing.T) (*regtestFixture, func()) {
	t.Helper()

	env := loadRegtestEnv(t)
	if strings.TrimSpace(env.RPCURL) != "" {
		node := &managedBitcoind{
			rpcURL:  env.RPCURL,
			rpcUser: env.RPCUser,
			rpcPass: env.RPCPass,
		}
		keyMaterial, changeAddr, changeScript, changeType := deriveRegtestKeyMaterial(t, env.PrivateKey, env.ChangeAddr)
		env.PrivateKey = keyMaterial.PrivateKeyHex
		env.ChangeAddr = changeAddr

		state := newRegtestStateStub(t, map[uint64]stateResponse{}, nil)
		fee := newRegtestFeeStub(t, feeResponse{FastestFee: 9, HalfHourFee: 5, HourFee: 3, MinimumFee: 2}, nil)
		env.StateAPIURL = state.server.URL
		env.FeeAPIURL = fee.server.URL

		funding := fundPublisherInitialUTXO(t, node, changeAddr, changeScript, changeType)
		cfg := buildRegtestConfig(env, changeScript, changeType)
		cfg.Signing.InitialUTXOs = []config.InitialUTXO{funding.InitialUTXO}
		cfg.Runtime.DisableBroadcast = false
		cfg.Runtime.DryRun = false
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}

		fixture := &regtestFixture{
			env:          env,
			cfg:          cfg,
			keyMaterial:  keyMaterial,
			changeScript: changeScript,
			changeType:   changeType,
			state:        state,
			fee:          fee,
		}
		return fixture, func() {}
	}

	if node := detectRunningDefaultRegtest(t); node != nil {
		keyMaterial, changeAddr, changeScript, changeType := deriveRegtestKeyMaterial(t, env.PrivateKey, env.ChangeAddr)
		env.PrivateKey = keyMaterial.PrivateKeyHex
		env.ChangeAddr = changeAddr
		env.RPCURL = node.rpcURL
		env.RPCUser = node.rpcUser
		env.RPCPass = node.rpcPass

		state := newRegtestStateStub(t, map[uint64]stateResponse{}, nil)
		fee := newRegtestFeeStub(t, feeResponse{FastestFee: 9, HalfHourFee: 5, HourFee: 3, MinimumFee: 2}, nil)
		env.StateAPIURL = state.server.URL
		env.FeeAPIURL = fee.server.URL

		funding := fundPublisherInitialUTXO(t, node, changeAddr, changeScript, changeType)
		cfg := buildRegtestConfig(env, changeScript, changeType)
		cfg.Signing.InitialUTXOs = []config.InitialUTXO{funding.InitialUTXO}
		cfg.Runtime.DisableBroadcast = false
		cfg.Runtime.DryRun = false
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}

		fixture := &regtestFixture{
			env:          env,
			cfg:          cfg,
			keyMaterial:  keyMaterial,
			changeScript: changeScript,
			changeType:   changeType,
			state:        state,
			fee:          fee,
		}
		return fixture, func() {}
	}

	bitcoindBin := strings.TrimSpace(os.Getenv("REGTEST_BITCOIND_BIN"))
	if bitcoindBin == "" {
		path, err := exec.LookPath("bitcoind")
		if err != nil {
			return nil, nil
		}
		bitcoindBin = path
	}

	node := startManagedBitcoind(t, bitcoindBin)
	keyMaterial, changeAddr, changeScript, changeType := deriveRegtestKeyMaterial(t, env.PrivateKey, env.ChangeAddr)
	env.PrivateKey = keyMaterial.PrivateKeyHex
	env.ChangeAddr = changeAddr
	env.RPCURL = node.rpcURL
	env.RPCUser = node.rpcUser
	env.RPCPass = node.rpcPass

	state := newRegtestStateStub(t, map[uint64]stateResponse{}, nil)
	fee := newRegtestFeeStub(t, feeResponse{FastestFee: 9, HalfHourFee: 5, HourFee: 3, MinimumFee: 2}, nil)
	env.StateAPIURL = state.server.URL
	env.FeeAPIURL = fee.server.URL

	funding := fundPublisherInitialUTXO(t, node, changeAddr, changeScript, changeType)
	cfg := buildRegtestConfig(env, changeScript, changeType)
	cfg.Signing.InitialUTXOs = []config.InitialUTXO{funding.InitialUTXO}
	cfg.Runtime.DisableBroadcast = false
	cfg.Runtime.DryRun = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	fixture := &regtestFixture{
		env:          env,
		cfg:          cfg,
		keyMaterial:  keyMaterial,
		changeScript: changeScript,
		changeType:   changeType,
		state:        state,
		fee:          fee,
	}
	cleanup := func() {
		stopManagedBitcoind(t, node)
	}
	return fixture, cleanup
}

func detectRunningDefaultRegtest(t *testing.T) *managedBitcoind {
	t.Helper()

	node := &managedBitcoind{
		rpcURL:  "http://127.0.0.1:19443",
		rpcUser: "regtest",
		rpcPass: "regtestpass",
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := rawRPC(ctx, node.rpcURL, node.rpcUser, node.rpcPass, "getblockchaininfo", nil)
	if err != nil {
		return nil
	}
	var info struct {
		Chain string `json:"chain"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil
	}
	if info.Chain != "regtest" {
		return nil
	}
	return node
}

func startManagedBitcoind(t *testing.T, bitcoindBin string) *managedBitcoind {
	t.Helper()

	dataDir := filepath.Join(t.TempDir(), "bitcoind")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", dataDir, err)
	}
	rpcUser := "regtest-user"
	rpcPass := "regtest-pass"
	rpcPort := "18443"
	conf := strings.Join([]string{
		"regtest=1",
		"server=1",
		"fallbackfee=0.0002",
		"txindex=1",
		"rpcbind=127.0.0.1",
		"rpcallowip=127.0.0.1",
		"rpcuser=" + rpcUser,
		"rpcpassword=" + rpcPass,
		"rpcport=" + rpcPort,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dataDir, "bitcoin.conf"), []byte(conf), 0o644); err != nil {
		t.Fatalf("WriteFile(bitcoin.conf) error = %v", err)
	}

	cmd := exec.Command(bitcoindBin, "-datadir="+dataDir, "-noprinttoconsole")
	logFile, err := os.Create(filepath.Join(dataDir, "bitcoind.log"))
	if err != nil {
		t.Fatalf("Create(bitcoind.log) error = %v", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("bitcoind failed to start: %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	node := &managedBitcoind{
		cmd:     cmd,
		rpcURL:  "http://127.0.0.1:" + rpcPort,
		rpcUser: rpcUser,
		rpcPass: rpcPass,
		dataDir: dataDir,
	}
	waitForRPCReady(t, node, 20*time.Second)
	return node
}

func stopManagedBitcoind(t *testing.T, node *managedBitcoind) {
	t.Helper()
	if node == nil || node.cmd == nil || node.cmd.Process == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = rawRPC(ctx, node.rpcURL, node.rpcUser, node.rpcPass, "stop", nil)

	done := make(chan error, 1)
	go func() {
		done <- node.cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = node.cmd.Process.Kill()
		<-done
	case <-done:
	}
}

func waitForRPCReady(t *testing.T, node *managedBitcoind, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := rawRPC(ctx, node.rpcURL, node.rpcUser, node.rpcPass, "getblockcount", nil)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("bitcoind RPC did not become ready before timeout")
}

func fundPublisherInitialUTXO(t *testing.T, node *managedBitcoind, changeAddr, changeScript, changeType string) fundingResult {
	t.Helper()

	wallet := newWalletRPC(node, "miner")
	ctx := context.Background()
	if err := wallet.ensureWallet(ctx); err != nil {
		t.Fatalf("ensureWallet() error = %v", err)
	}
	miningAddr, err := wallet.getNewAddress(ctx, "bech32")
	if err != nil {
		t.Fatalf("getNewAddress(miner) error = %v", err)
	}
	if err := wallet.generateToAddress(ctx, 101, miningAddr); err != nil {
		t.Fatalf("generateToAddress(101) error = %v", err)
	}

	txid, err := wallet.sendToAddress(ctx, changeAddr, 1.0)
	if err != nil {
		t.Fatalf("sendToAddress(%q) error = %v", changeAddr, err)
	}
	if err := wallet.generateToAddress(ctx, 1, miningAddr); err != nil {
		t.Fatalf("generateToAddress(1) error = %v", err)
	}

	tx, err := wallet.getRawTransaction(ctx, txid)
	if err != nil {
		t.Fatalf("getRawTransaction(%q) error = %v", txid, err)
	}
	for _, out := range tx.Vout {
		if out.ScriptPubKey.Hex != changeScript {
			continue
		}
		amountSat := int64(out.ValueBTC*100_000_000 + 0.5)
		return fundingResult{
			MiningAddr:  miningAddr,
			FundingTxID: txid,
			InitialUTXO: config.InitialUTXO{
				TxID:         txid,
				Vout:         out.N,
				AmountSat:    amountSat,
				Address:      changeAddr,
				ScriptPubKey: changeScript,
				AddressType:  changeType,
			},
		}
	}

	t.Fatalf("funding tx %s does not contain output script %s", txid, changeScript)
	return fundingResult{}
}

type rawTransactionVerbose struct {
	TxID string `json:"txid"`
	Vout []struct {
		N            uint32  `json:"n"`
		ValueBTC     float64 `json:"value"`
		ScriptPubKey struct {
			Hex string `json:"hex"`
		} `json:"scriptPubKey"`
	} `json:"vout"`
}

func newWalletRPC(node *managedBitcoind, wallet string) *walletRPC {
	baseURL := strings.TrimRight(node.rpcURL, "/")
	return &walletRPC{
		baseURL:  baseURL,
		user:     node.rpcUser,
		pass:     node.rpcPass,
		wallet:   wallet,
		endpoint: baseURL + "/wallet/" + url.PathEscape(wallet),
	}
}

func (w *walletRPC) ensureWallet(ctx context.Context) error {
	if _, err := rawRPC(ctx, w.endpoint, w.user, w.pass, "getwalletinfo", nil); err == nil {
		return nil
	}
	_, err := rawRPC(ctx, w.baseURL, w.user, w.pass, "createwallet", []any{w.wallet})
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "Database already exists") {
		_, loadErr := rawRPC(ctx, w.baseURL, w.user, w.pass, "loadwallet", []any{w.wallet})
		if loadErr == nil || strings.Contains(loadErr.Error(), "already loaded") {
			return nil
		}
		return loadErr
	}
	if strings.Contains(err.Error(), "already loaded") {
		return nil
	}
	return err
}

func (w *walletRPC) getNewAddress(ctx context.Context, addressType string) (string, error) {
	var addr string
	if err := rpcInto(ctx, w.endpoint, w.user, w.pass, "getnewaddress", []any{"", addressType}, &addr); err != nil {
		return "", err
	}
	return addr, nil
}

func (w *walletRPC) generateToAddress(ctx context.Context, blocks int, address string) error {
	_, err := rawRPC(ctx, w.baseURL, w.user, w.pass, "generatetoaddress", []any{blocks, address})
	return err
}

func (w *walletRPC) sendToAddress(ctx context.Context, address string, amountBTC float64) (string, error) {
	var txid string
	if err := rpcInto(ctx, w.endpoint, w.user, w.pass, "sendtoaddress", []any{address, amountBTC}, &txid); err != nil {
		return "", err
	}
	return txid, nil
}

func (w *walletRPC) getRawTransaction(ctx context.Context, txid string) (rawTransactionVerbose, error) {
	var result rawTransactionVerbose
	if err := rpcInto(ctx, w.baseURL, w.user, w.pass, "getrawtransaction", []any{txid, true}, &result); err != nil {
		return rawTransactionVerbose{}, err
	}
	return result, nil
}

type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func rawRPC(ctx context.Context, endpoint, user, pass, method string, params []any) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      "regtest",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal rpc request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new rpc request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" || pass != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpc %s request: %w", method, err)
	}
	defer resp.Body.Close()

	var envelope rpcEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode rpc %s response: %w", method, err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("rpc %s: %d %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result, nil
}

func rpcInto(ctx context.Context, endpoint, user, pass, method string, params []any, out any) error {
	raw, err := rawRPC(ctx, endpoint, user, pass, method, params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode rpc %s result: %w", method, err)
	}
	return nil
}

func TestDeriveRegtestKeyMaterialMatchesRealScriptShape(t *testing.T) {
	keyMaterial, addr, scriptHex, addressType := deriveRegtestKeyMaterial(t, "", "")
	if !strings.HasPrefix(addr, "bcrt1") {
		t.Fatalf("derived regtest address = %q, want bcrt1 prefix", addr)
	}
	if addressType == "" {
		t.Fatal("derived address type is empty")
	}
	script, err := hex.DecodeString(scriptHex)
	if err != nil {
		t.Fatalf("DecodeString(scriptHex) error = %v", err)
	}
	wantScript, err := txbuilder.ScriptPubKeyHexForAddress(addr, &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatalf("ScriptPubKeyHexForAddress() error = %v", err)
	}
	if hex.EncodeToString(script) != wantScript {
		t.Fatalf("script hex = %q, want %q", hex.EncodeToString(script), wantScript)
	}
	if _, err := keys.Load("", keyMaterial.PrivateKeyHex); err != nil {
		t.Fatalf("Load(privateKeyHex) error = %v", err)
	}
}

func TestManagedRegtestRegisterProveRevealHappyPath(t *testing.T) {
	fixture, cleanup := buildManagedExternalRegtestFixture(t)
	if fixture == nil {
		t.Skip("bitcoind binary not available and no external/default regtest RPC configured")
	}
	defer cleanup()

	s := openFixtureStore(t, fixture)
	defer s.DB.Close()
	ctx := context.Background()
	if err := s.SeedInitialUTXOs(ctx, fixture.cfg.Signing.InitialUTXOs); err != nil {
		t.Fatalf("SeedInitialUTXOs() error = %v", err)
	}

	engine := newExternalEngine(t, fixture, s)
	node := &managedBitcoind{
		rpcURL:  fixture.env.RPCURL,
		rpcUser: fixture.env.RPCUser,
		rpcPass: fixture.env.RPCPass,
	}
	miner := newWalletRPC(node, "miner")
	if err := miner.ensureWallet(ctx); err != nil {
		t.Fatalf("ensureWallet(miner) error = %v", err)
	}
	miningAddr, err := miner.getNewAddress(ctx, "bech32")
	if err != nil {
		t.Fatalf("getNewAddress(miner) error = %v", err)
	}
	logRegtestReport(t, "fixture", map[string]any{
		"rpc_url":        fixture.env.RPCURL,
		"change_address": fixture.cfg.Signing.ChangeAddress,
		"initial_utxo":   fixture.cfg.Signing.InitialUTXOs[0],
		"miner_address":  miningAddr,
	})

	registerTxID, err := engine.RunRegister(ctx, fixture.cfg.Register.IndexRatioBP, fixture.cfg.Register.RewardAddrType, fixture.cfg.Register.RewardAddr, fixture.cfg.Register.Name)
	if err != nil {
		t.Fatalf("RunRegister() error = %v", err)
	}
	if registerTxID == "" {
		t.Fatal("register txid is empty")
	}
	logRegtestReport(t, "register_broadcast", map[string]any{
		"txid":             registerTxID,
		"reward_address":   fixture.cfg.Register.RewardAddr,
		"reward_addr_type": fixture.cfg.Register.RewardAddrType,
		"name":             fixture.cfg.Register.Name,
	})

	if err := miner.generateToAddress(ctx, 1, miningAddr); err != nil {
		t.Fatalf("generateToAddress(confirm register) error = %v", err)
	}
	tip, targetHeight, targetHash, targetVersion := mineEligibleTargetBlock(t, ctx, node, miner, miningAddr)
	logRegtestReport(t, "chain_progress", map[string]any{
		"tip_after_register_confirm": tip,
		"target_height":              targetHeight,
		"target_hash":                targetHash,
		"target_version":             fmt.Sprintf("0x%x", targetVersion),
	})
	setStateHeightResponse(fixture.state, targetHeight, stateResponse{
		BlockHash: targetHash,
		StateHash: strings.Repeat("1", 64),
	})
	if err := s.SetChainState(ctx, "last_scanned_height", strconv.FormatUint(tip, 10)); err != nil {
		t.Fatalf("SetChainState(last_scanned_height=%d) error = %v", tip, err)
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce(register) error = %v", err)
	}

	registerMessage := findMessageByType(t, s, model.MessageTypeRegister)
	if registerMessage.Status != model.MessageStatusRevealSent {
		t.Fatalf("register status = %q, want %q", registerMessage.Status, model.MessageStatusRevealSent)
	}
	indexerID := chainState(t, s, "indexer_id")
	if indexerID == "" {
		t.Fatal("indexer_id is empty after register confirmation")
	}
	logRegtestReport(t, "register_confirmed", map[string]any{
		"txid":           registerMessage.TxID,
		"confirm_height": registerMessage.ConfirmHeight,
		"indexer_id":     indexerID,
	})

	engine.Config.Scan.StartHeight = targetHeight
	engine.Config.Scan.TargetBlockVersion = targetVersion
	if err := engine.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce() error = %v", err)
	}
	if err := engine.ProgressOnce(ctx); err != nil {
		t.Fatalf("ProgressOnce(after ScanOnce) error = %v", err)
	}

	proveMessages := findMessagesByType(t, s, model.MessageTypeProve)
	if len(proveMessages) != 1 {
		t.Fatalf("prove message count = %d, want 1", len(proveMessages))
	}
	proveMessage := proveMessages[0]
	if proveMessage.RelatedHeight != targetHeight {
		t.Fatalf("prove related_height = %d, want %d", proveMessage.RelatedHeight, targetHeight)
	}
	if proveMessage.IndexerID != indexerID {
		t.Fatalf("prove indexer_id = %q, want %q", proveMessage.IndexerID, indexerID)
	}
	logRegtestReport(t, "prove_broadcast", map[string]any{
		"txid":           proveMessage.TxID,
		"related_height": proveMessage.RelatedHeight,
		"indexer_id":     proveMessage.IndexerID,
		"payload":        proveMessage.PayloadText,
	})
	if proveMessage.RevealTxID == "" {
		t.Fatal("reveal txid is empty before commit confirmation")
	}
	if proveMessage.RevealConfirmHeight != 0 {
		t.Fatalf("reveal confirm height before commit confirmation = %d, want 0", proveMessage.RevealConfirmHeight)
	}
	logRegtestReport(t, "reveal_preconfirm", map[string]any{
		"txid":   proveMessage.RevealTxID,
		"status": proveMessage.Status,
	})

	if err := miner.generateToAddress(ctx, 1, miningAddr); err != nil {
		t.Fatalf("generateToAddress(confirm prove commit) error = %v", err)
	}
	tip, err = currentTip(ctx, node)
	if err != nil {
		t.Fatalf("currentTip() after prove confirm error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_scanned_height", strconv.FormatUint(tip, 10)); err != nil {
		t.Fatalf("SetChainState(last_scanned_height=%d) error = %v", tip, err)
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce(prove commit) error = %v", err)
	}

	proveMessage = findMessageByType(t, s, model.MessageTypeProve)
	if proveMessage.Status != model.MessageStatusRevealSent {
		t.Fatalf("prove status = %q, want %q", proveMessage.Status, model.MessageStatusRevealSent)
	}
	proveMessage = findMessageByType(t, s, model.MessageTypeProve)
	if proveMessage.RevealTxID == "" {
		attempts, attemptsErr := s.ListBroadcastAttemptsByMessage(ctx, proveMessage.ID)
		if attemptsErr != nil {
			t.Fatalf("reveal txid empty, attempts query error = %v", attemptsErr)
		}
		t.Fatalf("reveal txid empty after commit confirmation, attempts=%+v", attempts)
	}
	logRegtestReport(t, "reveal_broadcast", map[string]any{
		"txid":          proveMessage.RevealTxID,
		"status":        proveMessage.Status,
		"parent_commit": proveMessage.TxID,
	})

	if err := miner.generateToAddress(ctx, 1, miningAddr); err != nil {
		t.Fatalf("generateToAddress(confirm reveal) error = %v", err)
	}
	tip, err = currentTip(ctx, node)
	if err != nil {
		t.Fatalf("currentTip() after reveal confirm error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_scanned_height", strconv.FormatUint(tip, 10)); err != nil {
		t.Fatalf("SetChainState(last_scanned_height=%d) error = %v", tip, err)
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce(reveal) error = %v", err)
	}

	proveMessage = findMessageByType(t, s, model.MessageTypeProve)
	if proveMessage.RevealConfirmHeight == 0 {
		t.Fatal("reveal confirm height is empty")
	}
	logRegtestReport(t, "reveal_confirmed", map[string]any{
		"txid":           proveMessage.RevealTxID,
		"confirm_height": proveMessage.RevealConfirmHeight,
		"commit_txid":    proveMessage.TxID,
	})

	available, err := s.ListAvailableUTXOs(ctx)
	if err != nil {
		t.Fatalf("ListAvailableUTXOs() error = %v", err)
	}
	if len(available) == 0 {
		t.Fatal("expected at least one available change utxo after prove confirmation")
	}
	logRegtestReport(t, "available_utxos", available)
}

func TestManagedRegtestMempoolSequence(t *testing.T) {
	fixture, cleanup := buildManagedExternalRegtestFixture(t)
	if fixture == nil {
		t.Skip("bitcoind binary not available and no external/default regtest RPC configured")
	}
	defer cleanup()

	s := openFixtureStore(t, fixture)
	defer s.DB.Close()
	ctx := context.Background()
	if err := s.SeedInitialUTXOs(ctx, fixture.cfg.Signing.InitialUTXOs); err != nil {
		t.Fatalf("SeedInitialUTXOs() error = %v", err)
	}

	engine := newExternalEngine(t, fixture, s)
	node := &managedBitcoind{
		rpcURL:  fixture.env.RPCURL,
		rpcUser: fixture.env.RPCUser,
		rpcPass: fixture.env.RPCPass,
	}
	miner := newWalletRPC(node, "miner")
	if err := miner.ensureWallet(ctx); err != nil {
		t.Fatalf("ensureWallet(miner) error = %v", err)
	}
	miningAddr, err := miner.getNewAddress(ctx, "bech32")
	if err != nil {
		t.Fatalf("getNewAddress(miner) error = %v", err)
	}

	registerTxID, err := engine.RunRegister(ctx, fixture.cfg.Register.IndexRatioBP, fixture.cfg.Register.RewardAddrType, fixture.cfg.Register.RewardAddr, fixture.cfg.Register.Name)
	if err != nil {
		t.Fatalf("RunRegister() error = %v", err)
	}
	assertMempoolContains(t, ctx, node, registerTxID)
	logRegtestReport(t, "mempool_after_register", map[string]any{
		"register_txid": registerTxID,
		"mempool":       mustRawMempool(t, ctx, node),
	})

	if err := miner.generateToAddress(ctx, 1, miningAddr); err != nil {
		t.Fatalf("generateToAddress(confirm register) error = %v", err)
	}
	tip, targetHeight, targetHash, targetVersion := mineEligibleTargetBlock(t, ctx, node, miner, miningAddr)
	setStateHeightResponse(fixture.state, targetHeight, stateResponse{
		BlockHash: targetHash,
		StateHash: strings.Repeat("2", 64),
	})
	if err := s.SetChainState(ctx, "last_scanned_height", strconv.FormatUint(tip, 10)); err != nil {
		t.Fatalf("SetChainState(last_scanned_height=%d) error = %v", tip, err)
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce(register) error = %v", err)
	}

	engine.Config.Scan.StartHeight = targetHeight
	engine.Config.Scan.TargetBlockVersion = targetVersion
	if err := engine.ScanOnce(ctx); err != nil {
		t.Fatalf("ScanOnce() error = %v", err)
	}
	if err := engine.ProgressOnce(ctx); err != nil {
		t.Fatalf("ProgressOnce(after ScanOnce) error = %v", err)
	}

	proveMessage := findMessageByType(t, s, model.MessageTypeProve)
	assertMempoolContains(t, ctx, node, proveMessage.TxID)
	logRegtestReport(t, "mempool_after_prove_commit", map[string]any{
		"prove_txid": proveMessage.TxID,
		"mempool":    mustRawMempool(t, ctx, node),
	})

	if proveMessage.RevealTxID == "" {
		t.Fatal("reveal txid is empty")
	}
	assertMempoolNotContains(t, ctx, node, proveMessage.RevealTxID)
	logRegtestReport(t, "reveal_not_yet_in_mempool", map[string]any{
		"reveal_txid": proveMessage.RevealTxID,
		"mempool":     mustRawMempool(t, ctx, node),
	})

	if err := miner.generateToAddress(ctx, 1, miningAddr); err != nil {
		t.Fatalf("generateToAddress(confirm prove commit) error = %v", err)
	}
	tip, err = currentTip(ctx, node)
	if err != nil {
		t.Fatalf("currentTip() error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_scanned_height", strconv.FormatUint(tip, 10)); err != nil {
		t.Fatalf("SetChainState(last_scanned_height=%d) error = %v", tip, err)
	}
	if err := engine.ConfirmOnce(ctx); err != nil {
		t.Fatalf("ConfirmOnce(prove commit) error = %v", err)
	}

	proveMessage = findMessageByType(t, s, model.MessageTypeProve)
	if proveMessage.RevealTxID == "" {
		t.Fatal("reveal txid is empty after prove confirmation")
	}
	assertMempoolContains(t, ctx, node, proveMessage.RevealTxID)
	logRegtestReport(t, "mempool_after_reveal_broadcast", map[string]any{
		"reveal_txid": proveMessage.RevealTxID,
		"mempool":     mustRawMempool(t, ctx, node),
	})
}

func newExternalEngine(t *testing.T, fixture *regtestFixture, s *store.Store) service.Engine {
	t.Helper()
	keyMaterial, err := keys.Load("", fixture.cfg.Signing.PrivateKeyHex)
	if err != nil {
		t.Fatalf("Load(private key) error = %v", err)
	}
	return service.Engine{
		Store:       s,
		RPC:         bitcoinrpc.New(fixture.env.RPCURL, fixture.env.RPCUser, fixture.env.RPCPass),
		StateAPI:    stateapi.New(fixture.env.StateAPIURL, "", 5*time.Second, ""),
		FeeAPI:      feeapi.New(fixture.env.FeeAPIURL, 5*time.Second),
		Config:      fixture.cfg,
		KeyMaterial: keyMaterial,
	}
}

func currentTip(ctx context.Context, node *managedBitcoind) (uint64, error) {
	var tip uint64
	if err := rpcInto(ctx, node.rpcURL, node.rpcUser, node.rpcPass, "getblockcount", nil, &tip); err != nil {
		return 0, err
	}
	return tip, nil
}

func mineEligibleTargetBlock(t *testing.T, ctx context.Context, node *managedBitcoind, miner *walletRPC, miningAddr string) (tip uint64, targetHeight uint64, targetHash string, targetVersion uint32) {
	t.Helper()
	beforeTip, err := currentTip(ctx, node)
	if err != nil {
		t.Fatalf("currentTip(before target) error = %v", err)
	}
	if err := miner.generateToAddress(ctx, 1, miningAddr); err != nil {
		t.Fatalf("generateToAddress(target block) error = %v", err)
	}
	tip, err = currentTip(ctx, node)
	if err != nil {
		t.Fatalf("currentTip(after target) error = %v", err)
	}
	if tip <= beforeTip {
		t.Fatalf("tip = %d, want > %d after mining target block", tip, beforeTip)
	}
	targetHeight = tip
	client := bitcoinrpc.New(node.rpcURL, node.rpcUser, node.rpcPass)
	targetHash, err = client.GetBlockHash(ctx, targetHeight)
	if err != nil {
		t.Fatalf("GetBlockHash(%d) error = %v", targetHeight, err)
	}
	header, err := client.GetBlockHeader(ctx, targetHash)
	if err != nil {
		t.Fatalf("GetBlockHeader(%q) error = %v", targetHash, err)
	}
	targetVersion = header.Version
	return tip, targetHeight, targetHash, targetVersion
}

func setStateHeightResponse(stub *regtestStateStub, height uint64, response stateResponse) {
	if stub == nil {
		return
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.byHeight[height] = response
}

func rawMempool(ctx context.Context, node *managedBitcoind) ([]string, error) {
	var mempool []string
	if err := rpcInto(ctx, node.rpcURL, node.rpcUser, node.rpcPass, "getrawmempool", nil, &mempool); err != nil {
		return nil, err
	}
	return mempool, nil
}

func mustRawMempool(t *testing.T, ctx context.Context, node *managedBitcoind) []string {
	t.Helper()
	mempool, err := rawMempool(ctx, node)
	if err != nil {
		t.Fatalf("getrawmempool error = %v", err)
	}
	return mempool
}

func logRegtestReport(t *testing.T, title string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Logf("[report] %s: %+v", title, value)
		return
	}
	t.Logf("[report] %s\n%s", title, string(data))
}

func assertMempoolContains(t *testing.T, ctx context.Context, node *managedBitcoind, txid string) {
	t.Helper()
	mempool, err := rawMempool(ctx, node)
	if err != nil {
		t.Fatalf("getrawmempool error = %v", err)
	}
	for _, item := range mempool {
		if item == txid {
			return
		}
	}
	t.Fatalf("mempool %v does not contain txid %s", mempool, txid)
}

func assertMempoolNotContains(t *testing.T, ctx context.Context, node *managedBitcoind, txid string) {
	t.Helper()
	mempool, err := rawMempool(ctx, node)
	if err != nil {
		t.Fatalf("getrawmempool error = %v", err)
	}
	for _, item := range mempool {
		if item == txid {
			t.Fatalf("mempool %v unexpectedly contains txid %s", mempool, txid)
		}
	}
}
