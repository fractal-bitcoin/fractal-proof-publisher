package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fractal-proof-publisher/internal/app"
	"fractal-proof-publisher/internal/bitcoinrpc"
	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/inscription"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/protocol"
	"fractal-proof-publisher/internal/store"
	"fractal-proof-publisher/internal/txbuilder"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
)

type regtestEnv struct {
	RPCURL      string
	RPCUser     string
	RPCPass     string
	StateAPIURL string
	FeeAPIURL   string
	PrivateKey  string
	ChangeAddr  string
	SQLitePath  string
}

type regtestFixture struct {
	env          regtestEnv
	cfg          config.Config
	keyMaterial  keys.KeyMaterial
	changeScript string
	changeType   string
	trace        *[]string

	rpc   *regtestRPCStub
	state *regtestStateStub
	fee   *regtestFeeStub
}

type regtestRPCProbe struct {
	URL  string
	User string
	Pass string
}

func (f *regtestFixture) rpcProbe() regtestRPCProbe {
	return regtestRPCProbe{
		URL:  f.env.RPCURL,
		User: f.env.RPCUser,
		Pass: f.env.RPCPass,
	}
}

type regtestBlock struct {
	Hash          string
	Height        uint64
	Confirmations int64
	Version       uint32
	PreviousHash  string
	Tx            []string
}

type stateResponse struct {
	BlockHash string `json:"blockhash"`
	StateHash string `json:"statehash"`
}

type feeResponse struct {
	FastestFee  int64 `json:"fastestFee"`
	HalfHourFee int64 `json:"halfHourFee"`
	HourFee     int64 `json:"hourFee"`
	MinimumFee  int64 `json:"minimumFee"`
}

type regtestRPCStub struct {
	server        *httptest.Server
	user          string
	pass          string
	tip           uint64
	blocks        map[uint64]regtestBlock
	byHash        map[string]regtestBlock
	methods       []string
	rawTxByID     map[string]string
	mempool       []string
	bestBlockHash string
	trace         *[]string
	mu            sync.Mutex
}

type regtestStateStub struct {
	server   *httptest.Server
	byHeight map[uint64]stateResponse
	requests []uint64
	mu       sync.Mutex
}

type regtestFeeStub struct {
	server   *httptest.Server
	response feeResponse
	calls    int
	mu       sync.Mutex
}

type rpcRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func loadRegtestEnv(t *testing.T) regtestEnv {
	t.Helper()
	return regtestEnv{
		RPCURL:      os.Getenv("REGTEST_RPC_URL"),
		RPCUser:     os.Getenv("REGTEST_RPC_USER"),
		RPCPass:     os.Getenv("REGTEST_RPC_PASS"),
		StateAPIURL: os.Getenv("REGTEST_STATE_API_URL"),
		FeeAPIURL:   os.Getenv("REGTEST_FEE_API_URL"),
		PrivateKey:  os.Getenv("REGTEST_PRIVATE_KEY_HEX"),
		ChangeAddr:  os.Getenv("REGTEST_CHANGE_ADDRESS"),
		SQLitePath:  filepath.Join(t.TempDir(), "regtest.db"),
	}
}

func buildRegtestFixture(t *testing.T) *regtestFixture {
	t.Helper()
	env := loadRegtestEnv(t)
	if strings.TrimSpace(env.RPCURL) != "" {
		return buildExternalRegtestFixture(t, env)
	}
	return buildStubRegtestFixture(t, env)
}

func buildExternalRegtestFixture(t *testing.T, env regtestEnv) *regtestFixture {
	t.Helper()
	keyMaterial, changeAddr, changeScript, changeType := deriveRegtestKeyMaterial(t, env.PrivateKey, env.ChangeAddr)
	env.PrivateKey = keyMaterial.PrivateKeyHex
	env.ChangeAddr = changeAddr
	cfg := buildRegtestConfig(env, changeScript, changeType)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	return &regtestFixture{
		env:          env,
		cfg:          cfg,
		keyMaterial:  keyMaterial,
		changeScript: changeScript,
		changeType:   changeType,
	}
}

