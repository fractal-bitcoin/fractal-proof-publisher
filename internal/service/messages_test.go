package service

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fractal-proof-publisher/internal/bitcoinrpc"
	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/feeapi"
	"fractal-proof-publisher/internal/inscription"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/protocol"
	"fractal-proof-publisher/internal/stateapi"
	"fractal-proof-publisher/internal/store"
	"fractal-proof-publisher/internal/txbuilder"

	"github.com/btcsuite/btcd/chaincfg"
)

func TestBuildAndSign(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "service.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	seedScript, err := keyMaterial.P2WPKHScript(&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("P2WPKHScript() error = %v", err)
	}
	if err := s.SeedInitialUTXOs(ctx, []config.InitialUTXO{{
		TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		Vout:         0,
		AmountSat:    5000,
		Address:      "bc1qtest",
		ScriptPubKey: hex.EncodeToString(seedScript),
		AddressType:  "p2wpkh",
	}}); err != nil {
		t.Fatalf("SeedInitialUTXOs() error = %v", err)
	}

	feeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fastestFee":12,"halfHourFee":8,"hourFee":4,"minimumFee":2}`))
	}))
	defer feeServer.Close()

	stateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"blockhash":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","statehash":"ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"}`))
	}))
	defer stateServer.Close()

	changeAddress, err := keyMaterial.Address(&chaincfg.MainNetParams, "p2tr")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	expectedChangeScript, err := txbuilder.ScriptPubKeyHexForAddress(changeAddress, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("ScriptPubKeyHexForAddress() error = %v", err)
	}

	engine := Engine{
		Store:    s,
		FeeAPI:   feeapi.New(feeServer.URL, time.Second),
		StateAPI: stateapi.New(stateServer.URL, "", time.Second, ""),
		Config: config.Config{
			Signing: config.SigningConfig{ChangeAddress: changeAddress},
			FeeAPI:  config.FeeAPIConfig{Strategy: "half_hour", MinFeeRateSatVB: 1, MaxFeeRateSatVB: 20},
			Tx:      config.TxConfig{SendChangeMinValue: 546},
			Runtime: config.RuntimeConfig{DisableBroadcast: true},
		},
		KeyMaterial: keyMaterial,
	}

	messageID, payload, err := engine.BuildProveMessage(ctx, 100, "100:1")
	if err != nil {
		t.Fatalf("BuildProveMessage() error = %v", err)
	}
	if payload == "" {
		t.Fatal("payload is empty")
	}

	signed, err := engine.BuildAndSign(ctx, messageID, payload)
	if err != nil {
		t.Fatalf("BuildAndSign() error = %v", err)
	}
	if signed == "" {
		t.Fatal("signed tx is empty")
	}

	msg, _, err := parseSignedTx(signed)
	if err != nil {
		t.Fatalf("parseSignedTx() error = %v", err)
	}
	if len(msg.TxOut) != 1 {
		t.Fatalf("commit tx outputs = %d, want 1", len(msg.TxOut))
	}

	env, err := inscription.NewTextEnvelope([]byte(payload))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	revealMarker := []byte("FIP-101:" + protocol.OpProve + ":reveal")
	revealVBytes, revealFeeValue, err := txbuilder.EstimateRevealFeeWithOpReturn(env.CommitPlan(keyMaterial.PublicKey), chaincfg.MainNetParams.Name, changeAddress, 8, revealMarker)
	if err != nil {
		t.Fatalf("EstimateRevealFeeWithOpReturn() error = %v", err)
	}
	commitOutputValue := msg.TxOut[0].Value
	revealChangeValue := commitOutputValue - revealFeeValue - txbuilder.DefaultOpReturnValue
	if revealChangeValue <= 0 {
		t.Fatalf("reveal change value = %d, want > 0", revealChangeValue)
	}
	commitUnsigned, err := txbuilder.Build(txbuilder.BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         0,
			AmountSat:    5000,
			Address:      "bc1qtest",
			ScriptPubKey: hex.EncodeToString(seedScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     changeAddress,
		Network:           chaincfg.MainNetParams.Name,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      8,
		CommitOutputValue: msg.TxOut[0].Value,
		RevealOutputValue: msg.TxOut[0].Value - revealFeeValue - txbuilder.DefaultOpReturnValue,
		RevealRecipient:   changeAddress,
		RevealOpReturn:    []byte(payload),
	})
	if err != nil {
		t.Fatalf("Build(commitUnsigned) error = %v", err)
	}
	finalizedReveal, err := txbuilder.FinalizeRevealFromCommitHex(commitUnsigned.Reveal, signed)
	if err != nil {
		t.Fatalf("FinalizeRevealFromCommitHex() error = %v", err)
	}
	if finalizedReveal.CommitTxID == "" {
		t.Fatal("finalized reveal commit txid is empty")
	}
	if finalizedReveal.TxIn == nil {
		t.Fatal("finalized reveal input is nil")
	}
	if finalizedReveal.TxIn.PreviousOutPoint.Hash.String() != finalizedReveal.CommitTxID {
		t.Fatalf("reveal previous outpoint txid = %q, want %q", finalizedReveal.TxIn.PreviousOutPoint.Hash.String(), finalizedReveal.CommitTxID)
	}
	if finalizedReveal.RawTxHex == "" {
		t.Fatal("finalized reveal raw tx is empty")
	}
	commitFeeValue := int64(5000) - commitOutputValue
	if commitFeeValue <= 0 {
		t.Fatalf("commit fee value = %d, want > 0", commitFeeValue)
	}
	if commitFeeValue+commitOutputValue != 5000 {
		t.Fatalf("commit tx conservation mismatch: fee=%d commit=%d total=%d", commitFeeValue, commitOutputValue, 5000)
	}
	if commitOutputValue-txbuilder.DefaultOpReturnValue-revealChangeValue != revealFeeValue {
		t.Fatalf("reserved reveal fee = %d, want %d", commitOutputValue-txbuilder.DefaultOpReturnValue-revealChangeValue, revealFeeValue)
	}
	if revealVBytes <= 0 {
		t.Fatalf("reveal vbytes = %d, want > 0", revealVBytes)
	}

	var storedMessage store.MessageRecord
	expectedTxID, err := expectedSignedTxID(signed)
	if err != nil {
		t.Fatalf("expectedSignedTxID() error = %v", err)
	}

	txid, err := engine.BroadcastSigned(ctx, messageID, signed)
	if err != nil {
		t.Fatalf("BroadcastSigned() error = %v", err)
	}
	if txid == "" {
		t.Fatal("txid is empty")
	}
	if txid != expectedTxID {
		t.Fatalf("broadcast txid = %q, want %q", txid, expectedTxID)
	}

	storedMessage, err = s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if storedMessage.RevealTxID == "" {
		t.Fatal("stored message reveal txid is empty")
	}
	if storedMessage.RevealRawTxHex == "" {
		t.Fatal("stored message reveal raw tx is empty")
	}
	revealTx, _, err := parseSignedTx(storedMessage.RevealRawTxHex)
	if err != nil {
		t.Fatalf("parse reveal tx error = %v", err)
	}
	if len(revealTx.TxOut) != 2 {
		t.Fatalf("reveal tx outputs = %d, want 2", len(revealTx.TxOut))
	}
	if revealTx.TxOut[0].Value != txbuilder.DefaultOpReturnValue {
		t.Fatalf("reveal opreturn value = %d, want %d", revealTx.TxOut[0].Value, txbuilder.DefaultOpReturnValue)
	}
	if len(revealTx.TxOut[0].PkScript) == 0 || revealTx.TxOut[0].PkScript[0] != 0x6a {
		t.Fatalf("reveal output is not OP_RETURN: %x", revealTx.TxOut[0].PkScript)
	}
	if !strings.Contains(hex.EncodeToString(revealTx.TxOut[0].PkScript), hex.EncodeToString([]byte("FIP-101:submit_proof:reveal"))) {
		t.Fatal("reveal opreturn does not contain proof marker")
	}
	if strings.Contains(hex.EncodeToString(revealTx.TxOut[0].PkScript), hex.EncodeToString([]byte(payload))) {
		t.Fatal("reveal opreturn should not contain full proof payload")
	}
	if revealTx.TxOut[1].Value != revealChangeValue {
		t.Fatalf("reveal change value = %d, want %d", revealTx.TxOut[1].Value, revealChangeValue)
	}
	if hex.EncodeToString(revealTx.TxOut[1].PkScript) != expectedChangeScript {
		t.Fatalf("reveal change script = %x, want %s", revealTx.TxOut[1].PkScript, expectedChangeScript)
	}

	revealBroadcastTxID, err := engine.BroadcastReveal(ctx, messageID)
	if err != nil {
		t.Fatalf("BroadcastReveal() error = %v", err)
	}
	if revealBroadcastTxID != storedMessage.RevealTxID {
		t.Fatalf("reveal broadcast txid = %q, want %q", revealBroadcastTxID, storedMessage.RevealTxID)
	}

	attempts, err := s.ListBroadcastAttemptsByMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("ListBroadcastAttemptsByMessage() error = %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("broadcast attempts = %d, want 0 in disable-broadcast mode", len(attempts))
	}

	utxos, err := s.ListAvailableUTXOs(ctx)
	if err != nil {
		t.Fatalf("ListAvailableUTXOs() error = %v", err)
	}
	if len(utxos) != 0 {
		t.Fatalf("available utxo count = %d, want 0 before commit confirmation", len(utxos))
	}

	var pendingTxID, pendingScript, pendingAddressType, pendingStatus string
	var pendingAmount int64
	var pendingAddress string
	if err := s.DB.QueryRowContext(ctx, `
		SELECT txid, amount_sat, address, script_pub_key, address_type, status
		FROM utxos WHERE reserved_by_message_id = ? AND source = ?
	`, messageID, model.UTXOSourceChange).Scan(&pendingTxID, &pendingAmount, &pendingAddress, &pendingScript, &pendingAddressType, &pendingStatus); err != nil {
		t.Fatalf("query pending change utxo error = %v", err)
	}
	if pendingStatus != string(model.UTXOStatusPending) {
		t.Fatalf("pending change status = %q, want %q", pendingStatus, model.UTXOStatusPending)
	}
	if pendingTxID != storedMessage.RevealTxID {
		t.Fatalf("pending change utxo txid = %q, want %q", pendingTxID, storedMessage.RevealTxID)
	}
	if pendingAmount != revealChangeValue {
		t.Fatalf("pending change utxo amount = %d, want %d", pendingAmount, revealChangeValue)
	}
	if pendingAddress != changeAddress {
		t.Fatalf("pending change utxo address = %q, want %q", pendingAddress, changeAddress)
	}
	if pendingScript != expectedChangeScript {
		t.Fatalf("pending change utxo script = %q, want %q", pendingScript, expectedChangeScript)
	}
	if pendingAddressType != "p2tr" {
		t.Fatalf("pending change utxo address type = %q, want %q", pendingAddressType, "p2tr")
	}

	storedMessage, err = s.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if storedMessage.RevealTxID == "" {
		t.Fatal("stored message reveal txid is empty")
	}
	if storedMessage.RevealRawTxHex == "" {
		t.Fatal("stored message reveal raw tx is empty")
	}

	if storedMessage.RevealConfirmHeight != 0 {
		t.Fatalf("reveal confirm height = %d, want 0 before reveal confirmation", storedMessage.RevealConfirmHeight)
	}
}

