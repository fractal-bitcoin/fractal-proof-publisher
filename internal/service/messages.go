package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"fractal-proof-publisher/internal/feeapi"
	"fractal-proof-publisher/internal/inscription"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/protocol"
	"fractal-proof-publisher/internal/stateapi"
	"fractal-proof-publisher/internal/txbuilder"
)

func (e *Engine) BuildRegisterMessage(ctx context.Context, data model.RegisterData) (int64, string, error) {
	return e.BuildRegisterMessageFromHeight(ctx, data, nil)
}

func (e *Engine) BuildRegisterMessageFromHeight(ctx context.Context, data model.RegisterData, relatedHeight *uint64) (int64, string, error) {
	payload, err := protocol.EncodeRegisterText(data)
	if err != nil {
		return 0, "", err
	}
	messageID, err := e.Store.CreateMessage(ctx, model.MessageTypeRegister, string(payload), relatedHeight, "")
	if err != nil {
		return 0, "", err
	}
	e.Logf("register_message_created message_id=%d name=%s reward_addr_type=%s index_ratio_bp=%d", messageID, data.Name, data.RewardAddrType, data.IndexRatioBP)
	return messageID, string(payload), nil
}

func (e *Engine) BuildProveMessage(ctx context.Context, height uint64, indexerID string) (int64, string, error) {
	state, err := e.StateAPI.GetHeightState(ctx, height)
	if err != nil {
		return 0, "", err
	}
	if strings.TrimSpace(state.StateHash) == "" {
		return 0, "", &stateapi.StatusError{
			StatusCode: http.StatusNotFound,
			URL:        fmt.Sprintf("height:%d", height),
			Body:       "state hash is empty",
		}
	}
	if strings.TrimSpace(state.BlockHash) == "" {
		if e.RPC == nil {
			return 0, "", fmt.Errorf("state api returned empty block hash for height %d and rpc client is not configured", height)
		}
		state.BlockHash, err = e.RPC.GetBlockHash(ctx, height)
		if err != nil {
			return 0, "", fmt.Errorf("fallback get block hash at %d: %w", height, err)
		}
	}
	proveHash, err := protocol.ComputeProveHash(indexerID, state.BlockHash, state.StateHash)
	if err != nil {
		return 0, "", err
	}
	payload, err := protocol.EncodeProveText(model.ProveData{IndexerID: indexerID, ProveHeight: height, ProveHash: proveHash})
	if err != nil {
		return 0, "", err
	}
	messageID, err := e.Store.CreateMessage(ctx, model.MessageTypeProve, string(payload), &height, indexerID)
	if err != nil {
		return 0, "", err
	}
	e.Logf("prove_message_created message_id=%d height=%d indexer_id=%s prove_hash=%s", messageID, height, indexerID, proveHash)
	return messageID, string(payload), nil
}