func buildStubRegtestFixture(t *testing.T, env regtestEnv) *regtestFixture {
	t.Helper()
	keyMaterial, changeAddr, changeScript, changeType := deriveRegtestKeyMaterial(t, env.PrivateKey, env.ChangeAddr)
	env.PrivateKey = keyMaterial.PrivateKeyHex
	env.ChangeAddr = changeAddr
	env.RPCUser = "stubuser"
	env.RPCPass = "stubpass"

	registerTxID := strings.Repeat("1", 64)
	blockHash0 := strings.Repeat("9", 64)
	blockHash1 := strings.Repeat("a", 64)
	blockHash2 := strings.Repeat("b", 64)
	blockHash3 := strings.Repeat("0", 64)
	stateHash2 := strings.Repeat("c", 64)
	stateHash3 := strings.Repeat("d", 64)
	indexerID := "1:0"
	trace := []string{}

	proveHash2, err := protocol.ComputeProveHash(indexerID, blockHash2, stateHash2)
	if err != nil {
		t.Fatalf("ComputeProveHash(height=2) error = %v", err)
	}
	proveHash3, err := protocol.ComputeProveHash(indexerID, blockHash3, stateHash3)
	if err != nil {
		t.Fatalf("ComputeProveHash(height=3) error = %v", err)
	}
	proveRawTx2 := stubSignedHexForProvePayload(t, keyMaterial, changeAddr, changeScript, changeType, 2, indexerID, proveHash2)
	proveRawTx3 := stubSignedHexForProvePayload(t, keyMaterial, changeAddr, changeScript, changeType, 3, indexerID, proveHash3)
	proveTxID2 := txIDFromSignedHex(t, proveRawTx2)
	proveTxID3 := txIDFromSignedHex(t, proveRawTx3)

	rpc := newRegtestRPCStub(t, env.RPCUser, env.RPCPass, []regtestBlock{
		{Hash: blockHash0, Height: 0, Confirmations: 4, Version: 0x20000000, PreviousHash: "", Tx: nil},
		{Hash: blockHash1, Height: 1, Confirmations: 3, Version: 0x20260100, PreviousHash: blockHash0, Tx: []string{registerTxID}},
		{Hash: blockHash2, Height: 2, Confirmations: 2, Version: 0x20260100, PreviousHash: blockHash1, Tx: []string{proveTxID2}},
		{Hash: blockHash3, Height: 3, Confirmations: 1, Version: 0x20000000, PreviousHash: blockHash2, Tx: []string{proveTxID3}},
	}, map[string]string{
		proveTxID2: proveRawTx2,
		proveTxID3: proveRawTx3,
	}, &trace)
	state := newRegtestStateStub(t, map[uint64]stateResponse{
		2: {BlockHash: blockHash2, StateHash: stateHash2},
		3: {BlockHash: blockHash3, StateHash: stateHash3},
	}, &trace)
	fee := newRegtestFeeStub(t, feeResponse{FastestFee: 9, HalfHourFee: 5, HourFee: 3, MinimumFee: 2}, &trace)

	env.RPCURL = rpc.server.URL
	env.StateAPIURL = state.server.URL
	env.FeeAPIURL = fee.server.URL
	cfg := buildRegtestConfig(env, changeScript, changeType)
	cfg.Runtime.DisableBroadcast = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	return &regtestFixture{
		env:          env,
		cfg:          cfg,
		keyMaterial:  keyMaterial,
		changeScript: changeScript,
		changeType:   changeType,
		trace:        &trace,
		rpc:          rpc,
		state:        state,
		fee:          fee,
	}
}

func deriveRegtestKeyMaterial(t *testing.T, privateKeyHex, changeAddr string) (keys.KeyMaterial, string, string, string) {
	t.Helper()
	if strings.TrimSpace(privateKeyHex) == "" {
		privateKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	}
	keyMaterial, err := keys.Load("", privateKeyHex)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if strings.TrimSpace(changeAddr) == "" {
		changeAddr, err = keyMaterial.Address(&chaincfg.RegressionNetParams, "p2wpkh")
		if err != nil {
			t.Fatalf("Address() error = %v", err)
		}
	}
	changeScript, err := txbuilder.ScriptPubKeyHexForAddress(changeAddr, &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatalf("ScriptPubKeyHexForAddress() error = %v", err)
	}
	changeType, err := txbuilder.AddressTypeForAddress(changeAddr, &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatalf("AddressTypeForAddress() error = %v", err)
	}
	return keyMaterial, changeAddr, changeScript, changeType
}

