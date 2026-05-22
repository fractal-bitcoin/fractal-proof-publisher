package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fractal-proof-publisher/internal/bitcoinrpc"
	"fractal-proof-publisher/internal/config"
	"fractal-proof-publisher/internal/feeapi"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"
	"fractal-proof-publisher/internal/protocol"
	"fractal-proof-publisher/internal/service"
	"fractal-proof-publisher/internal/stateapi"
	"fractal-proof-publisher/internal/store"
)

type statusResponse struct {
	OK                   bool   `json:"ok"`
	Mode                 string `json:"mode"`
	ProgressPendingCount int64  `json:"progress_pending_count"`
	DoneCount            int64  `json:"done_count"`
	FailedCount          int64  `json:"failed_count"`
	IndexerID            string `json:"indexer_id,omitempty"`
	LastScannedHeight    string `json:"last_scanned_height,omitempty"`
	LastSeenTip          string `json:"last_seen_tip,omitempty"`
}

type messageRebuildRequest struct {
	MessageID int64  `json:"message_id"`
	Height    uint64 `json:"height"`
}

type messageRebuildResponse struct {
	OK        bool                `json:"ok"`
	MessageID int64               `json:"message_id"`
	Previous  messageStateSummary `json:"previous"`
}

type messageStateSummary struct {
	Type       model.MessageType   `json:"type"`
	Status     model.MessageStatus `json:"status"`
	Height     uint64              `json:"height,omitempty"`
	IndexerID  string              `json:"indexer_id,omitempty"`
	TxID       string              `json:"txid,omitempty"`
	RevealTxID string              `json:"reveal_txid,omitempty"`
	RevealSent bool                `json:"reveal_sent"`
	RevealDone bool                `json:"reveal_done"`
}

func Run(ctx context.Context, cfg config.Config) error {
	return RunMode(ctx, cfg, "run")
}

func RunMode(ctx context.Context, cfg config.Config, mode string) error {
	if cfg.Runtime.DryRun {
		log.Printf("publisher mode=%s dry_run=true exiting without work", mode)
		return nil
	}

	s, err := store.Open(cfg.Database.SQLitePath)
	if err != nil {
		return err
	}
	defer s.DB.Close()

	if err := s.SeedInitialUTXOs(ctx, cfg.Signing.InitialUTXOs); err != nil {
		return err
	}

	keyMaterial, err := keys.Load(cfg.Signing.PrivateKeyWIF, cfg.Signing.PrivateKeyHex)
	if err != nil {
		return err
	}

	rpc := bitcoinrpc.New(cfg.BitcoinRPC.URL, cfg.BitcoinRPC.User, cfg.BitcoinRPC.Password)
	stateClient := stateapi.New(cfg.StateAPI.BaseURL, cfg.StateAPI.Auth, cfg.StateAPI.Timeout, cfg.StateAPI.Provider)
	var feeClient *feeapi.Client
	if cfg.FeeAPI.FixedFeeRateSatVB <= 0 {
		feeClient = feeapi.New(cfg.FeeAPI.BaseURL, cfg.FeeAPI.Timeout)
	}
	var unisatOpenAPI *service.UnisatOpenAPIClient
	if strings.EqualFold(strings.TrimSpace(cfg.Runtime.Mode), "unisat_open_api") {
		unisatOpenAPI = service.NewUnisatOpenAPIClient(cfg.Runtime.UnisatOpenAPIURL, cfg.Runtime.UnisatOpenAPIKey, cfg.StateAPI.Timeout)
	}

	engine := service.Engine{
		Store:         s,
		RPC:           rpc,
		StateAPI:      stateClient,
		FeeAPI:        feeClient,
		UnisatOpenAPI: unisatOpenAPI,
		Config:        cfg,
		KeyMaterial:   keyMaterial,
	}

	if err := startHealthServer(ctx, cfg.Runtime.HealthAddr, s, &engine, mode); err != nil {
		return err
	}

	if cfg.Scan.PollInterval <= 0 {
		cfg.Scan.PollInterval = 30 * time.Second
		engine.Config.Scan.PollInterval = cfg.Scan.PollInterval
	}

	log.Printf(
		"publisher mode=%s runtime_mode=%s network=%s db=%s start_height=%d poll_interval=%s target_block_version=%d required_confirmations=%d disable_broadcast=%t",
		mode,
		engine.Config.Runtime.Mode,
		cfg.BitcoinRPC.Network,
		cfg.Database.SQLitePath,
		cfg.Scan.StartHeight,
		cfg.Scan.PollInterval,
		cfg.Scan.TargetBlockVersion,
		cfg.Scan.RequiredConfirmations,
		cfg.Runtime.DisableBroadcast,
	)

	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "register":
		log.Printf("publisher mode=register running single register submission")
		return runRegisterOnce(ctx, &engine)
	}

	if err := runLoopOnce(ctx, &engine); err != nil {
		return err
	}

	ticker := time.NewTicker(cfg.Scan.PollInterval)
	defer ticker.Stop()

	for {
		lastHeight, err := rpc.GetBlockCount(ctx)
		if err == nil {
			_ = s.SetChainState(ctx, "last_seen_tip", strconv.FormatUint(lastHeight, 10))
		}

		select {
		case <-ctx.Done():
			log.Printf("publisher mode=%s shutdown requested", mode)
			return nil
		case <-ticker.C:
			if err := runLoopOnce(ctx, &engine); err != nil {
				return err
			}
		}
	}
}

