package txbuilder

import (
	"fractal-proof-publisher/internal/keys"

	"github.com/btcsuite/btcd/chaincfg"
)

func mainnetParams() *chaincfg.Params {
	return &chaincfg.MainNetParams
}

func p2wpkhScript(keyMaterial keys.KeyMaterial) ([]byte, error) {
	return keyMaterial.P2WPKHScript(mainnetParams())
}

func p2trScript(keyMaterial keys.KeyMaterial) ([]byte, error) {
	return keyMaterial.P2TRScript(mainnetParams())
}
