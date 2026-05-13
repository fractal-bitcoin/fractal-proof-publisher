package protocol

import (
	"testing"

	"fractal-proof-publisher/internal/model"
)

func TestEncodeRegisterText(t *testing.T) {
	payload, err := EncodeRegisterText(model.RegisterData{
		IndexRatioBP:   100,
		RewardAddrType: "p2tr",
		RewardAddr:     "bc1ptest",
		Name:           "hello",
	})
	if err != nil {
		t.Fatalf("EncodeRegisterText() error = %v", err)
	}
	want := "fip101,1,register_indexer,100,bc1ptest,hello"
	if string(payload) != want {
		t.Fatalf("payload = %q, want %q", string(payload), want)
	}
}

func TestEncodeProveText(t *testing.T) {
	payload, err := EncodeProveText(model.ProveData{
		IndexerID:   "100:2",
		ProveHeight: 888,
		ProveHash:   "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
	})
	if err != nil {
		t.Fatalf("EncodeProveText() error = %v", err)
	}
	want := "fip101,1,submit_proof,100:2,888,00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if string(payload) != want {
		t.Fatalf("payload = %q, want %q", string(payload), want)
	}
}
