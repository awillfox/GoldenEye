package mediamtx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to mediamtx's HTTP API (v3).
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a Client targeting the given API base URL (e.g. http://127.0.0.1:9997).
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// PathConfig is the payload sent to add/update a path.
type PathConfig struct {
	Source                string `json:"source"`
	SourceOnDemand        bool   `json:"sourceOnDemand"`
	Record                bool   `json:"record"`
	RecordFormat          string `json:"recordFormat,omitempty"`
	RecordPath            string `json:"recordPath,omitempty"`
	RecordSegmentDuration string `json:"recordSegmentDuration,omitempty"`
}

// PathListItem is one entry in the paths list response.
type PathListItem struct {
	Name string `json:"name"`
}

// pathListResponse is the raw API list response.
type pathListResponse struct {
	Items []PathListItem `json:"items"`
}

// PathInfo holds runtime info about a live path (readers, tracks, etc.).
type PathInfo struct {
	Name   string  `json:"name"`
	Source *Source `json:"source"`
}

// Source holds the source info within a path, including detected tracks.
type Source struct {
	Type string `json:"type"`
}

// AddPath registers a new path in mediamtx.
func (c *Client) AddPath(ctx context.Context, name string, cfg PathConfig) error {
	return c.doJSON(ctx, http.MethodPost, "/v3/config/paths/add/"+name, cfg, nil)
}

// UpdatePath updates an existing path config in mediamtx.
func (c *Client) UpdatePath(ctx context.Context, name string, cfg PathConfig) error {
	return c.doJSON(ctx, http.MethodPatch, "/v3/config/paths/patch/"+name, cfg, nil)
}

// DeletePath removes a path from mediamtx.
func (c *Client) DeletePath(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, "/v3/config/paths/delete/"+name, nil, nil)
}

// ListConfigPaths returns all configured path names.
func (c *Client) ListConfigPaths(ctx context.Context) ([]PathListItem, error) {
	var resp pathListResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v3/config/paths/list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// Ping checks that mediamtx's API is reachable.
func (c *Client) Ping(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/v3/config/global/get", nil, nil)
}

// doJSON performs an HTTP request with optional JSON body and decodes the response.
func (c *Client) doJSON(ctx context.Context, method, path string, body, out interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mediamtx %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mediamtx %s %s → %d: %s", method, path, resp.StatusCode, msg)
	}

	if out != nil {
		if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode mediamtx response: %w", err)
		}
	}

	return nil
}
