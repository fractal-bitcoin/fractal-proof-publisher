package feeapi

import "strings"

func SelectFeeRate(fees RecommendedFees, strategy string, minFeeRate, maxFeeRate int64) int64 {
	var selected int64
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "fastest":
		selected = fees.FastestFee
	case "hour", "hourfee":
		selected = fees.HourFee
	case "minimum", "min":
		selected = fees.MinimumFee
	case "half_hour", "halfhour", "half-hour", "":
		selected = fees.HalfHourFee
	default:
		selected = fees.HalfHourFee
	}

	if selected < minFeeRate {
		selected = minFeeRate
	}
	if maxFeeRate > 0 && selected > maxFeeRate {
		selected = maxFeeRate
	}
	if selected <= 0 {
		selected = 1
	}
	return selected
}
