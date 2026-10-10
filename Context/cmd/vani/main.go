package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vani-context/internal/config"
	"vani-context/internal/httpapi"
	"vani-context/internal/memory"
	"vani-context/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vani:", err)
		os.Exit(1)
	}
}

func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command != "serve" && command != "migrate" && command != "seed-synthetic" && command != "rebuild" {
		return fmt.Errorf("unknown command %q (use serve, migrate, seed-synthetic, or rebuild)", command)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, cfg.DBPath, cfg.BusyTimeout)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	referenceTime := time.Now
	if cfg.ReferenceTime != nil {
		fixed := *cfg.ReferenceTime
		referenceTime = func() time.Time { return fixed }
	}
	builder := memory.NewBuilder(store, cfg.RecentHistoryDays, cfg.ContextTokenBudget, referenceTime)
	switch command {
	case "migrate":
		fmt.Println("database schema is at version 1")
		return nil
	case "seed-synthetic":
		if err := store.SeedSynthetic(ctx); err != nil {
			return err
		}
		if err := builder.RebuildAll(ctx); err != nil {
			return err
		}
		fmt.Println("inserted synthetic source data and rebuilt snapshots: SYNTH-SELLER-001 seller/buyer and SYNTH-SELLER-002 seller")
		return nil
	case "rebuild":
		if err := builder.RebuildAll(ctx); err != nil {
			return err
		}
		fmt.Println("rebuilt all current_context snapshots")
		return nil
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if rebuilt, err := builder.RebuildStale(ctx); err != nil {
		return fmt.Errorf("initial snapshot backfill: %w", err)
	} else if rebuilt > 0 {
		logger.Info("backfilled stale contexts before serving", "count", rebuilt)
	}
	api := httpapi.New(store, cfg.ServiceToken, logger)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go memory.NewWorker(builder, cfg.WorkerInterval, logger).Run(stopCtx)
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("VANI Context API listening", "address", cfg.HTTPAddr, "database", cfg.DBPath)
		serverErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stopCtx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}