func TestBuildAndSignSkipsDustSizedUTXOSelection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dust-selection.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	seedScript, err := keyMaterial.P2WPKHScript(&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("P2WPKHScript() error = %v", err)
	}
	changeAddress, err := keyMaterial.Address(&chaincfg.MainNetParams, "p2tr")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	changeScript, err := txbuilder.ScriptPubKeyHexForAddress(changeAddress, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("ScriptPubKeyHexForAddress() error = %v", err)
	}
	stateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"blockhash":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","statehash":"ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"}`))
	}))
	defer stateServer.Close()

	if err := s.SeedInitialUTXOs(ctx, []config.InitialUTXO{{
		TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		Vout:         0,
		AmountSat:    5000,
		Address:      "bc1qtest",
		ScriptPubKey: hex.EncodeToString(seedScript),
		AddressType:  "p2wpkh",
	}}); err != nil {
		t.Fatalf("SeedInitialUTXOs() error = %v", err)
	}
	if err := s.InsertChangeUTXO(ctx, 0, model.UTXO{
		TxID:         "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100",
		Vout:         0,
		AmountSat:    txbuilder.DefaultRevealPostage,
		Address:      changeAddress,
		ScriptPubKey: changeScript,
		AddressType:  "p2tr",
		Status:       model.UTXOStatusAvailable,
		Source:       model.UTXOSourceChange,
	}); err != nil {
		t.Fatalf("InsertChangeUTXO() error = %v", err)
	}

	engine := Engine{
		Store:    s,
		StateAPI: stateapi.New(stateServer.URL, "", time.Second, ""),
		Config: config.Config{
			BitcoinRPC: config.BitcoinRPCConfig{Network: chaincfg.MainNetParams.Name},
			Signing:    config.SigningConfig{ChangeAddress: changeAddress},
			FeeAPI:     config.FeeAPIConfig{FixedFeeRateSatVB: 1},
			Tx:         config.TxConfig{SendChangeMinValue: 546},
			Runtime:    config.RuntimeConfig{DisableBroadcast: true},
		},
		KeyMaterial: keyMaterial,
	}

	messageID, payload, err := engine.BuildProveMessage(ctx, 100, "100:1")
	if err != nil {
		t.Fatalf("BuildProveMessage() error = %v", err)
	}
	if _, err := engine.BuildAndSign(ctx, messageID, payload); err != nil {
		t.Fatalf("BuildAndSign() error = %v", err)
	}

	var dustStatus string
	if err := s.DB.QueryRowContext(ctx, `SELECT status FROM utxos WHERE txid = ?`, "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100").Scan(&dustStatus); err != nil {
		t.Fatalf("query dust status error = %v", err)
	}
	if dustStatus != string(model.UTXOStatusAvailable) {
		t.Fatalf("dust utxo status = %q, want %q", dustStatus, model.UTXOStatusAvailable)
	}

	var selectedAmount int64
	if err := s.DB.QueryRowContext(ctx, `SELECT amount_sat FROM utxos WHERE reserved_by_message_id = ? AND status = ?`, messageID, model.UTXOStatusPending).Scan(&selectedAmount); err != nil {
		t.Fatalf("query selected utxo error = %v", err)
	}
	if selectedAmount <= txbuilder.DefaultRevealPostage {
		t.Fatalf("selected utxo amount = %d, want > %d", selectedAmount, txbuilder.DefaultRevealPostage)
	}
}