func (e *Engine) BuildAndSign(ctx context.Context, messageID int64, payload string) (string, error) {
	e.Logf("build_sign_start message_id=%d payload_bytes=%d", messageID, len(payload))
	available, err := e.listAvailableUTXOs(ctx)
	if err != nil {
		return "", err
	}
	available = filterSpendableUTXOs(available)
	if len(available) == 0 {
		return "", fmt.Errorf("no available utxos")
	}

	envelope, err := inscription.NewTextEnvelope([]byte(payload))
	if err != nil {
		return "", err
	}
	params := txbuilder.ParamsForNetwork(e.Config.BitcoinRPC.Network)
	signerAddressType, err := txbuilder.AddressTypeForAddress(e.Config.Signing.ChangeAddress, params)
	if err != nil {
		return "", err
	}
	commitPlan := envelope.CommitPlan(e.KeyMaterial.PublicKey, signerAddressType)
	feeRate := e.Config.FeeAPI.FixedFeeRateSatVB
	if feeRate <= 0 {
		if e.FeeAPI == nil {
			return "", fmt.Errorf("fee api client is not configured")
		}
		fees, err := e.FeeAPI.RecommendedFees(ctx)
		if err != nil {
			return "", err
		}
		feeRate = feeapi.SelectFeeRate(fees, e.Config.FeeAPI.Strategy, e.Config.FeeAPI.MinFeeRateSatVB, e.Config.FeeAPI.MaxFeeRateSatVB)
	}
	revealOpReturn := revealOpReturnPayload(payload)
	revealOutputValue := txbuilder.DefaultRevealPostage
	minimumCommitOutputValue := revealOutputValue
	if len(revealOpReturn) > 0 {
		revealOutputValue = 0
		minimumCommitOutputValue = txbuilder.DefaultOpReturnValue
	}
	sendChangeMinValue := e.Config.Tx.SendChangeMinValue
	if sendChangeMinValue <= 0 {
		sendChangeMinValue = txbuilder.DefaultSendChangeMin
	}
	revealVBytes, revealFeeValue, err := txbuilder.EstimateRevealFeeWithOpReturn(commitPlan, e.Config.BitcoinRPC.Network, e.Config.Signing.ChangeAddress, feeRate, revealOpReturn)
	if err != nil {
		return "", err
	}
	var funding txbuilder.FundingPlan
	if len(revealOpReturn) > 0 {
		funding, err = txbuilder.PlanFundingWithoutCommitChange(available, feeRate, minimumCommitOutputValue+revealFeeValue)
	} else {
		funding, err = txbuilder.PlanFunding(available, feeRate, minimumCommitOutputValue+revealFeeValue, sendChangeMinValue)
	}
	if err != nil {
		return "", err
	}
	if len(revealOpReturn) > 0 {
		revealOutputValue = funding.CommitOutputValue - revealFeeValue - txbuilder.DefaultOpReturnValue
		if revealOutputValue < 0 {
			return "", fmt.Errorf("negative reveal change value: %d", revealOutputValue)
		}
	}
	e.Logf(
		"build_sign_plan message_id=%d available_utxos=%d selected_inputs=%d fee_rate_sat_vb=%d commit_output_sat=%d reveal_fee_sat=%d change_sat=%d total_fee_sat=%d",
		messageID,
		len(available),
		len(funding.SelectedInputs),
		feeRate,
		funding.CommitOutputValue,
		revealFeeValue,
		funding.ChangeValue,
		funding.FeeValue,
	)
	e.markUsedOpenAPIUTXOs(funding.SelectedInputs)
	if !e.isUnisatOpenAPIMode() {
		if err := e.Store.ReserveUTXOs(ctx, messageID, funding.SelectedInputs); err != nil {
			return "", err
		}
	}

	unsigned, err := txbuilder.Build(txbuilder.BuildInput{
		Inputs:            funding.SelectedInputs,
		ChangeAddress:     e.Config.Signing.ChangeAddress,
		Network:           e.Config.BitcoinRPC.Network,
		CommitPlan:        commitPlan,
		FeeRateSatVB:      feeRate,
		CommitOutputValue: funding.CommitOutputValue,
		ChangeValue:       funding.ChangeValue,
		RevealOutputValue: revealOutputValue,
		RevealRecipient:   e.Config.Signing.ChangeAddress,
		RevealOpReturn:    revealOpReturn,
	})
	if err != nil {
		if !e.isUnisatOpenAPIMode() {
			_ = e.Store.ReleaseReservedUTXOs(ctx, messageID)
		}
		return "", err
	}
	unsigned.FeeValue = funding.FeeValue
	unsigned.EstimatedVBytes = funding.EstimatedVBytes
	unsigned.Reveal.EstimatedVBytes = revealVBytes
	unsigned.Reveal.FeeValue = revealFeeValue

	signed, err := txbuilder.Sign(unsigned, e.KeyMaterial)
	if err != nil {
		if !e.isUnisatOpenAPIMode() {
			_ = e.Store.ReleaseReservedUTXOs(ctx, messageID)
		}
		return "", err
	}
	finalizedReveal, err := txbuilder.FinalizeRevealFromCommitHex(unsigned.Reveal, signed)
	if err != nil {
		if !e.isUnisatOpenAPIMode() {
			_ = e.Store.ReleaseReservedUTXOs(ctx, messageID)
		}
		return "", err
	}
	finalizedReveal, err = txbuilder.SignRevealPlan(finalizedReveal, e.KeyMaterial)
	if err != nil {
		if !e.isUnisatOpenAPIMode() {
			_ = e.Store.ReleaseReservedUTXOs(ctx, messageID)
		}
		return "", err
	}
	unsigned.Reveal = finalizedReveal
	revealTxID, err := broadcastTxID(finalizedReveal.RawTxHex)
	if err != nil {
		if !e.isUnisatOpenAPIMode() {
			_ = e.Store.ReleaseReservedUTXOs(ctx, messageID)
		}
		return "", err
	}
	if !e.isUnisatOpenAPIMode() {
		if unsigned.ChangeValue > 0 {
			if err := e.insertBroadcastChangeUTXO(ctx, messageID, signed); err != nil {
				_ = e.Store.ReleaseReservedUTXOs(ctx, messageID)
				return "", err
			}
		}
		if finalizedReveal.RecipientValue > 0 {
			if err := e.insertBroadcastChangeUTXO(ctx, messageID, finalizedReveal.RawTxHex); err != nil {
				_ = e.Store.ReleaseReservedUTXOs(ctx, messageID)
				return "", err
			}
		}
	}
	if err := e.Store.MarkMessageSignedWithReveal(ctx, messageID, signed, finalizedReveal.RawTxHex, revealTxID); err != nil {
		return "", err
	}
	e.Logf("build_sign_done message_id=%d commit_txid=%s reveal_txid=%s reveal_vbytes=%d", messageID, mustBroadcastTxID(signed), revealTxID, revealVBytes)
	return signed, nil
}

