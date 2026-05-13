package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"fractal-proof-publisher/internal/app"
	"fractal-proof-publisher/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	mode := "run"
	if len(os.Args) > 1 {
		mode = strings.TrimSpace(os.Args[1])
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := app.RunMode(ctx, cfg, mode); err != nil {
		log.Fatal(err)
	}
}
