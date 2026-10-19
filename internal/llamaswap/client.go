package llamaswap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RunningModel is one entry of the sidecar's /running answer.
type RunningModel struct {
	Model       string `json:"model"`
	State       string `json:"state"`
	Cmd         string `json:"cmd"`
	Proxy       string `json:"proxy"`
	TTL         int    `json:"ttl"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Client talks to a running sidecar over loopback.
type Client struct {
	Base string
	Key  string
	HTTP *http.Client
}

// NewClient returns a client for the sidecar on port.
func NewClient(port int, key string) *Client {
	return &Client{
		Base: "http://127.0.0.1:" + strconv.Itoa(port),
		Key:  key,
		HTTP: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Running lists what the sidecar has up right now.
func (c *Client) Running(ctx context.Context) ([]RunningModel, error) {
	var body struct {
		Running []RunningModel `json:"running"`
	}
	if err := c.get(ctx, "/running", &body); err != nil {
		return nil, err
	}
	return body.Running, nil
}

// Version answers the sidecar's own /api/version.
func (c *Client) Version(ctx context.Context) (string, error) {
	var body struct {
		Version string `json:"version"`
	}
	if err := c.get(ctx, "/api/version", &body); err != nil {
		return "", err
	}
	return body.Version, nil
}

// ProxyPort turns a model's proxy URL into its port.
func ProxyPort(proxy string) int {
	_, after, ok := strings.Cut(proxy, "://")
	if !ok {
		after = proxy
	}
	hostPort := strings.TrimSuffix(after, "/")
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		if port, err := strconv.Atoi(hostPort[i+1:]); err == nil {
			return port
		}
	}
	return 0
}