func filterSpendableUTXOs(utxos []model.UTXO) []model.UTXO {
	filtered := utxos[:0]
	for _, utxo := range utxos {
		if utxo.AmountSat <= txbuilder.DefaultRevealPostage {
			continue
		}
		filtered = append(filtered, utxo)
	}
	return filtered
}

func revealOpReturnPayload(payload string) []byte {
	parts := strings.Split(payload, ",")
	if len(parts) < 3 {
		return nil
	}
	if strings.TrimSpace(parts[0]) != protocol.ProtocolName {
		return nil
	}
	if strings.TrimSpace(parts[1]) != protocol.ProtocolVersion {
		return nil
	}
	if strings.TrimSpace(parts[2]) != protocol.OpProve {
		return nil
	}
	return []byte("FIP-101:" + protocol.OpProve + ":reveal")
}

func (e *Engine) insertBroadcastChangeUTXO(ctx context.Context, messageID int64, txHex string) error {
	changeUTXO, err := buildBroadcastChangeUTXO(txHex, e.Config.Signing.ChangeAddress, e.Config.BitcoinRPC.Network)
	if err != nil {
		return err
	}
	if changeUTXO == nil {
		return nil
	}
	return e.Store.InsertChangeUTXO(ctx, messageID, *changeUTXO)
}

func (e *Engine) listAvailableUTXOs(ctx context.Context) ([]model.UTXO, error) {
	if !e.isUnisatOpenAPIMode() {
		return e.Store.ListAvailableUTXOs(ctx)
	}
	if e.UnisatOpenAPI == nil {
		return nil, fmt.Errorf("unisat open api client is not configured")
	}
	utxos, err := e.UnisatOpenAPI.AvailableUTXOs(ctx, e.Config.Signing.ChangeAddress)
	if err != nil {
		return nil, err
	}
	e.pruneUsedOpenAPIUTXOs(utxos)
	return e.filterUsedOpenAPIUTXOs(utxos), nil
}

func (e *Engine) filterUsedOpenAPIUTXOs(utxos []model.UTXO) []model.UTXO {
	if len(e.UsedOpenAPIUTXOs) == 0 {
		return utxos
	}
	filtered := make([]model.UTXO, 0, len(utxos))
	for _, utxo := range utxos {
		if _, exists := e.UsedOpenAPIUTXOs[utxoKey(utxo.TxID, utxo.Vout)]; exists {
			continue
		}
		filtered = append(filtered, utxo)
	}
	return filtered
}

func (e *Engine) pruneUsedOpenAPIUTXOs(utxos []model.UTXO) {
	if len(e.UsedOpenAPIUTXOs) == 0 {
		return
	}
	current := make(map[string]struct{}, len(utxos))
	for _, utxo := range utxos {
		current[utxoKey(utxo.TxID, utxo.Vout)] = struct{}{}
	}
	for key := range e.UsedOpenAPIUTXOs {
		if _, exists := current[key]; !exists {
			delete(e.UsedOpenAPIUTXOs, key)
		}
	}
}

func (e *Engine) markUsedOpenAPIUTXOs(utxos []model.UTXO) {
	if !e.isUnisatOpenAPIMode() || len(utxos) == 0 {
		return
	}
	if e.UsedOpenAPIUTXOs == nil {
		e.UsedOpenAPIUTXOs = make(map[string]struct{}, len(utxos))
	}
	for _, utxo := range utxos {
		e.UsedOpenAPIUTXOs[utxoKey(utxo.TxID, utxo.Vout)] = struct{}{}
	}
}

func utxoKey(txid string, vout uint32) string {
	return fmt.Sprintf("%s:%d", txid, vout)
}

type unisatOpenAPIResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		UTXO []struct {
			TxID       string `json:"txid"`
			Vout       uint32 `json:"vout"`
			Satoshi    int64  `json:"satoshi"`
			ScriptType string `json:"scriptType"`
			ScriptPk   string `json:"scriptPk"`
			Address    string `json:"address"`
			CodeType   int    `json:"codeType"`
			IsSpent    bool   `json:"isSpent"`
			IsSpending bool   `json:"isSpending"`
		} `json:"utxo"`
	} `json:"data"`
}

