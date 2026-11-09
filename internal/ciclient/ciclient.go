// Package ciclient is the HTTP client behind "sandpost wait" and "sandpost
// extract": it calls the project scoped wait and extract routes with a project
// API token. It validates parameters locally with the server's limits so a
// typo fails fast, and never puts the token in an error message.
package ciclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Server limits, see docs/openapi.yaml (GET /projects/{pid}/wait).
const (
	MaxCount     = 50
	MaxTimeout   = 120 * time.Second
	MaxRunIDSize = 100

	// clientMargin is added to the poll timeout for the HTTP deadline, so the
	// server's own answer (timed_out) arrives before the client gives up.
	clientMargin = 15 * time.Second
	maxBody      = 4 << 20
	maxErrBody   = 4 << 10
	apiPrefix    = "_/api/v1/projects"
)

// ErrInvalid marks a bad parameter or configuration, found before any request.
var ErrInvalid = errors.New("invalid argument")

// APIError is a non 2xx answer of the server.
type APIError struct {
	Status int
	// Message is the server's "error" text, empty when the body was not JSON.
	Message string
}

func (e *APIError) Error() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		return "authentication failed (401): check SANDPOST_TOKEN, it must be a valid project token"
	case e.Status == http.StatusForbidden:
		return "not allowed (403): the token needs the messages.read capability"
	case e.Status == http.StatusNotFound:
		return "project or message not found (404): check SANDPOST_PROJECT and that the token belongs to that project"
	case e.Message != "":
		return fmt.Sprintf("server answered %d: %s", e.Status, e.Message)
	default:
		return fmt.Sprintf("server answered %d", e.Status)
	}
}

// Summary is the part of a message summary the CLI uses.
type Summary struct {
	ID         int64     `json:"id"`
	ReceivedAt time.Time `json:"received_at"`
	From       string    `json:"from"`
	To         []string  `json:"to"`
	Subject    string    `json:"subject"`
}

// WaitResult is the answer of the wait route.
type WaitResult struct {
	Messages []Summary `json:"messages"`
	TimedOut bool      `json:"timed_out"`
}

// Code is a one-time code found in a message.
type Code struct {
	Value string `json:"value"`
	Kind  string `json:"kind"`
}

// Extraction is the answer of the extract route.
type Extraction struct {
	MessageID int64    `json:"message_id"`
	Links     []string `json:"links"`
	Codes     []Code   `json:"codes"`
}

// WaitParams are the query parameters of the wait route. Count is required
// (1 to 50), the other zero values are omitted from the request.
type WaitParams struct {
	Query   string
	Run     string
	AfterID int64
	Count   int
	Timeout time.Duration
}

// ExtractParams are the optional filters of the extract route.
type ExtractParams struct {
	LinkContains string
	CodePattern  string
}

// Client talks to one project of one server.
type Client struct {
	base    *url.URL
	project int64
	token   string
	// HTTP can be replaced in tests. Its Timeout must stay zero, deadlines are per call.
	HTTP *http.Client
}

// New validates the server address, the numeric project id and the token.
func New(serverURL, project, token string) (*Client, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("%w: server URL is required (--server or SANDPOST_URL)", ErrInvalid)
	}
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: server URL must be an http or https address", ErrInvalid)
	}
	if project == "" {
		return nil, fmt.Errorf("%w: project id is required (--project or SANDPOST_PROJECT)", ErrInvalid)
	}
	pid, err := strconv.ParseInt(project, 10, 64)
	if err != nil || pid < 1 {
		return nil, fmt.Errorf("%w: project must be the numeric project id", ErrInvalid)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: API token is required (SANDPOST_TOKEN)", ErrInvalid)
	}
	u.RawQuery, u.Fragment = "", ""
	return &Client{base: u, project: pid, token: token, HTTP: &http.Client{}}, nil
}