func TestBroadcastSignedRecordsAttemptOnRPCError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "broadcast-attempts.db")
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

	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":null,"error":{"code":-26,"message":"txn already in mempool"}}`))
	}))
	defer rpcServer.Close()

	engine := Engine{
		Store: s,
		RPC:   bitcoinrpc.New(rpcServer.URL, "", ""),
		Config: config.Config{
			Runtime: config.RuntimeConfig{DisableBroadcast: false},
		},
	}

	_, err = engine.BroadcastSigned(ctx, messageID, "abcd")
	if err == nil {
		t.Fatal("BroadcastSigned() error = nil, want rpc failure")
	}

	attempts, err := s.ListBroadcastAttemptsByMessage(ctx, messageID)
	if err != nil {
		t.Fatalf("ListBroadcastAttemptsByMessage() error = %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("broadcast attempts = %d, want 1", len(attempts))
	}
	if !strings.Contains(attempts[0].ErrorMessage, "sendrawtransaction") {
		t.Fatalf("broadcast attempt error = %q, want rpc method context", attempts[0].ErrorMessage)
	}
}

func TestBuildAndSignUsesFixedFeeRateWithoutFeeAPI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fixed-fee.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	seedScript, err := keyMaterial.P2WPKHScript(&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("P2WPKHScript() error = %v", err)
	}
	if err := s.SeedInitialUTXOs(ctx, []config.InitialUTXO{{
		TxID:         "111122223333444455556666777788889999aaaabbbbccccddddeeeeffff0000",
		Vout:         0,
		AmountSat:    5000,
		Address:      "bc1qtest",
		ScriptPubKey: hex.EncodeToString(seedScript),
		AddressType:  "p2wpkh",
	}}); err != nil {
		t.Fatalf("SeedInitialUTXOs() error = %v", err)
	}

	stateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"blockhash":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","statehash":"ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"}`))
	}))
	defer stateServer.Close()

	changeAddress, err := keyMaterial.Address(&chaincfg.MainNetParams, "p2tr")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}

	engine := Engine{
		Store:    s,
		StateAPI: stateapi.New(stateServer.URL, "", time.Second, ""),
		Config: config.Config{
			BitcoinRPC: config.BitcoinRPCConfig{Network: chaincfg.MainNetParams.Name},
			Signing:    config.SigningConfig{ChangeAddress: changeAddress},
			FeeAPI:     config.FeeAPIConfig{FixedFeeRateSatVB: 1},
			Tx:         config.TxConfig{SendChangeMinValue: 546},
			Runtime:    config.RuntimeConfig{DisableBroadcast: true},
		},
		KeyMaterial: keyMaterial,
	}

	messageID, payload, err := engine.BuildProveMessage(ctx, 100, "100:1")
	if err != nil {
		t.Fatalf("BuildProveMessage() error = %v", err)
	}

	signed, err := engine.BuildAndSign(ctx, messageID, payload)
	if err != nil {
		t.Fatalf("BuildAndSign() error = %v", err)
	}
	if signed == "" {
		t.Fatal("signed tx is empty")
	}
}

