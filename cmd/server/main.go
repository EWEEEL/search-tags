// Команда server отдаёт поисковые теги через JSON API и веб-страницу.
package main

import (
	"context"
	"errors"
	"flag"
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
	// В distroless-образе нет curl, поэтому healthcheck в docker-compose вызывает сам бинарник.
	healthcheck := flag.Bool("healthcheck", false, "проверить /readyz запущенного сервиса и выйти с кодом 0 или 1")
	flag.Parse()
	if *healthcheck {
		os.Exit(checkReady())
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

// checkReady обращается к /readyz сервиса, запущенного в этом же контейнере.
func checkReady() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/readyz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", response.StatusCode)
		return 1
	}
	return 0
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