// ValidateRunID applies the server's run id rule: 1 to 100 bytes of valid
// UTF-8, no control characters, no surrounding spaces.
func ValidateRunID(id string) error {
	switch {
	case id == "" || len(id) > MaxRunIDSize:
		return fmt.Errorf("%w: run id must be 1 to %d bytes", ErrInvalid, MaxRunIDSize)
	case !utf8.ValidString(id):
		return fmt.Errorf("%w: run id must be valid UTF-8", ErrInvalid)
	case strings.TrimSpace(id) != id:
		return fmt.Errorf("%w: run id must not start or end with spaces", ErrInvalid)
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: run id must not contain control characters", ErrInvalid)
		}
	}
	return nil
}

// Validate checks the parameters against the server limits.
func (p WaitParams) Validate() error {
	switch {
	case p.Count < 1 || p.Count > MaxCount:
		return fmt.Errorf("%w: count must be between 1 and %d", ErrInvalid, MaxCount)
	case p.Timeout < 0 || p.Timeout > MaxTimeout:
		return fmt.Errorf("%w: timeout must be between 0 and %s", ErrInvalid, MaxTimeout)
	case p.AfterID < 0:
		return fmt.Errorf("%w: after-id must not be negative", ErrInvalid)
	}
	if p.Run != "" {
		return ValidateRunID(p.Run)
	}
	return nil
}

// Wait long polls the project until Count messages match or Timeout passed.
// It returns the decoded answer and the raw JSON body. A timed out wait is not
// an error: check WaitResult.TimedOut.
func (c *Client) Wait(ctx context.Context, p WaitParams) (WaitResult, []byte, error) {
	var res WaitResult
	if err := p.Validate(); err != nil {
		return res, nil, err
	}
	q := url.Values{}
	if p.Query != "" {
		q.Set("q", p.Query)
	}
	if p.Run != "" {
		q.Set("run", p.Run)
	}
	if p.AfterID > 0 {
		q.Set("after_id", strconv.FormatInt(p.AfterID, 10))
	}
	q.Set("count", strconv.Itoa(p.Count))
	// Round up so a sub second timeout still waits.
	q.Set("timeout", strconv.Itoa(int((p.Timeout+time.Second-1)/time.Second)))
	ctx, cancel := context.WithTimeout(ctx, p.Timeout+clientMargin)
	defer cancel()
	raw, err := c.get(ctx, "wait", q)
	if err != nil {
		return res, nil, err
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return res, nil, fmt.Errorf("decode wait answer: %w", err)
	}
	return res, raw, nil
}

// Extract returns the links and one-time codes of a message.
func (c *Client) Extract(ctx context.Context, messageID int64, p ExtractParams) (Extraction, []byte, error) {
	var ex Extraction
	if messageID < 1 {
		return ex, nil, fmt.Errorf("%w: message id must be positive", ErrInvalid)
	}
	q := url.Values{}
	if p.LinkContains != "" {
		q.Set("link", p.LinkContains)
	}
	if p.CodePattern != "" {
		q.Set("code_pattern", p.CodePattern)
	}
	ctx, cancel := context.WithTimeout(ctx, clientMargin)
	defer cancel()
	raw, err := c.get(ctx, "messages/"+strconv.FormatInt(messageID, 10)+"/extract", q)
	if err != nil {
		return ex, nil, err
	}
	if err := json.Unmarshal(raw, &ex); err != nil {
		return ex, nil, fmt.Errorf("decode extract answer: %w", err)
	}
	return ex, raw, nil
}

func (c *Client) get(ctx context.Context, route string, q url.Values) ([]byte, error) {
	u := c.base.JoinPath(apiPrefix, strconv.FormatInt(c.project, 10), route)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req) //nolint:gosec // G704: the address is the operator's own --server flag
	if err != nil {
		// url.Error carries the URL (never the token, it is a header).
		return nil, fmt.Errorf("request %s: %w", route, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &body)
		return nil, &APIError{Status: resp.StatusCode, Message: body.Error}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read answer: %w", err)
	}
	return raw, nil
}
