package txbuilder

import (
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

func newOutPoint(txid string, vout uint32) (*wire.OutPoint, error) {
	h, err := chainhash.NewHashFromStr(txid)
	if err != nil {
		return nil, fmt.Errorf("parse input txid %s: %w", txid, err)
	}
	return wire.NewOutPoint(h, vout), nil
}