func TestBuildProveMessageFallsBackToRPCBlockHashWhenStateAPIOmitsIt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "prove-fallback.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	const (
		height        = uint64(100)
		blockHashHex  = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
		stateHashHex  = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
		expectedTxRef = "100:1"
	)

	stateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"detail":[{"height":100,"blockHash":"","stateHash":"` + stateHashHex + `"}]}}`))
	}))
	defer stateServer.Close()

	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"` + blockHashHex + `","error":null}`))
	}))
	defer rpcServer.Close()

	engine := Engine{
		Store:    s,
		RPC:      bitcoinrpc.New(rpcServer.URL, "", ""),
		StateAPI: stateapi.New(stateServer.URL, "", time.Second, "query-fip101"),
	}

	messageID, payload, err := engine.BuildProveMessage(ctx, height, expectedTxRef)
	if err != nil {
		t.Fatalf("BuildProveMessage() error = %v", err)
	}
	if messageID == 0 {
		t.Fatal("message id is empty")
	}

	wantHash, err := protocol.ComputeProveHash(expectedTxRef, blockHashHex, stateHashHex)
	if err != nil {
		t.Fatalf("ComputeProveHash() error = %v", err)
	}
	wantPayload := "fip101,1,submit_proof,100:1,100," + wantHash
	if payload != wantPayload {
		t.Fatalf("payload = %q, want %q", payload, wantPayload)
	}
}

