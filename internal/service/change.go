package service

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/txbuilder"

	"github.com/btcsuite/btcd/wire"
)

func parseSignedTx(signedHex string) (*wire.MsgTx, string, error) {
	rawTx, err := hex.DecodeString(signedHex)
	if err != nil {
		return nil, "", fmt.Errorf("decode signed tx: %w", err)
	}
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(rawTx)); err != nil {
		return nil, "", fmt.Errorf("deserialize signed tx: %w", err)
	}
	return &tx, tx.TxHash().String(), nil
}

func broadcastTxID(signedHex string) (string, error) {
	_, txid, err := parseSignedTx(signedHex)
	if err == nil {
		return txid, nil
	}

	fallback := strings.ToLower(strings.TrimSpace(signedHex))
	if len(fallback) > 64 {
		fallback = fallback[:64]
	}
	if fallback == "" {
		return "", err
	}
	return fallback, nil
}

func mustBroadcastTxID(signedHex string) string {
	txid, err := broadcastTxID(signedHex)
	if err != nil {
		return ""
	}
	return txid
}

func buildBroadcastChangeUTXO(signedHex, changeAddress, network string) (*model.UTXO, error) {
	if signedHex == "" || changeAddress == "" {
		return nil, nil
	}

	params := txbuilder.ParamsForNetwork(network)
	changeScript, err := txbuilder.ScriptPubKeyHexForAddress(changeAddress, params)
	if err != nil {
		return nil, err
	}
	changeAddressType, err := txbuilder.AddressTypeForAddress(changeAddress, params)
	if err != nil {
		return nil, err
	}

	tx, txid, err := parseSignedTx(signedHex)
	if err != nil {
		return nil, err
	}
	for vout, out := range tx.TxOut {
		if hex.EncodeToString(out.PkScript) != changeScript {
			continue
		}
		return &model.UTXO{
			TxID:         txid,
			Vout:         uint32(vout),
			AmountSat:    out.Value,
			Address:      changeAddress,
			ScriptPubKey: changeScript,
			AddressType:  changeAddressType,
			Status:       model.UTXOStatusPending,
			Source:       model.UTXOSourceChange,
		}, nil
	}

	return nil, nil
}
