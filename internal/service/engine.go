package service

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fractal-proof-publisher/internal/bitcoinrpc"
	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/feeapi"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/stateapi"
	"fractal-proof-publisher/internal/store"
)

type Engine struct {
	Store            *store.Store
	RPC              *bitcoinrpc.Client
	StateAPI         *stateapi.Client
	FeeAPI           *feeapi.Client
	UnisatOpenAPI    *UnisatOpenAPIClient
	Config           config.Config
	KeyMaterial      keys.KeyMaterial
	UsedOpenAPIUTXOs map[string]struct{}
	ProgressRetryAt  time.Time
	ProgressErrCount int
}

type UnisatOpenAPIClient struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

func NewUnisatOpenAPIClient(baseURL, key string, timeout time.Duration) *UnisatOpenAPIClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &UnisatOpenAPIClient{
		BaseURL: normalizeUnisatBaseURL(baseURL),
		Key:     normalizeBearerToken(key),
		HTTP:    &http.Client{Timeout: timeout},
	}
}

func normalizeUnisatBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, "/v1/indexer") {
		return value
	}
	return value + "/v1/indexer"
}

func normalizeBearerToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return value
	}
	return "Bearer " + value
}

func (e *Engine) runtimeMode() string {
	mode := strings.ToLower(strings.TrimSpace(e.Config.Runtime.Mode))
	if mode == "" {
		return "default"
	}
	return mode
}

func (e *Engine) isUnisatOpenAPIMode() bool {
	return e.runtimeMode() == "unisat_open_api"
}

func (e *Engine) RecoverOnce(ctx context.Context) error {
	return e.ProgressOnce(ctx)
}

func (e *Engine) setLastScannedHeight(ctx context.Context, height uint64) error {
	return e.Store.SetChainState(ctx, "last_scanned_height", strconv.FormatUint(height, 10))
}

func (e *Engine) ScanOnce(ctx context.Context) error {
	tip, err := e.RPC.GetBlockCount(ctx)
	if err != nil {
		return fmt.Errorf("get block count: %w", err)
	}

	start := e.Config.Scan.StartHeight
	lastScannedText, err := e.Store.GetChainState(ctx, "last_scanned_height")
	if err != nil {
		return err
	}
	if lastScannedText != "" {
		lastScanned, err := strconv.ParseUint(lastScannedText, 10, 64)
		if err != nil {
			return fmt.Errorf("parse last_scanned_height: %w", err)
		}
		if lastScanned >= start {
			start = lastScanned + 1
		}
		if e.Config.Scan.MaxReorgDepth > 0 {
			rewind := uint64(0)
			if lastScanned+1 > e.Config.Scan.MaxReorgDepth {
				rewind = lastScanned + 1 - e.Config.Scan.MaxReorgDepth
			}
			if rewind < start {
				start = rewind
			}
			if start < e.Config.Scan.StartHeight {
				start = e.Config.Scan.StartHeight
			}
		}
	}

	if start > tip {
		e.Logf("scan_noop start_height=%d tip=%d reason=tip_behind_start", start, tip)
		return nil
	}

	e.Logf("scan_start start_height=%d tip=%d", start, tip)

	indexerID, err := e.Store.GetChainState(ctx, "indexer_id")
	if err != nil {
		return err
	}

	for height := start; height <= tip; height++ {
		blockHash, err := e.RPC.GetBlockHash(ctx, height)
		if err != nil {
			return fmt.Errorf("get block hash at %d: %w", height, err)
		}
		previous, err := e.Store.GetBlock(ctx, height)
		if err != nil {
			return err
		}
		if previous.BlockHash != "" && previous.BlockHash != blockHash {
			e.Logf("reorg_detected height=%d previous_hash=%s new_hash=%s", height, previous.BlockHash, blockHash)
			if err := e.Store.MarkBlockOrphaned(ctx, height); err != nil {
				return err
			}
			if err := e.Store.RollbackOrphanedUTXOsByHeight(ctx, height); err != nil {
				return err
			}
			if err := e.Store.InvalidateOrphanedChangeUTXOsByHeight(ctx, height); err != nil {
				return err
			}
			if err := e.Store.MarkMessagesFailedByHeight(ctx, height, "block hash changed due to reorg"); err != nil {
				return err
			}
			indexerID, err = e.Store.GetChainState(ctx, "indexer_id")
			if err != nil {
				return err
			}
		}
		header, err := e.RPC.GetBlockHeader(ctx, blockHash)
		if err != nil {
			return fmt.Errorf("get block header at %d: %w", height, err)
		}

		eligible := header.Version == e.Config.Scan.TargetBlockVersion
		status := model.BlockStatusSkipped
		if eligible && uint64(header.Confirmations) < e.Config.Scan.RequiredConfirmations {
			status = model.BlockStatusWaitingConfirm
		}
		if eligible && uint64(header.Confirmations) >= e.Config.Scan.RequiredConfirmations {
			status = model.BlockStatusReady
		}
		if err := e.Store.UpsertBlock(ctx, height, blockHash, header.Version, uint64(header.Confirmations), eligible, status); err != nil {
			return err
		}

		if !eligible || uint64(header.Confirmations) < e.Config.Scan.RequiredConfirmations || indexerID == "" {
			if eligible && indexerID == "" {
				e.Logf("scan_wait_register height=%d confirmations=%d", height, header.Confirmations)
			}
			if err := e.setLastScannedHeight(ctx, height); err != nil {
				return err
			}
			continue
		}
		existingID, err := e.Store.FindMessageByHeightAndType(ctx, height, model.MessageTypeProve)
		if err != nil {
			return err
		}
		if existingID != 0 {
			e.Logf("scan_skip_existing_prove height=%d existing_message_id=%d", height, existingID)
			if err := e.setLastScannedHeight(ctx, height); err != nil {
				return err
			}
			continue
		}
		if _, err := e.CreateProveSubmission(ctx, height, indexerID); err != nil {
			if stateapi.IsRetryableHeightUnavailable(err) {
				e.Logf("scan_paused height=%d reason=state_api_not_ready err=%v", height, err)
				return nil
			}
			return fmt.Errorf("create prove submission at %d: %w", height, err)
		}
		e.Logf("scan_created_prove height=%d indexer_id=%s", height, indexerID)
		if err := e.setLastScannedHeight(ctx, height); err != nil {
			return err
		}
	}
	e.Logf("scan_done start_height=%d tip=%d", start, tip)
	return nil
}
