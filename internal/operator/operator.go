// Package operator is the plumbing of the sidecars' operator APIs: an HTTP server on a unix socket
// inside the container, reached only through `engine exec`, and the tiny client that runs there.
package operator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Serve answers on a unix socket until SIGTERM or SIGINT.
func Serve(socket string, handler http.Handler) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return err
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func client(socket string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
}

// Request sends one request to the operator API at socket and returns the response body. A
// non-2xx status is an error that carries the body.
func Request(socket, method, path string, body io.Reader) ([]byte, error) {
	request, err := http.NewRequest(method, "http://sidecar"+path, body)
	if err != nil {
		return nil, err
	}
	response, err := client(socket).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return data, fmt.Errorf("%s %s: %s: %s", method, path, response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Healthcheck succeeds when GET /healthz answers 200; it is what the engine's healthcheck runs.
func Healthcheck(socket string) error {
	_, err := Request(socket, http.MethodGet, "/healthz", nil)
	return err
}

// Stream sends one request and copies the response body to out as it arrives, without a timeout:
// it is how a long-lived event stream reaches the caller.
func Stream(socket, method, path string, out io.Writer) error {
	streaming := client(socket)
	streaming.Timeout = 0
	request, err := http.NewRequest(method, "http://sidecar"+path, nil)
	if err != nil {
		return err
	}
	response, err := streaming.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		data, _ := io.ReadAll(response.Body)
		return fmt.Errorf("%s %s: %s: %s", method, path, response.Status, strings.TrimSpace(string(data)))
	}
	buffer := make([]byte, 4096)
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			out.Write(buffer[:n])
			if f, ok := out.(interface{ Sync() error }); ok {
				f.Sync()
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
