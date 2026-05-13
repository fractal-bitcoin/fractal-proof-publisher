package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	BitcoinRPC BitcoinRPCConfig `json:"bitcoin_rpc"`
	Signing    SigningConfig    `json:"signing"`
	StateAPI   StateAPIConfig   `json:"state_api"`
	FeeAPI     FeeAPIConfig     `json:"fee_api"`
	Register   RegisterConfig   `json:"register"`
	Scan       ScanConfig       `json:"scan"`
	Tx         TxConfig         `json:"tx"`
	Database   DatabaseConfig   `json:"database"`
	Runtime    RuntimeConfig    `json:"runtime"`
}

type BitcoinRPCConfig struct {
	URL      string `json:"url"`
	User     string `json:"user"`
	Password string `json:"password"`
	Network  string `json:"network"`
}

type SigningConfig struct {
	PrivateKeyWIF string        `json:"private_key_wif"`
	PrivateKeyHex string        `json:"private_key_hex"`
	ChangeAddress string        `json:"change_address"`
	InitialUTXOs  []InitialUTXO `json:"initial_utxos"`
}

type InitialUTXO struct {
	TxID         string `json:"txid"`
	Vout         uint32 `json:"vout"`
	AmountSat    int64  `json:"amount_sat"`
	Address      string `json:"address"`
	ScriptPubKey string `json:"script_pub_key"`
	AddressType  string `json:"address_type"`
}

type StateAPIConfig struct {
	BaseURL  string        `json:"base_url"`
	Timeout  time.Duration `json:"timeout"`
	Auth     string        `json:"auth"`
	Provider string        `json:"provider"`
}

type FeeAPIConfig struct {
	BaseURL           string        `json:"base_url"`
	Timeout           time.Duration `json:"timeout"`
	Strategy          string        `json:"strategy"`
	MinFeeRateSatVB   int64         `json:"min_fee_rate_sat_vb"`
	MaxFeeRateSatVB   int64         `json:"max_fee_rate_sat_vb"`
	FixedFeeRateSatVB int64         `json:"fixed_fee_rate_sat_vb"`
}

type RegisterConfig struct {
	IndexRatioBP   uint16 `json:"index_ratio_bp"`
	RewardAddrType string `json:"reward_addr_type"`
	RewardAddr     string `json:"reward_addr"`
	Name           string `json:"name"`
	IndexerID      string `json:"indexer_id"`
}

type ScanConfig struct {
	StartHeight           uint64        `json:"start_height"`
	PollInterval          time.Duration `json:"poll_interval"`
	TargetBlockVersion    uint32        `json:"target_block_version"`
	RequiredConfirmations uint64        `json:"required_confirmations"`
	MaxReorgDepth         uint64        `json:"max_reorg_depth"`
}

type TxConfig struct {
	SendChangeMinValue int64 `json:"send_change_min_value"`
}

type DatabaseConfig struct {
	SQLitePath string `json:"sqlite_path"`
}

type RuntimeConfig struct {
	DryRun           bool   `json:"dry_run"`
	DisableBroadcast bool   `json:"disable_broadcast"`
	HealthAddr       string `json:"health_addr"`
	Mode             string `json:"mode"`
	UnisatOpenAPIURL string `json:"unisat_open_api_url"`
	UnisatOpenAPIKey string `json:"unisat_open_api_key"`
}

func Load() (Config, error) {
	path := strings.TrimSpace(os.Getenv("PUBLISHER_CONFIG"))
	if path == "" {
		path = "config.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	mode := strings.ToLower(strings.TrimSpace(c.Runtime.Mode))
	if mode == "" {
		mode = "default"
	}

	if c.BitcoinRPC.URL == "" {
		return errors.New("bitcoin_rpc.url is required")
	}
	if c.BitcoinRPC.Network == "" {
		return errors.New("bitcoin_rpc.network is required")
	}
	if c.Signing.PrivateKeyWIF == "" && c.Signing.PrivateKeyHex == "" {
		return errors.New("one of signing.private_key_wif or signing.private_key_hex is required")
	}
	if mode != "unisat_open_api" && len(c.Signing.InitialUTXOs) == 0 {
		return errors.New("signing.initial_utxos is required")
	}
	if c.StateAPI.BaseURL == "" {
		return errors.New("state_api.base_url is required")
	}
	if c.FeeAPI.FixedFeeRateSatVB <= 0 && c.FeeAPI.BaseURL == "" {
		return errors.New("fee_api.base_url is required when fee_api.fixed_fee_rate_sat_vb is not set")
	}
	if c.Database.SQLitePath == "" {
		return errors.New("database.sqlite_path is required")
	}
	if c.Scan.RequiredConfirmations == 0 {
		return errors.New("scan.required_confirmations must be greater than 0")
	}
	if mode == "unisat_open_api" {
		if strings.TrimSpace(c.Runtime.UnisatOpenAPIURL) == "" {
			return errors.New("runtime.unisat_open_api_url is required when runtime.mode is unisat_open_api")
		}
		if strings.TrimSpace(c.Runtime.UnisatOpenAPIKey) == "" {
			return errors.New("runtime.unisat_open_api_key is required when runtime.mode is unisat_open_api")
		}
	}
	return nil
}