func (c *UnisatOpenAPIClient) AvailableUTXOs(ctx context.Context, address string) ([]model.UTXO, error) {
	requestURL := c.BaseURL + "/address/" + url.PathEscape(strings.TrimSpace(address)) + "/available-utxo-data"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if c.Key != "" {
		req.Header.Set("Authorization", c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, requestURL, strings.TrimSpace(string(body)))
	}

	var parsed unisatOpenAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Code != 0 {
		msg := strings.TrimSpace(parsed.Msg)
		if msg == "" {
			msg = "unisat open api returned non-zero code"
		}
		return nil, fmt.Errorf("%s", msg)
	}

	result := make([]model.UTXO, 0, len(parsed.Data.UTXO))
	for _, item := range parsed.Data.UTXO {
		if item.IsSpent || item.IsSpending {
			continue
		}
		result = append(result, model.UTXO{
			TxID:         item.TxID,
			Vout:         item.Vout,
			AmountSat:    item.Satoshi,
			Address:      item.Address,
			ScriptPubKey: strings.ToLower(strings.TrimSpace(item.ScriptPk)),
			AddressType:  unisatAddressType(item.ScriptType, item.ScriptPk, item.CodeType),
			Status:       model.UTXOStatusAvailable,
			Source:       model.UTXOSourceConfig,
		})
	}
	return result, nil
}

type unisatPushTxRequest struct {
	TxHex string `json:"txHex"`
}

type unisatPushTxResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type unisatTxStatusResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (c *UnisatOpenAPIClient) PushTx(ctx context.Context, txHex string) (string, error) {
	requestURL := c.BaseURL + "/local_pushtx"
	payload, err := json.Marshal(unisatPushTxRequest{TxHex: txHex})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		req.Header.Set("Authorization", c.Key)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, requestURL, strings.TrimSpace(string(body)))
	}

	var parsed unisatPushTxResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if parsed.Code != 0 {
		msg := strings.TrimSpace(parsed.Msg)
		if msg == "" {
			msg = "unisat open api push tx returned non-zero code"
		}
		return "", fmt.Errorf("%s", msg)
	}

	txid, err := broadcastTxID(txHex)
	if err != nil {
		return "", err
	}
	return txid, nil
}

func (c *UnisatOpenAPIClient) HasTx(ctx context.Context, txid string) (bool, error) {
	requestURL := c.BaseURL + "/tx/" + url.PathEscape(strings.TrimSpace(txid))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return false, fmt.Errorf("new request: %w", err)
	}
	if c.Key != "" {
		req.Header.Set("Authorization", c.Key)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, requestURL, strings.TrimSpace(string(body)))
	}

	var parsed unisatTxStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Code != 0 {
		msg := strings.TrimSpace(parsed.Msg)
		if msg == "" {
			msg = "unisat tx query returned non-zero code"
		}
		return false, fmt.Errorf("%s", msg)
	}
	return len(parsed.Data) > 0 && string(parsed.Data) != "null", nil
}

func unisatAddressType(scriptType, scriptPk string, codeType int) string {
	scriptType = strings.ToLower(strings.TrimSpace(scriptType))
	scriptPk = strings.ToLower(strings.TrimSpace(scriptPk))

	switch {
	case strings.HasPrefix(scriptPk, "5120"), scriptType == "5120":
		return "p2tr"
	case strings.HasPrefix(scriptPk, "0014"), scriptType == "0014":
		return "p2wpkh"
	case strings.HasPrefix(scriptPk, "a914"), scriptType == "a914":
		return "p2sh-p2wpkh"
	case strings.HasPrefix(scriptPk, "76a914"), scriptType == "76a914":
		return "p2pkh"
	}

	switch codeType {
	case 1:
		return "p2pkh"
	case 2, 3:
		return "p2sh-p2wpkh"
	case 4, 5:
		return "p2wpkh"
	case 6, 7:
		return "p2tr"
	default:
		return ""
	}
}