func startHealthServer(ctx context.Context, addr string, s *store.Store, engine *service.Engine, mode string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		resp := statusResponse{
			OK:   true,
			Mode: mode,
		}
		pendingCount, err := s.CountMessagesByStatus(
			ctx,
			model.MessageStatusBuilding,
			model.MessageStatusCommitSigned,
			model.MessageStatusCommitSent,
			model.MessageStatusCommitConfirmed,
			model.MessageStatusRevealSent,
		)
		if err != nil {
			http.Error(w, fmt.Sprintf("count pending messages: %v", err), http.StatusInternalServerError)
			return
		}
		doneCount, err := s.CountMessagesByStatus(ctx, model.MessageStatusDone)
		if err != nil {
			http.Error(w, fmt.Sprintf("count done messages: %v", err), http.StatusInternalServerError)
			return
		}
		failedCount, err := s.CountMessagesByStatus(ctx, model.MessageStatusFailed)
		if err != nil {
			http.Error(w, fmt.Sprintf("count failed messages: %v", err), http.StatusInternalServerError)
			return
		}
		resp.ProgressPendingCount = pendingCount
		resp.DoneCount = doneCount
		resp.FailedCount = failedCount

		resp.IndexerID, _ = s.GetChainState(ctx, "indexer_id")
		resp.LastScannedHeight, _ = s.GetChainState(ctx, "last_scanned_height")
		resp.LastSeenTip, _ = s.GetChainState(ctx, "last_seen_tip")

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/admin/message/rebuild", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req messageRebuildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		if req.Height == 0 && req.MessageID <= 0 {
			http.Error(w, "height or message_id must be provided", http.StatusBadRequest)
			return
		}

		messageID := req.MessageID
		var message store.MessageRecord
		var err error
		if req.Height > 0 {
			message, err = s.GetLatestMessageByHeightAndType(ctx, req.Height, model.MessageTypeProve)
			if err != nil {
				http.Error(w, fmt.Sprintf("get message by height: %v", err), http.StatusInternalServerError)
				return
			}
			if message.ID == 0 {
				http.Error(w, fmt.Sprintf("prove message not found for height %d", req.Height), http.StatusNotFound)
				return
			}
			messageID = message.ID
		} else {
			message, err = s.GetMessage(ctx, messageID)
			if err != nil {
				http.Error(w, fmt.Sprintf("get message: %v", err), http.StatusNotFound)
				return
			}
		}

		allowRebuild := isRebuildableMessageStatus(message.Status)
		rebuildPayload := ""
		if !allowRebuild && message.Type == model.MessageTypeProve && message.Status == model.MessageStatusDone {
			newPayload, changed, err := rebuildDoneProvePayload(ctx, engine, message)
			if err != nil {
				http.Error(w, fmt.Sprintf("recompute prove hash: %v", err), http.StatusInternalServerError)
				return
			}
			allowRebuild = changed
			rebuildPayload = newPayload
		}
		if !allowRebuild {
			http.Error(w, fmt.Sprintf("message status %s cannot be rebuilt", message.Status), http.StatusConflict)
			return
		}

		var updated bool
		if rebuildPayload != "" {
			updated, err = s.ResetMessageToBuildingWithPayload(ctx, messageID, rebuildPayload)
		} else {
			updated, err = s.ResetMessageToBuilding(ctx, messageID)
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("reset message: %v", err), http.StatusInternalServerError)
			return
		}
		if !updated {
			http.Error(w, fmt.Sprintf("message %d not found", messageID), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(messageRebuildResponse{
			OK:        true,
			MessageID: messageID,
			Previous:  summarizeMessageState(message),
		})
	})
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && err != context.Canceled {
			log.Printf("health server shutdown error: %v", err)
		}
	}()

	go func() {
		log.Printf("health server listening addr=%s path=/healthz", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("health server error: %v", err)
		}
	}()

	return nil
}