func buildRegtestConfig(env regtestEnv, changeScript, changeType string) config.Config {
	return config.Config{
		BitcoinRPC: config.BitcoinRPCConfig{
			URL:      env.RPCURL,
			User:     env.RPCUser,
			Password: env.RPCPass,
			Network:  "regtest",
		},
		Signing: config.SigningConfig{
			PrivateKeyHex: env.PrivateKey,
			ChangeAddress: env.ChangeAddr,
			InitialUTXOs: []config.InitialUTXO{{
				TxID:         strings.Repeat("f", 64),
				Vout:         0,
				AmountSat:    6000,
				Address:      env.ChangeAddr,
				ScriptPubKey: changeScript,
				AddressType:  changeType,
			}},
		},
		StateAPI: config.StateAPIConfig{BaseURL: env.StateAPIURL, Timeout: 5 * time.Second},
		FeeAPI: config.FeeAPIConfig{
			BaseURL:         env.FeeAPIURL,
			Timeout:         5 * time.Second,
			Strategy:        "half_hour",
			MinFeeRateSatVB: 1,
			MaxFeeRateSatVB: 100,
		},
		Scan: config.ScanConfig{
			StartHeight:           0,
			PollInterval:          100 * time.Millisecond,
			TargetBlockVersion:    0x20260100,
			RequiredConfirmations: 1,
			MaxReorgDepth:         6,
		},
		Tx:       config.TxConfig{SendChangeMinValue: 546},
		Database: config.DatabaseConfig{SQLitePath: env.SQLitePath},
		Runtime:  config.RuntimeConfig{DisableBroadcast: true, DryRun: true},
	}
}

func TestRegtestHarnessDryRunWorkflow(t *testing.T) {
	fixture := buildRegtestFixture(t)
	if fixture.rpc == nil {
		t.Skip("stub workflow assertions only run against the local in-process harness")
	}

	ctx := context.Background()
	s := openFixtureStore(t, fixture)
	defer s.DB.Close()
	seedRegisterMessage(t, s, fixture.rpc.blocks[1].Tx[0])

	if err := app.Run(ctx, fixture.cfg); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	registerMessage := findMessageByType(t, s, model.MessageTypeRegister)
	if registerMessage.Status != model.MessageStatusCommitSent {
		t.Fatalf("register status = %q, want %q", registerMessage.Status, model.MessageStatusCommitSent)
	}

	proveMessages := findMessagesByType(t, s, model.MessageTypeProve)
	if len(proveMessages) != 0 {
		t.Fatalf("prove message count = %d, want 0 in dry-run", len(proveMessages))
	}

	if got := chainState(t, s, "last_scanned_height"); got != "1" {
		t.Fatalf("last_scanned_height = %q, want 1 from seeded register only", got)
	}
	if got := chainState(t, s, "indexer_id"); got != "" {
		t.Fatalf("indexer_id = %q, want empty in dry-run", got)
	}
	if got := chainState(t, s, "last_seen_tip"); got != "" {
		t.Fatalf("last_seen_tip = %q, want empty in dry-run", got)
	}

	availableUTXOs, err := s.ListAvailableUTXOs(ctx)
	if err != nil {
		t.Fatalf("ListAvailableUTXOs() error = %v", err)
	}
	if len(availableUTXOs) != 0 {
		t.Fatalf("available utxos = %d, want 0 in dry-run", len(availableUTXOs))
	}

	if got := fixture.fee.callCount(); got != 0 {
		t.Fatalf("fee API call count = %d, want 0", got)
	}
	if got := fixture.state.requestHeights(); len(got) != 0 {
		t.Fatalf("state API heights = %v, want []", got)
	}
	methods := fixture.rpc.calledMethods()
	if len(methods) != 0 {
		t.Fatalf("rpc methods = %v, want none in dry-run", methods)
	}
	if got := traceEntries(fixture); len(got) != 0 {
		t.Fatalf("trace = %v, want empty in dry-run", got)
	}
}

