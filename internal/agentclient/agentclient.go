// Package agentclient is the agent's side of the control sidecar's agent API: what runs inside an
// agent container (the session holder, the hook command) uses it with the agent's own credentials.
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client calls the control sidecar as the agent whose credentials the environment holds.
type Client struct {
	URL, Agent, Token string
	HTTP              *http.Client
}

// FromEnv builds a client from EGZO_CONTROL_URL, EGZO_AGENT and EGZO_TOKEN.
func FromEnv() (*Client, error) {
	c := &Client{URL: os.Getenv("EGZO_CONTROL_URL"), Agent: os.Getenv("EGZO_AGENT"), Token: os.Getenv("EGZO_TOKEN")}
	if c.URL == "" || c.Agent == "" || c.Token == "" {
		return nil, fmt.Errorf("EGZO_CONTROL_URL, EGZO_AGENT and EGZO_TOKEN must be set (is this an egzo agent container?)")
	}
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c, nil
}

// Do sends one request and returns the response body. A non-2xx status is an error.
func (c *Client) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(c.Agent, c.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode/100 != 2 {
		return data, fmt.Errorf("control answered %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}
