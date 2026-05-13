package txbuilder

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

func decodeAddress(address, network string) (btcutil.Address, error) {
	addr, err := btcutil.DecodeAddress(address, ParamsForNetwork(network))
	if err != nil {
		return nil, fmt.Errorf("decode address %s: %w", address, err)
	}
	return addr, nil
}

func ParamsForNetwork(network string) *chaincfg.Params {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "", "main", "mainnet":
		return &chaincfg.MainNetParams
	case "regtest", "regression", "regressiontest":
		return &chaincfg.RegressionNetParams
	case "testnet", "testnet3":
		return &chaincfg.TestNet3Params
	case "signet":
		return &chaincfg.SigNetParams
	default:
		return &chaincfg.MainNetParams
	}
}

func ScriptPubKeyHexForAddress(address string, params *chaincfg.Params) (string, error) {
	addr, err := btcutil.DecodeAddress(address, params)
	if err != nil {
		return "", fmt.Errorf("decode address %s: %w", address, err)
	}
	script, err := txscript.PayToAddrScript(addr)
	if err != nil {
		return "", fmt.Errorf("build script pub key for address %s: %w", address, err)
	}
	return hex.EncodeToString(script), nil
}

func AddressTypeForAddress(address string, params *chaincfg.Params) (string, error) {
	addr, err := btcutil.DecodeAddress(address, params)
	if err != nil {
		return "", fmt.Errorf("decode address %s: %w", address, err)
	}
	switch addr.(type) {
	case *btcutil.AddressWitnessPubKeyHash:
		return "p2wpkh", nil
	case *btcutil.AddressTaproot:
		return "p2tr", nil
	default:
		return "", fmt.Errorf("unsupported address type for %s", address)
	}
}