func TestBuildProveMessageRejectsEmptyStateHash(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "prove-empty-state-hash.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	stateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"detail":[{"height":100,"blockHash":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","stateHash":""}]}}`))
	}))
	defer stateServer.Close()

	engine := Engine{
		Store:    s,
		StateAPI: stateapi.New(stateServer.URL, "", time.Second, "query-fip101"),
	}

	messageID, payload, err := engine.BuildProveMessage(ctx, 100, "100:1")
	if err == nil {
		t.Fatal("expected BuildProveMessage to reject empty state hash")
	}
	if !stateapi.IsRetryableHeightUnavailable(err) {
		t.Fatalf("expected retryable state error, got %v", err)
	}
	if messageID != 0 {
		t.Fatalf("message id = %d, want 0", messageID)
	}
	if payload != "" {
		t.Fatalf("payload = %q, want empty", payload)
	}
	existingID, findErr := s.FindMessageByHeightAndType(ctx, 100, model.MessageTypeProve)
	if findErr != nil {
		t.Fatalf("FindMessageByHeightAndType() error = %v", findErr)
	}
	if existingID != 0 {
		t.Fatalf("prove message id = %d, want 0", existingID)
	}
}

func expectedSignedTxID(signedHex string) (string, error) {
	_, txid, err := parseSignedTx(signedHex)
	return txid, err
}

func TestBuildAndSignUsesUnisatOpenAPIUTXOs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unisat-open-api.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.DB.Close()

	ctx := context.Background()
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	changeAddress, err := keyMaterial.Address(&chaincfg.MainNetParams, "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	changeScript, err := keyMaterial.P2WPKHScript(&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("P2WPKHScript() error = %v", err)
	}

	stateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"blockhash":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","statehash":"ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"}`))
	}))
	defer stateServer.Close()

	unisatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"cursor":0,"total":1,"utxo":[{"txid":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","vout":1,"satoshi":96196036,"scriptType":"0014","scriptPk":"` + hex.EncodeToString(changeScript) + `","codeType":5,"address":"` + changeAddress + `","isSpent":false,"isSpending":false}]}}`))
	}))
	defer unisatServer.Close()

	engine := Engine{
		Store:         s,
		StateAPI:      stateapi.New(stateServer.URL, "", time.Second, ""),
		UnisatOpenAPI: NewUnisatOpenAPIClient(unisatServer.URL, "test-key", time.Second),
		Config: config.Config{
			BitcoinRPC: config.BitcoinRPCConfig{Network: chaincfg.MainNetParams.Name},
			Signing:    config.SigningConfig{ChangeAddress: changeAddress},
			FeeAPI:     config.FeeAPIConfig{FixedFeeRateSatVB: 1},
			Tx:         config.TxConfig{SendChangeMinValue: 546},
			Runtime:    config.RuntimeConfig{Mode: "unisat_open_api", DisableBroadcast: true},
		},
		KeyMaterial: keyMaterial,
	}

	messageID, payload, err := engine.BuildProveMessage(ctx, 100, "100:1")
	if err != nil {
		t.Fatalf("BuildProveMessage() error = %v", err)
	}
	signed, err := engine.BuildAndSign(ctx, messageID, payload)
	if err != nil {
		t.Fatalf("BuildAndSign() error = %v", err)
	}
	if signed == "" {
		t.Fatal("signed tx is empty")
	}

	available, err := s.ListAvailableUTXOs(ctx)
	if err != nil {
		t.Fatalf("ListAvailableUTXOs() error = %v", err)
	}
	if len(available) != 0 {
		t.Fatalf("available utxos = %d, want 0 because unisat mode should not maintain local utxos", len(available))
	}
}