func isRebuildableMessageStatus(status model.MessageStatus) bool {
	switch status {
	case model.MessageStatusBuilding, model.MessageStatusCommitSigned, model.MessageStatusCommitSent, model.MessageStatusCommitConfirmed:
		return true
	default:
		return false
	}
}

func summarizeMessageState(message store.MessageRecord) messageStateSummary {
	return messageStateSummary{
		Type:       message.Type,
		Status:     message.Status,
		Height:     message.RelatedHeight,
		IndexerID:  message.IndexerID,
		TxID:       message.TxID,
		RevealTxID: message.RevealTxID,
		RevealSent: message.RevealBroadcastAt != "",
		RevealDone: message.RevealConfirmHeight != 0,
	}
}

func rebuildDoneProvePayload(ctx context.Context, engine *service.Engine, message store.MessageRecord) (string, bool, error) {
	if engine == nil || engine.StateAPI == nil {
		return "", false, fmt.Errorf("engine state api is not configured")
	}

	indexerID, height, oldHash, err := parseProvePayload(message.PayloadText)
	if err != nil {
		return "", false, err
	}

	state, err := engine.StateAPI.GetHeightState(ctx, height)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(state.StateHash) == "" {
		return "", false, fmt.Errorf("state hash is empty for height %d", height)
	}
	blockHash := strings.TrimSpace(state.BlockHash)
	if blockHash == "" {
		if engine.RPC == nil {
			return "", false, fmt.Errorf("state api returned empty block hash for height %d and rpc client is not configured", height)
		}
		blockHash, err = engine.RPC.GetBlockHash(ctx, height)
		if err != nil {
			return "", false, fmt.Errorf("fallback get block hash at %d: %w", height, err)
		}
	}

	newHash, err := protocol.ComputeProveHash(indexerID, blockHash, state.StateHash)
	if err != nil {
		return "", false, err
	}
	changed := !strings.EqualFold(strings.TrimSpace(oldHash), strings.TrimSpace(newHash))
	if !changed {
		return "", false, nil
	}
	payload, err := protocol.EncodeProveText(model.ProveData{
		IndexerID:   indexerID,
		ProveHeight: height,
		ProveHash:   newHash,
	})
	if err != nil {
		return "", false, err
	}
	return string(payload), true, nil
}

