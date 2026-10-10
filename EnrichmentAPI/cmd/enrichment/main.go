package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"vani-context/enrichmentapi/internal/enrich"
	"vani-context/enrichmentapi/internal/httpapi"
	"vani-context/enrichmentapi/shared"
	contextconfig "vani-context/internal/config"
	"vani-context/internal/memory"
	contextstorage "vani-context/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vani-enrichment:", err)
		os.Exit(1)
	}
}

type rebuildCoordinator struct {
	store   *contextstorage.SQLiteStore
	builder *memory.Builder
}

func (r rebuildCoordinator) Rebuild(ctx context.Context, key contextstorage.UserKey) error {
	stale, err := r.store.NeedsRebuild(ctx, key)
	if err != nil || !stale {
		return err
	}
	return r.builder.Rebuild(ctx, key)
}

func run() error {
	cfg, err := contextconfig.Load()
	if err != nil {
		return err
	}
	address := strings.TrimSpace(os.Getenv("VANI_ENRICHMENT_HTTP_ADDR"))
	if address == "" {
		address = "127.0.0.1:18083"
	}
	timezoneName := strings.TrimSpace(os.Getenv("VANI_TIMEZONE"))
	if timezoneName == "" {
		return errors.New("VANI_TIMEZONE is required for Sarvam start_datetime conversion")
	}
	sourceTimezone, err := time.LoadLocation(timezoneName)
	if err != nil {
		return fmt.Errorf("load VANI_TIMEZONE: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := shared.OpenSQLite(ctx, cfg.DBPath, false)
	if err != nil {
		return err
	}
	defer db.Close()
	contextStore, err := contextstorage.Open(ctx, cfg.DBPath, cfg.BusyTimeout)
	if err != nil {
		return err
	}
	defer contextStore.Close()
	if err := contextStore.Migrate(ctx); err != nil {
		return err
	}
	referenceTime := time.Now
	if cfg.ReferenceTime != nil {
		fixed := *cfg.ReferenceTime
		referenceTime = func() time.Time { return fixed }
	}
	builder := memory.NewBuilder(contextStore, cfg.RecentHistoryDays, cfg.ContextTokenBudget, referenceTime)
	service := enrich.NewService(db, rebuildCoordinator{store: contextStore, builder: builder}, time.Now)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ready := func(ctx context.Context) error { var one int; return db.QueryRowContext(ctx, "SELECT 1").Scan(&one) }
	handler := httpapi.New(service, cfg.ServiceToken, logger, ready, sourceTimezone).Handler()
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go service.RunWorker(stopCtx, 2*time.Second, logger)
	errorsChannel := make(chan error, 1)
	go func() {
		logger.Info("VANI Enrichment API listening", "address", address, "database", cfg.DBPath, "source_timezone", timezoneName)
		errorsChannel <- server.ListenAndServe()
	}()
	select {
	case err := <-errorsChannel:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stopCtx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	}
	return nil
}
