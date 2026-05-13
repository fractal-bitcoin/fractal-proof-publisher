package keys

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg"
)

func TestLoadAndAddress(t *testing.T) {
	material, err := Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(material.CompressedPubKey) == 0 {
		t.Fatal("compressed pubkey is empty")
	}
	if len(material.XOnlyPubKey) == 0 {
		t.Fatal("x-only pubkey is empty")
	}
	addr, err := material.Address(&chaincfg.MainNetParams, "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	if addr == "" {
		t.Fatal("address is empty")
	}
}
