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

	"vani-lookup/internal/config"
	"vani-lookup/internal/httpapi"
	"vani-lookup/internal/search"
	"vani-lookup/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vani-lookup:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repository, err := store.OpenReadOnly(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer repository.Close()
	referenceTime := time.Now
	if cfg.ReferenceTime != nil {
		fixed := *cfg.ReferenceTime
		referenceTime = func() time.Time { return fixed }
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	engine := search.New(repository, cfg.CandidateLimit)
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: httpapi.New(repository, engine, cfg.ServiceToken, logger, referenceTime).Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 7 * time.Second, WriteTimeout: 10 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorsChannel := make(chan error, 1)
	go func() {
		logger.Info("VANI Lookup API listening", "address", cfg.HTTPAddr, "database", cfg.DBPath)
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
