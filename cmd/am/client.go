package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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
	scope string // "cat" or "cat/proj"; empty = unscoped
	token string // bearer token; when set it carries the scope and the header is dropped
	http  *http.Client
}

func NewClient() *Client {
	base := serverURL()
	return &Client{
		base:  strings.TrimRight(base, "/"),
		agent: resolveAgent(),
		scope: resolveScope(),
		token: resolveToken(),
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

// newRequest builds a request carrying the identity and credential headers.
// It is the single place that decides them, shared by do() and the streaming
// waiter (wait.go), so the two cannot drift apart.
func (c *Client) newRequest(ctx context.Context, method, path string, body any) *http.Request {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		fail(1, "agentman: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.agent != "" {
		req.Header.Set("X-Agent", c.agent)
	}
	// A token carries the scope server-side, so when one is present we send it
	// as Authorization: Bearer and STOP sending X-Agent-Scope — the server
	// ignores the header anyway (scopeOf gives the token precedence), and not
	// sending it keeps the wire honest about where the scope comes from.
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else if c.scope != "" {
		req.Header.Set("X-Agent-Scope", c.scope)
	}
	return req
}

// do performs a request. Two failures are terminal for every caller and so
// are decided here, once: a transport error exits 6 (transportFail), and a 403
// from the server's Host/Origin guards exits 1 — it means AGENTMAN_URL or a
// proxy is misconfigured, not that the task is out of scope (exit 8 is kept
// for the out_of_scope 403 only).
func (c *Client) do(method, path string, body any) (int, []byte) {
	resp, err := c.http.Do(c.newRequest(context.Background(), method, path, body))
	if err != nil {
		transportFail(c.base, method, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		switch e := apiErr(data, ""); e {
		case "forbidden_host", "cross_origin":
			fail(1, "agentman: server at %s refused the request (%s) — point AGENTMAN_URL at http://127.0.0.1:<port> directly, not through a hostname alias or proxy", c.base, e)
		}
	}
	return resp.StatusCode, data
}

// transportFail exits 6 for a request that got no HTTP response, keeping the
// underlying error. A failed dial never reached the server; any other error
// (timeout, reset) may have happened after the server received a mutation,
// so for non-GET requests the message says the outcome is unknown.
func transportFail(base, method string, err error) {
	var op *net.OpError
	switch {
	case errors.As(err, &op) && op.Op == "dial":
		fail(6, "agentman: cannot reach server at %s (is `am serve` running?): %v", base, err)
	case method == http.MethodGet:
		fail(6, "agentman: request to %s failed: %v", base, err)
	default:
		fail(6, "agentman: %s to %s failed after it was sent (%v) — the outcome is unknown; check the task's state before retrying", method, base, err)
	}
}

// exitCodeFor maps an HTTP status to the CLI exit-code convention: 0 ok ·
// 3 not found · 4 conflict · 5 validation · 8 out of scope · 9 bad token ·
// 1 other (6 server down never gets here: do() exits on a transport error).
// Single source for doOrFail and the bulk verbs. 9 is distinct from 8 on purpose: a bad credential must hard-fail,
// not be swallowed as a per-id scope-skip in a bulk loop.
func exitCodeFor(st int) int {
	switch {
	case st >= 200 && st < 300:
		return 0
	case st == 401:
		return 9
	case st == 404:
		return 3
	case st == 409:
		return 4
	case st == 400:
		return 5
	case st == 403:
		return 8
	default:
		return 1
	}
}

// doOrFail returns the body on 2xx, otherwise prints a terse error and exits
// with the convention: 3 not found · 4 conflict · 5 validation · 8 out of
// scope (6 server down is decided in do()).
func (c *Client) doOrFail(method, path string, body any) []byte {
	st, data := c.do(method, path, body)
	return c.orFail(st, data)
}

// orFail is doOrFail's status mapping, for callers that inspect the status
// before falling back to the convention.
func (c *Client) orFail(st int, data []byte) []byte {
	switch exitCodeFor(st) {
	case 0:
		return data
	case 3:
		fail(3, "%s", apiErr(data, "not found"))
	case 4:
		fail(4, "%s", apiErr(data, "conflict"))
	case 5:
		fail(5, "%s", apiErr(data, "invalid request"))
	case 8:
		fail(8, "%s", apiErr(data, "out of scope"))
	case 9:
		fail(9, "%s", apiErr(data, "invalid or revoked token"))
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
