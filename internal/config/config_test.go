package config

import "testing"

func TestValidateAllowsFixedFeeWithoutFeeAPIBaseURL(t *testing.T) {
	cfg := Config{
		BitcoinRPC: BitcoinRPCConfig{URL: "http://rpc", Network: "mainnet"},
		Signing: SigningConfig{
			PrivateKeyHex: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			ChangeAddress: "bc1ptest",
			InitialUTXOs: []InitialUTXO{{
				TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
				Vout:         0,
				AmountSat:    1,
				Address:      "bc1qtest",
				ScriptPubKey: "0014",
				AddressType:  "p2wpkh",
			}},
		},
		StateAPI: StateAPIConfig{BaseURL: "http://state"},
		FeeAPI: FeeAPIConfig{
			FixedFeeRateSatVB: 1,
		},
		Database: DatabaseConfig{SQLitePath: "./publisher.db"},
		Scan:     ScanConfig{RequiredConfirmations: 1},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRequiresUnisatOpenAPISettingsInUnisatMode(t *testing.T) {
	cfg := Config{
		BitcoinRPC: BitcoinRPCConfig{URL: "http://rpc", Network: "mainnet"},
		Signing: SigningConfig{
			PrivateKeyHex: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			ChangeAddress: "bc1ptest",
		},
		StateAPI: StateAPIConfig{BaseURL: "http://state"},
		FeeAPI:   FeeAPIConfig{FixedFeeRateSatVB: 1},
		Database: DatabaseConfig{SQLitePath: "./publisher.db"},
		Scan:     ScanConfig{RequiredConfirmations: 1},
		Runtime:  RuntimeConfig{Mode: "unisat_open_api"},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unisat config error")
	}
}
