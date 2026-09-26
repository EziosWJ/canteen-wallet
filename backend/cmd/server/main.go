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

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/config"
	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/httpapi"
	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
	"github.com/EziosWJ/canteen-wallet/backend/internal/paymenttokens"
	"github.com/EziosWJ/canteen-wallet/backend/internal/recharges"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(os.Args) > 1 {
		if len(os.Args) != 3 || os.Args[1] != "create-admin" {
			return fmt.Errorf("usage: canteen-server [create-admin <username>]")
		}
		return createAdmin(cfg.DatabasePath, os.Args[2])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	publicListener, err := net.Listen("tcp", cfg.PublicAddr)
	if err != nil {
		return err
	}
	defer publicListener.Close()
	internalListener, err := net.Listen("tcp", cfg.InternalAddr)
	if err != nil {
		return err
	}
	defer internalListener.Close()

	adminService := adminauth.New(db, nil) // Password-only until the live-pilot MFA task supplies a verifier.
	employeeService := employees.New(db)
	location, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		return err
	}
	mealService := meals.New(db, location)
	tokenService := paymenttokens.New(db)
	rechargeService := recharges.New(db)
	publicServer := &http.Server{Handler: httpapi.Public(db, adminService, employeeService, mealService, tokenService, rechargeService), ReadHeaderTimeout: 5 * time.Second}
	internalServer := &http.Server{Handler: httpapi.Internal(db), ReadHeaderTimeout: 5 * time.Second}
	type serveResult struct {
		name string
		err  error
	}
	errorsCh := make(chan serveResult, 2)
	go func() { errorsCh <- serveResult{"public", publicServer.Serve(publicListener)} }()
	go func() { errorsCh <- serveResult{"internal", internalServer.Serve(internalListener)} }()
	logger.Info("server started", "public_addr", publicListener.Addr().String(), "internal_addr", internalListener.Addr().String())

	var serveErr error
	select {
	case <-ctx.Done():
	case result := <-errorsCh:
		if !errors.Is(result.err, http.ErrServerClosed) {
			stop()
			serveErr = fmt.Errorf("%s listener: %w", result.name, result.err)
		} else {
			serveErr = fmt.Errorf("%s listener stopped unexpectedly", result.name)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErrors := make(chan error, 2)
	go func() { shutdownErrors <- publicServer.Shutdown(shutdownCtx) }()
	go func() { shutdownErrors <- internalServer.Shutdown(shutdownCtx) }()
	shutdownErr := errors.Join(<-shutdownErrors, <-shutdownErrors)
	if err := errors.Join(serveErr, shutdownErr); err != nil {
		return err
	}
	logger.Info("server stopped")
	return nil
}
