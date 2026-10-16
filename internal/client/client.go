// Package client contains the cloud protocol client used by soseek.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/samekind/soseek/internal/core"
)

const maxResponse = 32 << 20

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type APIError struct {
	Status int
	Detail core.Error
}

func (e *APIError) Error() string { return e.Detail.Error() }

// ValidateURL requires TLS except for a literal loopback development address.
func ValidateURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("server must be an HTTPS URL without credentials, query or fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("HTTPS is required; HTTP is allowed only for localhost or a loopback IP")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func New(server, token string) (*Client, error) {
	base, err := ValidateURL(server)
	if err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("an access token is required")
	}
	return &Client{BaseURL: base, Token: token, HTTP: &http.Client{
		Timeout: 30 * time.Second,
		// Even same-origin redirects can change POST semantics. No redirect is
		// followed, so Authorization can never cross an origin boundary.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any, headers map[string]string) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "soseek/"+core.Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud request failed: %w", err)
	}
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) && resp.StatusCode != http.StatusNotModified {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		detail := core.Error{Code: "http_" + strconv.Itoa(resp.StatusCode), Message: http.StatusText(resp.StatusCode)}
		var wrapped struct {
			Error *core.Error `json:"error"`
		}
		if json.Unmarshal(b, &wrapped) == nil && wrapped.Error != nil {
			detail = *wrapped.Error
		} else {
			var direct core.Error
			if json.Unmarshal(b, &direct) == nil && direct.Code != "" {
				detail = direct
			}
		}
		detail.Message = strings.ReplaceAll(detail.Message, c.Token, "[redacted]")
		detail.Code = strings.ReplaceAll(detail.Code, c.Token, "[redacted]")
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			detail.RetryAfterSeconds = s
		}
		return nil, &APIError{Status: resp.StatusCode, Detail: detail}
	}
	return resp, nil
}

func (c *Client) Do(ctx context.Context, method, path string, body, out any, headers map[string]string) error {
	resp, err := c.request(ctx, method, path, body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return err
	}
	if len(b) > maxResponse {
		return errors.New("cloud response exceeds 32 MiB limit")
	}
	if out == nil {
		return nil
	}
	if err = json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("invalid cloud JSON response: %w", err)
	}
	return nil
}
func (c *Client) Plan(ctx context.Context, req core.Request) (core.Plan, error) {
	var out core.Plan
	err := c.Do(ctx, "POST", "/v1/plans", req, &out, nil)
	return out, err
}
func (c *Client) Submit(ctx context.Context, req core.Request, key string) (core.Run, error) {
	var out core.Run
	err := c.Do(ctx, "POST", "/v1/runs", req, &out, map[string]string{"Idempotency-Key": key})
	if err == nil && out.ID == "" {
		err = errors.New("cloud accepted request without a run_id; resume the saved pending request")
	}
	if err == nil {
		err = validateRun(out, "")
	}
	return out, err
}
func RunPath(id string) string { return "/v1/runs/" + url.PathEscape(id) }
func (c *Client) Run(ctx context.Context, id string) (core.Run, error) {
	var out core.Run
	err := c.Do(ctx, "GET", RunPath(id), nil, &out, nil)
	if err == nil {
		err = validateRun(out, id)
	}
	return out, err
}
func (c *Client) Result(ctx context.Context, id string) (core.Result, error) {
	var out core.Result
	err := c.Do(ctx, "GET", RunPath(id)+"/result", nil, &out, nil)
	return out, err
}
func (c *Client) Cancel(ctx context.Context, id string) (core.Run, error) {
	var out core.Run
	err := c.Do(ctx, "POST", RunPath(id)+"/cancel", nil, &out, nil)
	if err == nil {
		err = validateRun(out, id)
	}
	return out, err
}

func validateRun(run core.Run, expectedID string) error {
	if run.ID == "" || strings.ContainsAny(run.ID, "/\\?#") || (expectedID != "" && run.ID != expectedID) {
		return errors.New("cloud returned a missing or mismatched run_id")
	}
	if run.Status != "queued" && run.Status != "running" && !core.Terminal(run.Status) {
		return errors.New("cloud returned an unknown run status")
	}
	return nil
}

// Events reads one bounded SSE connection. The caller may reconnect using the
// last delivered ID, or retrieve the run snapshot; events are never required to
// obtain a result. Cancellation only closes this connection.
func (c *Client) Events(ctx context.Context, id string, after int64, emit func(core.Event) error) error {
	headers := map[string]string{"Accept": "text/event-stream"}
	if after > 0 {
		headers["Last-Event-ID"] = strconv.FormatInt(after, 10)
	}
	resp, err := c.request(ctx, "GET", RunPath(id)+"/events", nil, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 4096), 1<<20)
	event := core.Event{RunID: id}
	var data []string
	flush := func() error {
		if len(data) == 0 {
			event = core.Event{RunID: id}
			return nil
		}
		joined := strings.Join(data, "\n")
		if !json.Valid([]byte(joined)) {
			return errors.New("invalid SSE event JSON")
		}
		event.Data = json.RawMessage(joined)
		if err := emit(event); err != nil {
			return err
		}
		event = core.Event{RunID: id}
		data = nil
		return nil
	}
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		key, val, _ := strings.Cut(line, ":")
		val = strings.TrimPrefix(val, " ")
		switch key {
		case "id":
			event.ID, _ = strconv.ParseInt(val, 10, 64)
		case "event":
			event.Type = val
		case "data":
			data = append(data, val)
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	return flush()
}
