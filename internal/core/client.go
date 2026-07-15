// Package core contains the reusable logic of ocm: ssh tunnel management,
// remote opencode server lifecycle and a minimal opencode HTTP API client.
// The future dashboard should import this package (or shell out to
// `ocm ... --json`).
package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// basicAuthUser is opencode's default HTTP basic auth username.
const basicAuthUser = "opencode"

// Client is a minimal opencode server API client.
type Client struct {
	BaseURL string
	// Password enables HTTP basic auth (opencode's OPENCODE_SERVER_PASSWORD
	// scheme, username "opencode"). Empty means no auth. Servers that do not
	// require auth simply ignore the header.
	Password string
	HTTP     *http.Client
}

// NewClient creates a client for an opencode server reachable at baseURL.
// password may be empty for unprotected servers.
func NewClient(baseURL, password string) *Client {
	return &Client{
		BaseURL:  baseURL,
		Password: password,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
	}
}

// newRequest builds a request with basic auth applied when configured.
func (c *Client) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.Password != "" {
		req.SetBasicAuth(basicAuthUser, c.Password)
	}
	return req, nil
}

func (c *Client) get(path string, out any) error {
	req, err := c.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, truncate(string(body), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// Health reports whether the server is healthy and its version.
func (c *Client) Health() (version string, ok bool) {
	var v struct {
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	req, err := c.newRequest(http.MethodGet, "/global/health", nil)
	if err != nil {
		return "", false
	}
	hc := &http.Client{Timeout: 2 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", false
	}
	return v.Version, v.Healthy
}

// Session is a subset of the opencode Session type.
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	Version   string `json:"version"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

// Sessions lists sessions on the server.
func (c *Client) Sessions() ([]Session, error) {
	var out []Session
	if err := c.get("/session", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SessionStatus returns per-session status. Values are decoded loosely so we
// stay compatible across opencode versions; the "type" field is extracted
// when present.
func (c *Client) SessionStatus() (map[string]string, error) {
	var raw map[string]map[string]any
	if err := c.get("/session/status", &raw); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for id, v := range raw {
		status := "unknown"
		for _, key := range []string{"type", "status", "state"} {
			if s, ok := v[key].(string); ok {
				status = s
				break
			}
		}
		out[id] = status
	}
	return out, nil
}

func (c *Client) post(path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := c.newRequest(http.MethodPost, path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("POST %s: %s: %s", path, resp.Status, truncate(string(respBody), 200))
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

// MessageEntry is one message with its parts, decoded loosely.
type MessageEntry struct {
	Info struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		Error any    `json:"error,omitempty"`
		Time  struct {
			Created int64 `json:"created"`
		} `json:"time"`
	} `json:"info"`
	Parts []MessagePart `json:"parts"`
}

// MessagePart is a subset of the opencode Part type.
type MessagePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Tool string `json:"tool,omitempty"`
	State *struct {
		Status string `json:"status,omitempty"`
		Title  string `json:"title,omitempty"`
	} `json:"state,omitempty"`
}

// Messages returns up to limit messages of a session.
func (c *Client) Messages(sessionID string, limit int) ([]MessageEntry, error) {
	path := "/session/" + sessionID + "/message"
	if limit > 0 {
		path += fmt.Sprintf("?limit=%d", limit)
	}
	var out []MessageEntry
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PromptAsync sends a text prompt to a session without waiting for the reply.
func (c *Client) PromptAsync(sessionID, text string) error {
	body := map[string]any{
		"parts": []map[string]any{{"type": "text", "text": text}},
	}
	return c.post("/session/"+sessionID+"/prompt_async", body, nil)
}

// RespondPermission answers a pending permission request.
// response is one of "once", "always", "reject".
func (c *Client) RespondPermission(sessionID, permissionID, response string) error {
	return c.post("/session/"+sessionID+"/permissions/"+permissionID,
		map[string]any{"response": response}, nil)
}

// Abort aborts a running session.
func (c *Client) Abort(sessionID string) error {
	return c.post("/session/"+sessionID+"/abort", nil, nil)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