func TestRegtestHarnessRPCProbeMatchesBitcoindShape(t *testing.T) {
	fixture := buildRegtestFixture(t)
	probe := fixture.rpcProbe()
	client := bitcoinrpc.New(probe.URL, probe.User, probe.Pass)
	ctx := context.Background()

	tip, err := client.GetBlockCount(ctx)
	if err != nil {
		t.Fatalf("GetBlockCount() error = %v", err)
	}
	if tip == 0 {
		t.Fatal("tip is zero, want at least one mined block")
	}

	hash, err := client.GetBlockHash(ctx, tip)
	if err != nil {
		t.Fatalf("GetBlockHash(%d) error = %v", tip, err)
	}
	if strings.TrimSpace(hash) == "" {
		t.Fatalf("GetBlockHash(%d) returned empty hash", tip)
	}

	header, err := client.GetBlockHeader(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlockHeader(%q) error = %v", hash, err)
	}
	if header.Hash != hash {
		t.Fatalf("header hash = %q, want %q", header.Hash, hash)
	}
	if header.Height != tip {
		t.Fatalf("header height = %d, want %d", header.Height, tip)
	}
	if tip > 0 && strings.TrimSpace(header.PreviousHash) == "" {
		t.Fatalf("header previous hash is empty at height %d", tip)
	}

	block, err := client.GetBlock(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlock(%q) error = %v", hash, err)
	}
	if block.Hash != hash {
		t.Fatalf("block hash = %q, want %q", block.Hash, hash)
	}
	if block.Height != tip {
		t.Fatalf("block height = %d, want %d", block.Height, tip)
	}
	if tip > 0 && len(block.Tx) == 0 {
		t.Fatalf("block tx count = 0, want at least one tx at height %d", tip)
	}

	if fixture.rpc != nil {
		methods := fixture.rpc.calledMethods()
		if !containsSequence(methods, []string{"getblockcount", "getblockhash", "getblockheader", "getblock"}) {
			t.Fatalf("rpc methods sequence = %v, want getblockcount/getblockhash/getblockheader/getblock", methods)
		}
	}
}

func TestRegtestHarnessBuildsValidConfigForStub(t *testing.T) {
	fixture := buildRegtestFixture(t)
	if err := fixture.cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if fixture.cfg.BitcoinRPC.Network != "regtest" {
		t.Fatalf("network = %q, want regtest", fixture.cfg.BitcoinRPC.Network)
	}
	if fixture.rpc != nil {
		if !strings.HasPrefix(fixture.cfg.Signing.ChangeAddress, "bcrt1") {
			t.Fatalf("change address = %q, want bcrt1 prefix in stub mode", fixture.cfg.Signing.ChangeAddress)
		}
		if fixture.cfg.BitcoinRPC.User != "stubuser" || fixture.cfg.BitcoinRPC.Password != "stubpass" {
			t.Fatalf("stub rpc credentials = %q/%q, want stubuser/stubpass", fixture.cfg.BitcoinRPC.User, fixture.cfg.BitcoinRPC.Password)
		}
	}
}

func TestRegtestHarnessStubModelsBitcoindRPCMetadata(t *testing.T) {
	fixture := buildStubRegtestFixture(t, loadRegtestEnv(t))
	ctx := context.Background()
	client := bitcoinrpc.New(fixture.env.RPCURL, fixture.env.RPCUser, fixture.env.RPCPass)

	tip, err := client.GetBlockCount(ctx)
	if err != nil {
		t.Fatalf("GetBlockCount() error = %v", err)
	}
	if tip != 3 {
		t.Fatalf("tip = %d, want 3", tip)
	}

	hash, err := client.GetBlockHash(ctx, 2)
	if err != nil {
		t.Fatalf("GetBlockHash(2) error = %v", err)
	}
	header, err := client.GetBlockHeader(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlockHeader(%q) error = %v", hash, err)
	}
	if header.Hash != fixture.rpc.blocks[2].Hash {
		t.Fatalf("header hash = %q, want %q", header.Hash, fixture.rpc.blocks[2].Hash)
	}
	if header.Height != 2 {
		t.Fatalf("header height = %d, want 2", header.Height)
	}
	if header.Confirmations != fixture.rpc.blocks[2].Confirmations {
		t.Fatalf("header confirmations = %d, want %d", header.Confirmations, fixture.rpc.blocks[2].Confirmations)
	}
	if header.PreviousHash != fixture.rpc.blocks[2].PreviousHash {
		t.Fatalf("header previous hash = %q, want %q", header.PreviousHash, fixture.rpc.blocks[2].PreviousHash)
	}

	block, err := client.GetBlock(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlock(%q) error = %v", hash, err)
	}
	if block.Hash != fixture.rpc.blocks[2].Hash {
		t.Fatalf("block hash = %q, want %q", block.Hash, fixture.rpc.blocks[2].Hash)
	}
	if block.Height != 2 {
		t.Fatalf("block height = %d, want 2", block.Height)
	}
	if len(block.Tx) != 1 || block.Tx[0] != fixture.rpc.blocks[2].Tx[0] {
		t.Fatalf("block tx = %v, want [%s]", block.Tx, fixture.rpc.blocks[2].Tx[0])
	}

	bestHash, err := fixture.rpc.bestHash()
	if err != nil {
		t.Fatalf("bestHash() error = %v", err)
	}
	if bestHash != fixture.rpc.blocks[3].Hash {
		t.Fatalf("best hash = %q, want %q", bestHash, fixture.rpc.blocks[3].Hash)
	}
}

