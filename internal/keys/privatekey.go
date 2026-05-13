package keys

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

type KeyMaterial struct {
	PrivateKey       *btcec.PrivateKey
	PublicKey        *btcec.PublicKey
	PrivateKeyHex    string
	CompressedPubKey []byte
	XOnlyPubKey      []byte
}

func Load(privateKeyWIF, privateKeyHex string) (KeyMaterial, error) {
	if strings.TrimSpace(privateKeyHex) != "" {
		decoded, err := hex.DecodeString(strings.TrimSpace(privateKeyHex))
		if err != nil {
			return KeyMaterial{}, fmt.Errorf("decode private key hex: %w", err)
		}
		priv, pub := btcec.PrivKeyFromBytes(decoded)
		return newMaterial(priv, pub), nil
	}
	if strings.TrimSpace(privateKeyWIF) != "" {
		wif, err := btcutil.DecodeWIF(strings.TrimSpace(privateKeyWIF))
		if err != nil {
			return KeyMaterial{}, fmt.Errorf("decode private key wif: %w", err)
		}
		return newMaterial(wif.PrivKey, wif.PrivKey.PubKey()), nil
	}
	return KeyMaterial{}, fmt.Errorf("private key is required")
}

func (k KeyMaterial) Address(params *chaincfg.Params, addrType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(addrType)) {
	case "p2wpkh":
		script, err := k.P2WPKHScript(params)
		if err != nil {
			return "", err
		}
		_, addrs, _, err := txscript.ExtractPkScriptAddrs(script, params)
		if err != nil || len(addrs) == 0 {
			return "", fmt.Errorf("extract p2wpkh address: %w", err)
		}
		return addrs[0].EncodeAddress(), nil
	case "p2tr":
		script, err := k.P2TRScript(params)
		if err != nil {
			return "", err
		}
		_, addrs, _, err := txscript.ExtractPkScriptAddrs(script, params)
		if err != nil || len(addrs) == 0 {
			return "", fmt.Errorf("extract p2tr address: %w", err)
		}
		return addrs[0].EncodeAddress(), nil
	default:
		return "", fmt.Errorf("unsupported address type: %s", addrType)
	}
}

func (k KeyMaterial) P2WPKHScript(params *chaincfg.Params) ([]byte, error) {
	hash160 := btcutil.Hash160(k.CompressedPubKey)
	addr, err := btcutil.NewAddressWitnessPubKeyHash(hash160, params)
	if err != nil {
		return nil, fmt.Errorf("new p2wpkh address: %w", err)
	}
	return txscript.PayToAddrScript(addr)
}

func (k KeyMaterial) P2TRScript(params *chaincfg.Params) ([]byte, error) {
	tapKey := txscript.ComputeTaprootKeyNoScript(k.PublicKey)
	addr, err := btcutil.NewAddressTaproot(schnorr.SerializePubKey(tapKey), params)
	if err != nil {
		return nil, fmt.Errorf("new p2tr address: %w", err)
	}
	return txscript.PayToAddrScript(addr)
}

func newMaterial(priv *btcec.PrivateKey, pub *btcec.PublicKey) KeyMaterial {
	return KeyMaterial{
		PrivateKey:       priv,
		PublicKey:        pub,
		PrivateKeyHex:    hex.EncodeToString(priv.Serialize()),
		CompressedPubKey: pub.SerializeCompressed(),
		XOnlyPubKey:      schnorr.SerializePubKey(pub),
	}
}
