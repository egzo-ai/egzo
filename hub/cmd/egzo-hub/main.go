// Command egzo-hub is the hub: the web UI over the egzo projects of one or more container engines.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/auth"
	"github.com/egzo-ai/egzo/hub/internal/config"
	"github.com/egzo-ai/egzo/hub/internal/engines"
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/publicurl"
	"github.com/egzo-ai/egzo/hub/internal/secretbox"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/hub/internal/web"
	"github.com/egzo-ai/egzo/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("egzo-hub", version.Version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("egzo-hub stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Parse(os.Getenv)
	if err != nil {
		return err
	}
	box, err := secretbox.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "hub.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	registry := engines.NewRegistry(db, box, engines.DockerConnector)
	defer registry.Close()
	h := hub.New(db, registry, log)
	if cfg.LocalEngine != "" {
		if err := h.SeedEngine(context.Background(), "local", cfg.LocalEngine); err != nil {
			return err
		}
	}
	urls, err := publicurl.New(cfg.PublicURL, cfg.TrustedProxy)
	if err != nil {
		return err
	}
	generated, err := h.SetupSignIn(context.Background(), hub.SignInOptions{LocalLogin: cfg.LocalLogin, AdminPassword: cfg.AdminPassword, Issuer: cfg.OIDCIssuer})
	if err != nil {
		return err
	}
	if generated != "" {
		printPassword(generated)
	}
	var sso web.Authenticator
	if cfg.OIDCIssuer != "" {
		sso = auth.New(auth.Config{Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCSecret, Scopes: cfg.OIDCScopes})
		redirect := "<the address people reach the hub at>/auth/callback"
		if cfg.PublicURL != "" {
			redirect = strings.TrimSuffix(cfg.PublicURL, "/") + "/auth/callback"
		}
		log.Info("single sign-on", "issuer", cfg.OIDCIssuer, "redirect", redirect)
	}
	app, err := web.New(h, sso, web.Options{SSOName: cfg.OIDCName}, urls, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go h.Run(ctx, hub.FeedOptions{Fallback: cfg.ScanInterval})

	server := &http.Server{Addr: cfg.Listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Info("egzo-hub listening", "addr", cfg.Listen, "version", version.Version)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}

// printPassword shows the starter account's new password, once, on standard output: it is not kept anywhere else in clear.
func printPassword(password string) {
	fmt.Printf(`
==================================================================
  egzo hub: the starter account was given a password.

    username: admin
    password: %s

  It is shown this once. To choose it instead, or to reset it,
  set EGZO_ADMIN_PASSWORD and restart.
==================================================================

`, password)
}
