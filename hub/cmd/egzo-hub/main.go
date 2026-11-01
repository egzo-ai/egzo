// Command egzo-hub is the hub: the web UI over the egzo projects that link to it.
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
	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/publicurl"
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
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "hub.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	h := hub.New(db, log, hub.FeedOptions{})
	if err := h.RecoverTasks(context.Background()); err != nil {
		return err
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
	app, err := web.New(h, sso, web.Options{SSOName: cfg.OIDCName, LinkURL: cfg.LinkURL, OwnLink: cfg.LinkListen != ""}, urls, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go h.Run(ctx)

	failed := make(chan error, 2)
	serve := func(addr string, handler http.Handler, what string) *http.Server {
		server := newServer(addr, handler, readLimit)
		log.Info("egzo-hub listening", "for", what, "addr", addr, "version", version.Version)
		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				failed <- fmt.Errorf("listen: %w", err)
			}
		}()
		return server
	}
	servers := []*http.Server{serve(cfg.Listen, app.Handler(), "people")}
	if cfg.LinkListen != "" {
		servers = append(servers, serve(cfg.LinkListen, app.LinkHandler(), "links"))
	}
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, server := range servers {
		server.Shutdown(shutdown)
	}
	return nil
}

// readLimit is how long a request may take to arrive: every body the hub takes is small, so one that is still coming is somebody holding a
// connection open.
const readLimit = 30 * time.Second

// newServer limits what a connection may hold on to. A terminal or a link is unaffected: a WebSocket is hijacked, which clears these deadlines.
func newServer(addr string, handler http.Handler, read time.Duration) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: min(10*time.Second, read), ReadTimeout: read, IdleTimeout: 2 * time.Minute}
}

// printPassword shows the starter account's new password, once, on standard output: it is not kept anywhere else in clear.
func printPassword(password string) {
	fmt.Printf(`
==================================================================
  egzo hub: the starter account was given a password.

    username: %s
    password: %s

  It is shown this once. To choose it instead, or to reset it,
  set EGZO_ADMIN_PASSWORD and restart.
==================================================================

`, store.StarterUsername, password)
}