func TestBroadcastSignedUsesUnisatLocalPushTx(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unisat-push.db")
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

	var gotMethod, gotPath, gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
	}))
	defer server.Close()

	engine := Engine{
		Store:         s,
		UnisatOpenAPI: NewUnisatOpenAPIClient(server.URL, "test-key", time.Second),
		Config:        config.Config{Runtime: config.RuntimeConfig{Mode: "unisat_open_api"}},
	}

	txid, err := engine.BroadcastSigned(ctx, messageID, "abcd")
	if err != nil {
		t.Fatalf("BroadcastSigned() error = %v", err)
	}
	if txid != "abcd" {
		t.Fatalf("txid = %q, want %q", txid, "abcd")
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/indexer/local_pushtx" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/indexer/local_pushtx")
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization = %q, want %q", gotAuth, "Bearer test-key")
	}
	if gotBody != `{"txHex":"abcd"}` {
		t.Fatalf("body = %q, want %q", gotBody, `{"txHex":"abcd"}`)
	}
}

func TestNewUnisatOpenAPIClientNormalizesIndexerBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "root", in: "https://open-api.unisat.io", want: "https://open-api.unisat.io/v1/indexer"},
		{name: "root trailing slash", in: "https://open-api.unisat.io/", want: "https://open-api.unisat.io/v1/indexer"},
		{name: "already indexer", in: "https://open-api.unisat.io/v1/indexer", want: "https://open-api.unisat.io/v1/indexer"},
		{name: "already indexer trailing slash", in: "https://open-api.unisat.io/v1/indexer/", want: "https://open-api.unisat.io/v1/indexer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewUnisatOpenAPIClient(tt.in, "", time.Second)
			if client.BaseURL != tt.want {
				t.Fatalf("base url = %q, want %q", client.BaseURL, tt.want)
			}
		})
	}
}

func TestFilterUsedOpenAPIUTXOsPrunesMissingAndExcludesUsed(t *testing.T) {
	engine := Engine{
		Config: config.Config{Runtime: config.RuntimeConfig{Mode: "unisat_open_api"}},
		UsedOpenAPIUTXOs: map[string]struct{}{
			utxoKey("used", 1):  {},
			utxoKey("stale", 2): {},
		},
	}

	current := []model.UTXO{
		{TxID: "used", Vout: 1},
		{TxID: "fresh", Vout: 3},
	}

	engine.pruneUsedOpenAPIUTXOs(current)
	filtered := engine.filterUsedOpenAPIUTXOs(current)

	if len(filtered) != 1 {
		t.Fatalf("filtered utxos = %d, want 1", len(filtered))
	}
	if filtered[0].TxID != "fresh" || filtered[0].Vout != 3 {
		t.Fatalf("filtered utxo = %s:%d, want fresh:3", filtered[0].TxID, filtered[0].Vout)
	}
	if _, exists := engine.UsedOpenAPIUTXOs[utxoKey("stale", 2)]; exists {
		t.Fatal("stale utxo should be pruned from cache")
	}
	if _, exists := engine.UsedOpenAPIUTXOs[utxoKey("used", 1)]; !exists {
		t.Fatal("used utxo should remain in cache while api still returns it")
	}
}
