package txbuilder

import (
	"encoding/hex"
	"fmt"
	"strings"

	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

func PreparePrevOutputFetcher(inputs []model.UTXO) (*txscript.MultiPrevOutFetcher, error) {
	fetcher := txscript.NewMultiPrevOutFetcher(nil)
	for _, in := range inputs {
		scriptBytes, err := hex.DecodeString(in.ScriptPubKey)
		if err != nil {
			return nil, fmt.Errorf("decode script pub key for %s:%d: %w", in.TxID, in.Vout, err)
		}
		outPoint, err := newOutPoint(in.TxID, in.Vout)
		if err != nil {
			return nil, err
		}
		fetcher.AddPrevOut(*outPoint, &wire.TxOut{Value: in.AmountSat, PkScript: scriptBytes})
	}
	return fetcher, nil
}

func SignP2WPKH(unsigned BuildResult, sigHashes *txscript.TxSigHashes, inputIndex int, utxo model.UTXO, keyMaterial keys.KeyMaterial) error {
	params := ParamsForNetwork(unsigned.Network)
	expectedScript, err := keyMaterial.P2WPKHScript(params)
	if err != nil {
		return fmt.Errorf("derive expected p2wpkh script: %w", err)
	}
	actualScript, err := hex.DecodeString(strings.TrimSpace(utxo.ScriptPubKey))
	if err != nil {
		return fmt.Errorf("decode actual p2wpkh script: %w", err)
	}
	if hex.EncodeToString(actualScript) != hex.EncodeToString(expectedScript) {
		return fmt.Errorf("p2wpkh script does not match signing key")
	}
	witness, err := txscript.WitnessSignature(
		unsigned.Tx,
		sigHashes,
		inputIndex,
		utxo.AmountSat,
		expectedScript,
		txscript.SigHashAll,
		keyMaterial.PrivateKey,
		true,
	)
	if err != nil {
		return fmt.Errorf("p2wpkh witness signature: %w", err)
	}
	unsigned.Tx.TxIn[inputIndex].Witness = witness
	return nil
}

func SignP2TR(unsigned BuildResult, sigHashes *txscript.TxSigHashes, inputIndex int, utxo model.UTXO, keyMaterial keys.KeyMaterial) error {
	params := ParamsForNetwork(unsigned.Network)
	expectedScript, err := keyMaterial.P2TRScript(params)
	if err != nil {
		return fmt.Errorf("derive expected p2tr script: %w", err)
	}
	actualScript, err := hex.DecodeString(strings.TrimSpace(utxo.ScriptPubKey))
	if err != nil {
		return fmt.Errorf("decode p2tr script pub key: %w", err)
	}
	if hex.EncodeToString(actualScript) != hex.EncodeToString(expectedScript) {
		return fmt.Errorf("p2tr script does not match signing key")
	}
	witness, err := txscript.TaprootWitnessSignature(
		unsigned.Tx,
		sigHashes,
		inputIndex,
		utxo.AmountSat,
		actualScript,
		txscript.SigHashDefault,
		keyMaterial.PrivateKey,
	)
	if err != nil {
		return fmt.Errorf("p2tr witness signature: %w", err)
	}
	unsigned.Tx.TxIn[inputIndex].Witness = witness
	return nil
}
