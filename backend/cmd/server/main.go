package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aceitcenter.local/platform/internal/api"
	"aceitcenter.local/platform/internal/config"
	"aceitcenter.local/platform/internal/maintenance"
	postgresstore "aceitcenter.local/platform/internal/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	startupContext, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStartup()
	if err := db.PingContext(startupContext); err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}
	repository := postgresstore.New(db)
	if err := repository.Migrate(startupContext); err != nil {
		logger.Error("migrate database", "error", err)
		os.Exit(1)
	}
	retentionContext, cancelRetention := context.WithCancel(context.Background())
	defer cancelRetention()
	go maintenance.NewNetworkRetention(repository, logger, time.Now, 6*time.Hour, 90*24*time.Hour).Run(retentionContext)
	go maintenance.NewPairingRetention(repository, logger, time.Now, 24*time.Hour, 30*24*time.Hour).Run(retentionContext)

	handler := api.NewRouterWithOptions(repository, api.RouterOptions{SecureCookies: cfg.SecureCookies})
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("Ace IT Center API started", "address", cfg.ListenAddr)
		serverErrors <- server.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case signal := <-stop:
		logger.Info("shutdown requested", "signal", signal.String())
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped", "error", err)
		}
	}

	cancelRetention()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
