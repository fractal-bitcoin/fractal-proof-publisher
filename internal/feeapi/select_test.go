package feeapi

import "testing"

func TestSelectFeeRate(t *testing.T) {
	fees := RecommendedFees{FastestFee: 12, HalfHourFee: 8, HourFee: 5, MinimumFee: 2}
	got := SelectFeeRate(fees, "half_hour", 3, 10)
	if got != 8 {
		t.Fatalf("SelectFeeRate() = %d, want 8", got)
	}
	got = SelectFeeRate(fees, "hour", 6, 10)
	if got != 6 {
		t.Fatalf("SelectFeeRate() with min bound = %d, want 6", got)
	}
}