func (e *Engine) BroadcastSigned(ctx context.Context, messageID int64, signedHex string) (string, error) {
	e.Logf("commit_broadcast_start message_id=%d disable_broadcast=%t", messageID, e.Config.Runtime.DisableBroadcast)
	if e.Config.Runtime.DisableBroadcast {
		txid, err := broadcastTxID(signedHex)
		if err != nil {
			return "", err
		}
		if err := e.Store.MarkMessageBroadcasted(ctx, messageID, txid); err != nil {
			return "", err
		}
		if !e.isUnisatOpenAPIMode() {
			if err := e.Store.MarkReservedUTXOsSpent(ctx, messageID, txid); err != nil {
				return "", err
			}
		}
		e.Logf("commit_broadcast_done message_id=%d txid=%s simulated=true", messageID, txid)
		return txid, nil
	}

	var txid string
	var err error
	if e.isUnisatOpenAPIMode() {
		if e.UnisatOpenAPI == nil {
			return "", fmt.Errorf("unisat open api client is not configured")
		}
		txid, err = e.UnisatOpenAPI.PushTx(ctx, signedHex)
	} else {
		txid, err = e.RPC.SendRawTransaction(ctx, signedHex)
	}
	if err != nil {
		_ = e.Store.CreateBroadcastAttempt(ctx, messageID, "commit", err.Error())
		if isTxOutputsAlreadyInUTXOSet(err) {
			txid, txidErr := broadcastTxID(signedHex)
			if txidErr != nil {
				e.Logf("commit_broadcast_duplicate_txid_failed message_id=%d err=%v original_err=%v", messageID, txidErr, err)
				return "", err
			}
			if markErr := e.Store.MarkMessageBroadcasted(ctx, messageID, txid); markErr != nil {
				return "", markErr
			}
			if !e.isUnisatOpenAPIMode() {
				if markErr := e.Store.MarkReservedUTXOsSpent(ctx, messageID, txid); markErr != nil {
					return "", markErr
				}
			}
			e.Logf("commit_broadcast_already_in_utxo_set message_id=%d txid=%s", messageID, txid)
			return txid, nil
		}
		e.Logf("commit_broadcast_failed message_id=%d err=%v", messageID, err)
		return "", err
	}
	if err := e.Store.MarkMessageBroadcasted(ctx, messageID, txid); err != nil {
		return "", err
	}
	if !e.isUnisatOpenAPIMode() {
		if err := e.Store.MarkReservedUTXOsSpent(ctx, messageID, txid); err != nil {
			return "", err
		}
	}
	e.Logf("commit_broadcast_done message_id=%d txid=%s simulated=false", messageID, txid)
	return txid, nil
}

func (e *Engine) BroadcastReveal(ctx context.Context, parentMessageID int64) (string, error) {
	message, err := e.Store.GetMessage(ctx, parentMessageID)
	if err != nil {
		return "", err
	}
	if message.ID == 0 {
		return "", fmt.Errorf("message not found for %d", parentMessageID)
	}
	if message.RevealRawTxHex == "" {
		return "", fmt.Errorf("reveal raw tx is empty for parent %d", parentMessageID)
	}
	e.LogMessagef(message, "reveal_broadcast_start disable_broadcast=%t", e.Config.Runtime.DisableBroadcast)
	if e.Config.Runtime.DisableBroadcast {
		txid, err := broadcastTxID(message.RevealRawTxHex)
		if err != nil {
			return "", err
		}
		if err := e.Store.MarkRevealBroadcasted(ctx, message.ID, txid); err != nil {
			return "", err
		}
		e.LogMessagef(message, "reveal_broadcast_done txid=%s simulated=true", txid)
		return txid, nil
	}

	var txid string
	if e.isUnisatOpenAPIMode() {
		if e.UnisatOpenAPI == nil {
			return "", fmt.Errorf("unisat open api client is not configured")
		}
		txid, err = e.UnisatOpenAPI.PushTx(ctx, message.RevealRawTxHex)
	} else {
		txid, err = e.RPC.SendRawTransaction(ctx, message.RevealRawTxHex)
	}
	if err != nil {
		_ = e.Store.CreateBroadcastAttempt(ctx, message.ID, "reveal", err.Error())
		if isTxOutputsAlreadyInUTXOSet(err) {
			txid, txidErr := broadcastTxID(message.RevealRawTxHex)
			if txidErr != nil {
				e.LogMessagef(message, "reveal_broadcast_duplicate_txid_failed err=%v original_err=%v", txidErr, err)
				return "", err
			}
			if markErr := e.Store.MarkRevealBroadcasted(ctx, message.ID, txid); markErr != nil {
				return "", markErr
			}
			e.LogMessagef(message, "reveal_broadcast_already_in_utxo_set txid=%s", txid)
			return txid, nil
		}
		e.LogMessagef(message, "reveal_broadcast_failed err=%v", err)
		return "", err
	}
	if err := e.Store.MarkRevealBroadcasted(ctx, message.ID, txid); err != nil {
		return "", err
	}
	e.LogMessagef(message, "reveal_broadcast_done txid=%s simulated=false", txid)
	return txid, nil
}
