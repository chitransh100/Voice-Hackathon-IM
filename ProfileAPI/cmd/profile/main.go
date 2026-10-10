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

	"vani-context/enrichmentapi/shared"
	contextconfig "vani-context/internal/config"
	"vani-context/profileapi/internal/httpapi"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vani-profile:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := contextconfig.Load()
	if err != nil {
		return err
	}
	address := strings.TrimSpace(os.Getenv("VANI_PROFILE_HTTP_ADDR"))
	if address == "" {
		address = "127.0.0.1:18082"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := shared.OpenSQLite(ctx, cfg.DBPath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	server := &http.Server{Addr: address, Handler: httpapi.New(db, cfg.ServiceToken, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 8 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorsChannel := make(chan error, 1)
	go func() {
		logger.Info("VANI Profile API listening", "address", address, "database", cfg.DBPath)
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
