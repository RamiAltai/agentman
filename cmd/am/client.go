package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client is the thin HTTP client the CLI verbs use to talk to `am serve`.
type Client struct {
	base  string
	agent string
	http  *http.Client
}

func NewClient() *Client {
	base := envOr("AGENTMAN_URL", "http://127.0.0.1:8787")
	return &Client{
		base:  strings.TrimRight(base, "/"),
		agent: resolveAgent(),
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

// do performs a request. A transport error (server down) is reported by
// returning status 0, which callers map to exit code 6.
func (c *Client) do(method, path string, body any) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		fail(1, "agentman: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.agent != "" {
		req.Header.Set("X-Agent", c.agent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// exitCodeFor maps an HTTP status (0 = transport error) to the CLI exit-code
// convention: 0 ok · 3 not found · 4 conflict · 5 validation · 6 server down ·
// 1 other. Single source for doOrFail and the bulk verbs.
func exitCodeFor(st int) int {
	switch {
	case st >= 200 && st < 300:
		return 0
	case st == 0:
		return 6
	case st == 404:
		return 3
	case st == 409:
		return 4
	case st == 400:
		return 5
	default:
		return 1
	}
}

// doOrFail returns the body on 2xx, otherwise prints a terse error and exits
// with the convention: 3 not found · 4 conflict · 5 validation · 6 server down.
func (c *Client) doOrFail(method, path string, body any) []byte {
	st, data := c.do(method, path, body)
	switch exitCodeFor(st) {
	case 0:
		return data
	case 6:
		fail(6, "agentman: cannot reach server at %s (is `am serve` running?)", c.base)
	case 3:
		fail(3, "%s", apiErr(data, "not found"))
	case 4:
		fail(4, "%s", apiErr(data, "conflict"))
	case 5:
		fail(5, "%s", apiErr(data, "invalid request"))
	default:
		fail(1, "%s", apiErr(data, "error "+strconv.Itoa(st)))
	}
	return nil
}

func apiErr(data []byte, def string) string {
	var e struct {
		Error    string `json:"error"`
		Assignee string `json:"assignee"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		if e.Assignee != "" {
			return e.Error + " by " + e.Assignee
		}
		return e.Error
	}
	return def
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