func TestRegtestHarnessStubRejectsMissingBasicAuth(t *testing.T) {
	fixture := buildStubRegtestFixture(t, loadRegtestEnv(t))
	ctx := context.Background()
	rpc := bitcoinrpc.New(fixture.env.RPCURL, "", "")
	if _, err := rpc.GetBlockCount(ctx); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("GetBlockCount() error = %v, want unauthorized", err)
	}
}

func openFixtureStore(t *testing.T, fixture *regtestFixture) *store.Store {
	t.Helper()
	s, err := store.Open(fixture.cfg.Database.SQLitePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return s
}

func seedRegisterMessage(t *testing.T, s *store.Store, txid string) {
	t.Helper()
	ctx := context.Background()
	messageID, err := s.CreateMessage(ctx, model.MessageTypeRegister, "fip101,v1,register,100,p2wpkh,bcrt1fixture,indexer", nil, "")
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if err := s.MarkMessageSigned(ctx, messageID, "abcd"); err != nil {
		t.Fatalf("MarkMessageSigned() error = %v", err)
	}
	if err := s.MarkMessageBroadcasted(ctx, messageID, txid); err != nil {
		t.Fatalf("MarkMessageBroadcasted() error = %v", err)
	}
	if err := s.SetChainState(ctx, "last_scanned_height", "1"); err != nil {
		t.Fatalf("SetChainState() error = %v", err)
	}
	if err := s.UpdateMessageConfirmationDetails(ctx, messageID, 1, ""); err != nil {
		t.Fatalf("UpdateMessageConfirmationDetails() error = %v", err)
	}
}

func findMessageByType(t *testing.T, s *store.Store, messageType model.MessageType) store.MessageRecord {
	t.Helper()
	messages := findMessagesByType(t, s, messageType)
	if len(messages) == 0 {
		t.Fatalf("no message found for type %q", messageType)
	}
	return messages[0]
}

func findMessagesByType(t *testing.T, s *store.Store, messageType model.MessageType) []store.MessageRecord {
	t.Helper()
	rows, err := s.DB.QueryContext(context.Background(), `
		SELECT id, type, status, payload_text, related_height, indexer_id, txid, raw_tx_hex, confirm_height, reveal_txid, reveal_raw_tx_hex, reveal_broadcast_at, reveal_confirm_height
		FROM messages WHERE type = ? AND parent_message_id IS NULL ORDER BY id ASC
	`, messageType)
	if err != nil {
		t.Fatalf("QueryContext() error = %v", err)
	}
	defer rows.Close()

	var messages []store.MessageRecord
	for rows.Next() {
		var record store.MessageRecord
		var relatedHeight sql.NullInt64
		var indexerID sql.NullString
		var txid sql.NullString
		var rawTxHex sql.NullString
		var confirmHeight sql.NullInt64
		var revealTxID sql.NullString
		var revealRawTxHex sql.NullString
		var revealBroadcastAt sql.NullString
		var revealConfirmHeight sql.NullInt64
		if err := rows.Scan(&record.ID, &record.Type, &record.Status, &record.PayloadText, &relatedHeight, &indexerID, &txid, &rawTxHex, &confirmHeight, &revealTxID, &revealRawTxHex, &revealBroadcastAt, &revealConfirmHeight); err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		if relatedHeight.Valid {
			record.RelatedHeight = uint64(relatedHeight.Int64)
		}
		if indexerID.Valid {
			record.IndexerID = indexerID.String
		}
		if txid.Valid {
			record.TxID = txid.String
		}
		if rawTxHex.Valid {
			record.RawTxHex = rawTxHex.String
		}
		if confirmHeight.Valid {
			record.ConfirmHeight = uint64(confirmHeight.Int64)
		}
		if revealTxID.Valid {
			record.RevealTxID = revealTxID.String
		}
		if revealRawTxHex.Valid {
			record.RevealRawTxHex = revealRawTxHex.String
		}
		if revealBroadcastAt.Valid {
			record.RevealBroadcastAt = revealBroadcastAt.String
		}
		if revealConfirmHeight.Valid {
			record.RevealConfirmHeight = uint64(revealConfirmHeight.Int64)
		}
		messages = append(messages, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() = %v", err)
	}
	return messages
}

func assertBlockRecord(t *testing.T, s *store.Store, height uint64, wantHash string, wantStatus model.BlockStatus, wantEligible bool) {
	t.Helper()
	block, err := s.GetBlock(context.Background(), height)
	if err != nil {
		t.Fatalf("GetBlock(%d) error = %v", height, err)
	}
	if block.BlockHash != wantHash {
		t.Fatalf("block %d hash = %q, want %q", height, block.BlockHash, wantHash)
	}
	if block.Status != wantStatus {
		t.Fatalf("block %d status = %q, want %q", height, block.Status, wantStatus)
	}
	if block.Eligible != wantEligible {
		t.Fatalf("block %d eligible = %v, want %v", height, block.Eligible, wantEligible)
	}
}

func chainState(t *testing.T, s *store.Store, key string) string {
	t.Helper()
	value, err := s.GetChainState(context.Background(), key)
	if err != nil {
		t.Fatalf("GetChainState(%q) error = %v", key, err)
	}
	return value
}

func containsAll(have, want []string) bool {
	seen := make(map[string]bool, len(have))
	for _, item := range have {
		seen[item] = true
	}
	for _, item := range want {
		if !seen[item] {
			return false
		}
	}
	return true
}

func assertProvePayload(t *testing.T, payload string, wantHeight uint64, wantIndexerID string, wantState stateResponse) {
	t.Helper()
	parts := strings.Split(payload, ",")
	if len(parts) != 6 {
		t.Fatalf("prove payload parts = %d, want 6: %q", len(parts), payload)
	}
	if parts[0] != protocol.ProtocolName || parts[1] != protocol.ProtocolVersion || parts[2] != protocol.OpProve {
		t.Fatalf("prove payload prefix = %v, want [%s %s %s]", parts[:3], protocol.ProtocolName, protocol.ProtocolVersion, model.MessageTypeProve)
	}
	if parts[3] != wantIndexerID {
		t.Fatalf("prove indexer_id = %q, want %q", parts[3], wantIndexerID)
	}
	height, err := strconv.ParseUint(parts[4], 10, 64)
	if err != nil {
		t.Fatalf("ParseUint(prove height) error = %v", err)
	}
	if height != wantHeight {
		t.Fatalf("prove height = %d, want %d", height, wantHeight)
	}
	wantHash, err := protocol.ComputeProveHash(wantIndexerID, wantState.BlockHash, wantState.StateHash)
	if err != nil {
		t.Fatalf("ComputeProveHash() error = %v", err)
	}
	if parts[5] != wantHash {
		t.Fatalf("prove hash = %q, want %q", parts[5], wantHash)
	}
}

func assertChangeUTXOForMessage(t *testing.T, utxo model.UTXO, message store.MessageRecord) {
	t.Helper()
	if utxo.Source != model.UTXOSourceChange {
		t.Fatalf("available utxo source = %q, want %q", utxo.Source, model.UTXOSourceChange)
	}
	if utxo.Status != model.UTXOStatusAvailable {
		t.Fatalf("available utxo status = %q, want %q", utxo.Status, model.UTXOStatusAvailable)
	}
	if utxo.TxID != message.TxID {
		t.Fatalf("change utxo txid = %q, want %q", utxo.TxID, message.TxID)
	}
	if utxo.AmountSat <= txbuilder.DefaultRevealPostage {
		t.Fatalf("change utxo amount = %d, want greater than reveal postage %d", utxo.AmountSat, txbuilder.DefaultRevealPostage)
	}
}

func traceEntries(fixture *regtestFixture) []string {
	if fixture == nil || fixture.trace == nil {
		return nil
	}
	return append([]string(nil), (*fixture.trace)...)
}

func containsSequence(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	matched := 0
	for _, item := range have {
		if item != want[matched] {
			continue
		}
		matched++
		if matched == len(want) {
			return true
		}
	}
	return false
}

func stubSignedHexForProvePayload(t *testing.T, keyMaterial keys.KeyMaterial, changeAddr, changeScript, changeType string, height uint64, indexerID, proveHash string) string {
	t.Helper()
	payload, err := protocol.EncodeProveText(model.ProveData{IndexerID: indexerID, ProveHeight: height, ProveHash: proveHash})
	if err != nil {
		t.Fatalf("EncodeProveText() error = %v", err)
	}
	envelope, err := inscription.NewTextEnvelope(payload)
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	unsigned, err := txbuilder.Build(txbuilder.BuildInput{
		Inputs: []model.UTXO{{
			TxID:         strings.Repeat("e", 64),
			Vout:         0,
			AmountSat:    6000,
			Address:      changeAddr,
			ScriptPubKey: changeScript,
			AddressType:  changeType,
		}},
		ChangeAddress:     changeAddr,
		Network:           "regtest",
		CommitPlan:        envelope.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      5,
		CommitOutputValue: txbuilder.DefaultRevealPostage,
		ChangeValue:       6000 - txbuilder.DefaultRevealPostage,
		RevealOutputValue: txbuilder.DefaultRevealPostage,
		RevealRecipient:   changeAddr,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	signed, err := txbuilder.Sign(unsigned, keyMaterial)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	return signed
}

func txIDFromSignedHex(t *testing.T, signedHex string) string {
	t.Helper()
	txid, err := txIDFromSignedHexString(signedHex)
	if err != nil {
		t.Fatalf("txIDFromSignedHexString() error = %v", err)
	}
	return txid
}

func txIDFromSignedHexString(signedHex string) (string, error) {
	rawTx, err := hex.DecodeString(signedHex)
	if err != nil {
		return "", err
	}
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(rawTx)); err != nil {
		return "", err
	}
	return tx.TxHash().String(), nil
}

func stubTxIDForProvePayload(t *testing.T, keyMaterial keys.KeyMaterial, changeAddr, changeScript, changeType string, height uint64, indexerID, proveHash string) string {
	t.Helper()
	return strings.ToLower(txIDFromSignedHex(t, stubSignedHexForProvePayload(t, keyMaterial, changeAddr, changeScript, changeType, height, indexerID, proveHash)))
}

func newRegtestRPCStub(t *testing.T, user, pass string, blocks []regtestBlock, rawTxByID map[string]string, trace *[]string) *regtestRPCStub {
	t.Helper()
	stub := &regtestRPCStub{
		user:      user,
		pass:      pass,
		blocks:    make(map[uint64]regtestBlock, len(blocks)),
		byHash:    make(map[string]regtestBlock, len(blocks)),
		rawTxByID: make(map[string]string, len(rawTxByID)),
		trace:     trace,
	}
	for _, block := range blocks {
		stub.blocks[block.Height] = block
		stub.byHash[block.Hash] = block
		if block.Height > stub.tip {
			stub.tip = block.Height
			stub.bestBlockHash = block.Hash
		}
	}
	for txid, rawTx := range rawTxByID {
		stub.rawTxByID[txid] = rawTx
	}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !stub.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -32600, "message": "unauthorized"}})
			return
		}

		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-32700,"message":"decode request"}}`))
			return
		}

		stub.mu.Lock()
		stub.methods = append(stub.methods, req.Method)
		stub.mu.Unlock()
		stub.appendTrace("rpc:" + req.Method)

		result, err := stub.dispatch(req.Method, req.Params)
		if err != nil {
			_, _ = w.Write([]byte(fmt.Sprintf(`{"result":null,"error":{"code":-1,"message":%q}}`, err.Error())))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil})
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *regtestRPCStub) dispatch(method string, params []json.RawMessage) (any, error) {
	switch method {
	case "getblockcount":
		return s.tip, nil
	case "getbestblockhash":
		if s.bestBlockHash == "" {
			return nil, fmt.Errorf("best block hash unavailable")
		}
		return s.bestBlockHash, nil
	case "getblockchaininfo":
		bestHash, err := s.bestHash()
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"chain":                "regtest",
			"blocks":               s.tip,
			"headers":              s.tip,
			"bestblockhash":        bestHash,
			"initialblockdownload": false,
			"pruned":               false,
		}, nil
	case "getblockhash":
		var height uint64
		if err := decodeRPCParam(params, 0, &height); err != nil {
			return nil, err
		}
		block, ok := s.blocks[height]
		if !ok {
			return nil, fmt.Errorf("unknown height %d", height)
		}
		return block.Hash, nil
	case "getblockheader":
		var hash string
		if err := decodeRPCParam(params, 0, &hash); err != nil {
			return nil, err
		}
		block, ok := s.byHash[hash]
		if !ok {
			return nil, fmt.Errorf("unknown hash %s", hash)
		}
		return bitcoinrpc.BlockHeader{
			Hash:          block.Hash,
			Height:        block.Height,
			Confirmations: block.Confirmations,
			Version:       block.Version,
			PreviousHash:  block.PreviousHash,
		}, nil
	case "getblock":
		var hash string
		if err := decodeRPCParam(params, 0, &hash); err != nil {
			return nil, err
		}
		block, ok := s.byHash[hash]
		if !ok {
			return nil, fmt.Errorf("unknown hash %s", hash)
		}
		return bitcoinrpc.VerboseBlock{
			Hash:          block.Hash,
			Height:        block.Height,
			Confirmations: block.Confirmations,
			Version:       block.Version,
			Tx:            append([]string(nil), block.Tx...),
		}, nil
	case "getrawtransaction":
		var txid string
		if err := decodeRPCParam(params, 0, &txid); err != nil {
			return nil, err
		}
		rawTx, ok := s.lookupRawTransaction(txid)
		if !ok {
			return nil, fmt.Errorf("unknown txid %s", txid)
		}
		return rawTx, nil
	case "testmempoolaccept":
		var rawTxs []string
		if err := decodeRPCParam(params, 0, &rawTxs); err != nil {
			return nil, err
		}
		results := make([]map[string]any, 0, len(rawTxs))
		for _, rawTx := range rawTxs {
			txid, err := txIDFromSignedHexString(rawTx)
			if err != nil {
				return nil, err
			}
			results = append(results, map[string]any{
				"txid":          txid,
				"allowed":       true,
				"vsize":         0,
				"reject-reason": "",
			})
		}
		return results, nil
	case "getrawmempool":
		return s.mempoolTxIDs(), nil
	case "sendrawtransaction":
		var rawTx string
		if err := decodeRPCParam(params, 0, &rawTx); err != nil {
			return nil, err
		}
		txid, err := txIDFromSignedHexString(rawTx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.rawTxByID[txid] = rawTx
		s.mempool = append(s.mempool, txid)
		s.mu.Unlock()
		return txid, nil
	default:
		return nil, fmt.Errorf("unsupported method %s", method)
	}
}

func (s *regtestRPCStub) authorized(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if s.user == "" && s.pass == "" {
		return true
	}
	return ok && user == s.user && pass == s.pass
}

func (s *regtestRPCStub) appendTrace(entry string) {
	if s.trace == nil {
		return
	}
	*s.trace = append(*s.trace, entry)
}

func (s *regtestRPCStub) rawTransaction(txid string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rawTxByID[txid]
}

func (s *regtestRPCStub) lookupRawTransaction(txid string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rawTx, ok := s.rawTxByID[txid]
	return rawTx, ok
}

func (s *regtestRPCStub) bestHash() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bestBlockHash == "" {
		return "", fmt.Errorf("best block hash unavailable")
	}
	return s.bestBlockHash, nil
}

func (s *regtestRPCStub) mempoolTxIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.mempool...)
}

func (s *regtestRPCStub) calledMethods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

func newRegtestStateStub(t *testing.T, responses map[uint64]stateResponse, trace *[]string) *regtestStateStub {
	t.Helper()
	stub := &regtestStateStub{byHeight: responses}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		heightText := strings.TrimPrefix(r.URL.Path, "/")
		height, err := strconv.ParseUint(heightText, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		stub.mu.Lock()
		stub.requests = append(stub.requests, height)
		response, ok := stub.byHeight[height]
		stub.mu.Unlock()
		if trace != nil {
			*trace = append(*trace, fmt.Sprintf("state:%d", height))
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if response.BlockHash != "" {
			w.Header().Set("X-Regtest-Blockhash", response.BlockHash)
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *regtestStateStub) requestHeights() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.requests...)
}

func newRegtestFeeStub(t *testing.T, response feeResponse, trace *[]string) *regtestFeeStub {
	t.Helper()
	stub := &regtestFeeStub{response: response}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/api/v1/fees/recommended" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if trace != nil {
			*trace = append(*trace, "fee:"+r.URL.Path)
		}
		stub.mu.Lock()
		stub.calls++
		response := stub.response
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *regtestFeeStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func decodeRPCParam(params []json.RawMessage, idx int, out any) error {
	if idx >= len(params) {
		return fmt.Errorf("missing param %d", idx)
	}
	if err := json.Unmarshal(params[idx], out); err != nil {
		return fmt.Errorf("decode param %d: %w", idx, err)
	}
	return nil
}
