// Команда server отдаёт поисковые теги через JSON API и веб-страницу.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EWEEEL/search-tags/internal/config"
	"github.com/EWEEEL/search-tags/internal/storage"
	"github.com/EWEEEL/search-tags/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	store, err := storage.Open(storage.Options{
		Addr:        cfg.ClickHouseAddr,
		Database:    cfg.ClickHouseDatabase,
		Username:    cfg.ClickHouseUser,
		Password:    cfg.ClickHousePassword,
		DialTimeout: cfg.QueryTimeout,
	})
	if err != nil {
		return err
	}
	defer store.Close()

	pingCtx, cancelPing := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPing()
	if err := store.Ping(pingCtx); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              net.JoinHostPort("", cfg.Port),
		Handler:           web.NewServer(store, cfg.QueryTimeout, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.QueryTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stop, cancelStop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelStop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server started", "addr", server.Addr, "clickhouse", cfg.ClickHouseAddr)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-stop.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