func parseProvePayload(payload string) (string, uint64, string, error) {
	parts := strings.Split(strings.TrimSpace(payload), ",")
	if len(parts) < 6 {
		return "", 0, "", fmt.Errorf("invalid prove payload format")
	}
	if parts[2] != protocol.OpProve {
		return "", 0, "", fmt.Errorf("payload op %s is not %s", parts[2], protocol.OpProve)
	}
	height, err := strconv.ParseUint(strings.TrimSpace(parts[4]), 10, 64)
	if err != nil {
		return "", 0, "", fmt.Errorf("parse prove height: %w", err)
	}
	return strings.TrimSpace(parts[3]), height, strings.TrimSpace(parts[5]), nil
}

func runLoopOnce(ctx context.Context, engine *service.Engine) error {
	engine.Logf("loop_start")
	if err := engine.ProgressOnce(ctx); err != nil {
		return err
	}
	if err := ensureRegistered(ctx, engine); err != nil {
		return err
	}
	pendingProve, err := engine.Store.GetLatestMessageByType(
		ctx,
		model.MessageTypeProve,
		model.MessageStatusBuilding,
		model.MessageStatusCommitSigned,
		model.MessageStatusCommitSent,
		model.MessageStatusCommitConfirmed,
		model.MessageStatusRevealSent,
	)
	if err != nil {
		return err
	}
	if pendingProve.ID != 0 {
		engine.Logf("scan_skip_pending_prove message_id=%d status=%s height=%d", pendingProve.ID, pendingProve.Status, pendingProve.RelatedHeight)
		engine.Logf("loop_done")
		return nil
	}
	if err := engine.ScanOnce(ctx); err != nil {
		return err
	}
	if err := engine.ProgressOnce(ctx); err != nil {
		return err
	}
	engine.Logf("loop_done")
	return nil
}

func ensureRegistered(ctx context.Context, engine *service.Engine) error {
	indexerID, err := engine.Store.GetChainState(ctx, "indexer_id")
	if err != nil {
		return err
	}
	if strings.TrimSpace(indexerID) != "" {
		engine.Logf("register_check indexer_id=%s already_registered=true", indexerID)
		return nil
	}

	configuredIndexerID := strings.TrimSpace(engine.Config.Register.IndexerID)
	if configuredIndexerID != "" {
		if err := engine.Store.SetChainState(ctx, "indexer_id", configuredIndexerID); err != nil {
			return err
		}
		engine.Logf("register_check indexer_id=%s source=config skip_create=true", configuredIndexerID)
		return nil
	}

	latest, err := engine.Store.GetLatestMessageByType(ctx, model.MessageTypeRegister, model.MessageStatusBuilding, model.MessageStatusCommitSigned, model.MessageStatusCommitSent, model.MessageStatusCommitConfirmed, model.MessageStatusRevealSent, model.MessageStatusDone)
	if err != nil {
		return err
	}
	if latest.ID != 0 {
		engine.Logf("register_check existing_message_id=%d status=%s skip_create=true", latest.ID, latest.Status)
		return nil
	}

	cfg := engine.Config.Register
	if err := validateRegisterConfig(cfg); err != nil {
		return err
	}
	engine.Logf("register_check creating_initial_register=true name=%s reward_addr=%s", cfg.Name, cfg.RewardAddr)
	_, err = engine.CreateRegisterSubmission(ctx, model.RegisterData{
		IndexRatioBP:   cfg.IndexRatioBP,
		RewardAddrType: cfg.RewardAddrType,
		RewardAddr:     cfg.RewardAddr,
		Name:           cfg.Name,
	})
	return err
}

func runRegisterOnce(ctx context.Context, engine *service.Engine) error {
	cfg := engine.Config.Register
	if err := validateRegisterConfig(cfg); err != nil {
		return err
	}

	_, err := engine.RunRegister(ctx, cfg.IndexRatioBP, cfg.RewardAddrType, cfg.RewardAddr, cfg.Name)
	return err
}

func validateRegisterConfig(cfg config.RegisterConfig) error {
	if strings.TrimSpace(cfg.RewardAddr) == "" {
		return fmt.Errorf("register.reward_addr is required")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		return fmt.Errorf("register.name is required")
	}
	return nil
}
