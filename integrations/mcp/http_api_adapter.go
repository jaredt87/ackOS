package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const ToolHTTPControl = "ackos_control"

const maxAPIResponseBytes = 1 << 20

// HTTPAPIConfig configures the production MCP adapter. The adapter only calls
// ackos-api; all observation, authorization, execution, verification, and
// commitment remain behind that service's authenticated HTTP contract.
type HTTPAPIConfig struct {
	BaseURL     string
	BearerToken string
	Timeout     time.Duration
	HTTPClient  *http.Client
}

// HTTPControlRequest mirrors the enforceable ackos-api replacement-file
// contract. ContentBase64 is canonical padded Base64 for the exact file bytes.
type HTTPControlRequest struct {
	RepositoryID  string `json:"repository_id" jsonschema:"allowlisted repository ID configured on ackos-api"`
	Branch        string `json:"branch" jsonschema:"existing branch name in the configured repository"`
	Path          string `json:"path" jsonschema:"path of an existing tracked regular file"`
	Mode          string `json:"mode" jsonschema:"replacement file mode: 100644 or 100755"`
	ContentBase64 string `json:"content_base64" jsonschema:"canonical padded Base64 encoding of the complete replacement file bytes"`
}

type HTTPControlResponse struct {
	ExecutionID string    `json:"execution_id"`
	Verified    bool      `json:"verified"`
	Committed   bool      `json:"committed"`
	CompletedAt time.Time `json:"completed_at"`
	State       string    `json:"state"`
}

type observationHTTPRequest struct {
	RepositoryID string `json:"repository_id"`
	Branch       string `json:"branch"`
	Path         string `json:"path"`
}

type observationHTTPResponse struct {
	RepositoryID     string    `json:"repository_id"`
	Branch           string    `json:"branch"`
	Path             string    `json:"path"`
	ObservationToken string    `json:"observation_token"`
	ObservedAt       time.Time `json:"observed_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type changeHTTPRequest struct {
	ObservationToken string `json:"observation_token"`
	Mode             string `json:"mode"`
	ContentBase64    string `json:"content_base64"`
}

type apiErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type HTTPAPIAdapter struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPAPIAdapter(cfg HTTPAPIConfig) (*HTTPAPIAdapter, error) {
	rawURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if rawURL == "" {
		return nil, errors.New("ackos-api base URL is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
		return nil, errors.New("ackos-api URL must be an HTTPS origin (HTTP is permitted only for loopback)")
	}
	if strings.TrimSpace(cfg.BearerToken) == "" {
		return nil, errors.New("ackos-api bearer token is required")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("ackos-api timeout must be positive")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	} else if client.Timeout == 0 || client.Timeout > cfg.Timeout {
		// Never let an injected client weaken the configured total deadline.
		clone := *client
		clone.Timeout = cfg.Timeout
		client = &clone
	}
	return &HTTPAPIAdapter{baseURL: rawURL, token: cfg.BearerToken, client: client}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// MCPServer exposes the production-backed ackos_control tool. It intentionally
// does not construct a kernel runtime, executor, verifier, or Git provider.
func (a *HTTPAPIAdapter) MCPServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "ackOS HTTP adapter", Version: "0.3.0"}, nil)
	mcpsdk.AddTool[HTTPControlRequest, any](server, &mcpsdk.Tool{
		Name:        ToolHTTPControl,
		Description: "Replace an existing tracked file through authenticated ackos-api. The adapter first obtains a fresh provider observation and passes its opaque token to ackos-api; execution and verification remain exclusively in ackos-api.",
	}, a.controlTool)
	return server
}

func (a *HTTPAPIAdapter) controlTool(ctx context.Context, _ *mcpsdk.CallToolRequest, in HTTPControlRequest) (*mcpsdk.CallToolResult, any, error) {
	if strings.TrimSpace(in.RepositoryID) == "" || strings.TrimSpace(in.Branch) == "" || strings.TrimSpace(in.Path) == "" {
		return toolError("repository_id, branch, and path are required"), nil, nil
	}
	if in.Mode != "100644" && in.Mode != "100755" {
		return toolError("mode must be 100644 or 100755"), nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(in.ContentBase64)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != in.ContentBase64 {
		return toolError("content_base64 must be canonical padded Base64"), nil, nil
	}

	observationBody, err := json.Marshal(observationHTTPRequest{RepositoryID: in.RepositoryID, Branch: in.Branch, Path: in.Path})
	if err != nil {
		return toolError("could not encode observation request"), nil, nil
	}
	var observation observationHTTPResponse
	if err := a.postJSON(ctx, "/v1/observations", observationBody, &observation); err != nil {
		return toolError("ackos-api observation failed: " + err.Error()), nil, nil
	}
	if observation.ObservationToken == "" ||
		observation.RepositoryID != in.RepositoryID ||
		observation.Branch != in.Branch ||
		observation.Path != in.Path {
		return toolError("ackos-api returned an invalid or mismatched observation; no change was submitted"), nil, nil
	}

	changeBody, err := json.Marshal(changeHTTPRequest{
		ObservationToken: observation.ObservationToken,
		Mode:             in.Mode,
		ContentBase64:    in.ContentBase64,
	})
	if err != nil {
		return toolError("could not encode change request"), nil, nil
	}
	var result HTTPControlResponse
	if err := a.postJSON(ctx, "/v1/changes", changeBody, &result); err != nil {
		return toolError("ackos-api change failed or has an ambiguous outcome; do not retry without obtaining a new observation: " + err.Error()), nil, nil
	}
	if !result.Verified || !result.Committed || result.ExecutionID == "" {
		return toolError("ackos-api did not confirm both independent verification and commitment; outcome is not accepted as success"), nil, nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return toolError("could not encode ackos-api result"), nil, nil
	}
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(encoded)}}, StructuredContent: result}, result, nil
}

func toolError(message string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: message}}, IsError: true}
}

func (a *HTTPAPIAdapter) postJSON(ctx context.Context, path string, body []byte, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxAPIResponseBytes {
		return errors.New("ackos-api response exceeded size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope apiErrorEnvelope
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Code != "" {
			return fmt.Errorf("HTTP %d (%s): %s", resp.StatusCode, envelope.Error.Code, envelope.Error.Message)
		}
		return fmt.Errorf("HTTP %d from ackos-api", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected successful HTTP status %d from ackos-api", resp.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("invalid ackos-api response: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("ackos-api returned trailing JSON data")
	}
	return nil
}
