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

	if err := startHealthServer(ctx, cfg.Runtime.HealthAddr, s, mode); err != nil {
		return err
	}

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

func startHealthServer(ctx context.Context, addr string, s *store.Store, mode string) error {
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

func runLoopOnce(ctx context.Context, engine *service.Engine) error {
	engine.Logf("loop_start")
	if err := engine.ProgressOnce(ctx); err != nil {
		return err
	}
	if err := ensureRegistered(ctx, engine); err != nil {
		return err
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
