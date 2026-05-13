package txbuilder

import (
	"fmt"

	"fractal-proof-publisher/internal/model"
)

func SelectInputs(utxos []model.UTXO, targetAmount int64) ([]model.UTXO, int64, error) {
	var selected []model.UTXO
	var total int64
	for _, utxo := range utxos {
		selected = append(selected, utxo)
		total += utxo.AmountSat
		if total >= targetAmount {
			return selected, total, nil
		}
	}
	return nil, 0, fmt.Errorf("insufficient funds: need %d", targetAmount)
}
