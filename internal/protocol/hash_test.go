package protocol

import "testing"

func TestComputeProveHash(t *testing.T) {
	got, err := ComputeProveHash(
		"100:1",
		"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		"ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100",
	)
	if err != nil {
		t.Fatalf("ComputeProveHash() error = %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("hash len = %d, want 64", len(got))
	}
}
