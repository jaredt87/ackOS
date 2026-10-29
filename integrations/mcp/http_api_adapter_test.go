package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callHTTPControlTool(t *testing.T, adapter *HTTPAPIAdapter) *mcpsdk.CallToolResult {
	t.Helper()
	server := adapter.MCPServer()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ackos-http-adapter-test", Version: "test"}, nil)
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: ToolHTTPControl,
		Arguments: map[string]any{
			"repository_id":  "disposable",
			"branch":         "main",
			"path":           "README.md",
			"mode":           "100644",
			"content_base64": "bmV3IGNvbnRlbnQK",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newTestHTTPAdapter(t *testing.T, server *httptest.Server, timeout time.Duration) *HTTPAPIAdapter {
	t.Helper()
	adapter, err := NewHTTPAPIAdapter(HTTPAPIConfig{
		BaseURL: server.URL, BearerToken: "test-bearer-token", Timeout: timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestHTTPAPIAdapterMCPToolObservesThenChangesWithOpaqueToken(t *testing.T) {
	const token = "api-issued-opaque-token-not-a-fingerprint"
	var observations, changes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-bearer-token" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		switch r.URL.Path {
		case "/v1/observations":
			observations.Add(1)
			var in observationHTTPRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode observation: %v", err)
			}
			if in != (observationHTTPRequest{RepositoryID: "disposable", Branch: "main", Path: "README.md"}) {
				t.Errorf("observation request = %+v", in)
			}
			_ = json.NewEncoder(w).Encode(observationHTTPResponse{
				RepositoryID: in.RepositoryID, Branch: in.Branch, Path: in.Path,
				ObservationToken: token, ExpiresAt: time.Now().Add(time.Minute),
			})
		case "/v1/changes":
			changes.Add(1)
			var in changeHTTPRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode change: %v", err)
			}
			if in.ObservationToken != token {
				t.Errorf("token = %q, want exact API token", in.ObservationToken)
			}
			if in.Mode != "100644" || in.ContentBase64 != "bmV3IGNvbnRlbnQK" {
				t.Errorf("change request = %+v", in)
			}
			_ = json.NewEncoder(w).Encode(HTTPControlResponse{
				ExecutionID: "execution-123", Verified: true, Committed: true,
				CompletedAt: time.Now().UTC(), State: "committed",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := callHTTPControlTool(t, newTestHTTPAdapter(t, server, time.Second))
	if result.IsError {
		t.Fatalf("tool returned error: %+v", result.Content)
	}
	if observations != 1 || changes != 1 {
		t.Fatalf("calls: observations=%d changes=%d, want one each", observations, changes)
	}
}

func TestHTTPAPIAdapterFailsClosedOnObservationAuthError(t *testing.T) {
	var changes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observations" {
			http.Error(w, `{"error":{"code":"unauthorized","message":"invalid token"}}`, http.StatusUnauthorized)
			return
		}
		changes++
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()

	result := callHTTPControlTool(t, newTestHTTPAdapter(t, server, time.Second))
	if !result.IsError || changes != 0 {
		t.Fatalf("result error=%v changes=%d; expected fail-closed auth error", result.IsError, changes)
	}
}

func TestHTTPAPIAdapterFailsClosedOnStaleObservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observations" {
			_ = json.NewEncoder(w).Encode(observationHTTPResponse{
				RepositoryID: "disposable", Branch: "main", Path: "README.md",
				ObservationToken: "fresh-token",
			})
			return
		}
		http.Error(w, `{"error":{"code":"stale_observation","message":"stale"}}`, http.StatusConflict)
	}))
	defer server.Close()

	result := callHTTPControlTool(t, newTestHTTPAdapter(t, server, time.Second))
	if !result.IsError {
		t.Fatal("stale observation was reported as success")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].(*mcpsdk.TextContent).Text, "ambiguous outcome") {
		t.Fatalf("stale error should not invite unsafe retry: %+v", result.Content)
	}
}

func TestHTTPAPIAdapterRejectsMalformedObservationWithoutChange(t *testing.T) {
	var changes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observations" {
			_, _ = w.Write([]byte(`{"repository_id":"other","branch":"main","path":"README.md","observation_token":"token"}`))
			return
		}
		changes++
	}))
	defer server.Close()

	result := callHTTPControlTool(t, newTestHTTPAdapter(t, server, time.Second))
	if !result.IsError || changes != 0 {
		t.Fatalf("result error=%v changes=%d; expected reject mismatched observation", result.IsError, changes)
	}
}

func TestHTTPAPIAdapterFailsClosedOnTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observations" {
			_ = json.NewEncoder(w).Encode(observationHTTPResponse{
				RepositoryID: "disposable", Branch: "main", Path: "README.md",
				ObservationToken: "fresh-token",
			})
			return
		}
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(HTTPControlResponse{
			ExecutionID: "maybe-executed", Verified: true, Committed: true,
		})
	}))
	defer server.Close()

	result := callHTTPControlTool(t, newTestHTTPAdapter(t, server, 20*time.Millisecond))
	if !result.IsError {
		t.Fatal("timed-out change was reported as success")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].(*mcpsdk.TextContent).Text, "ambiguous outcome") {
		t.Fatalf("timeout should be reported as ambiguous: %+v", result.Content)
	}
}

func TestNewHTTPAPIAdapterRequiresExplicitSecureConfiguration(t *testing.T) {
	cases := []HTTPAPIConfig{
		{BaseURL: "", BearerToken: "token", Timeout: time.Second},
		{BaseURL: "https://api.example.com", BearerToken: "", Timeout: time.Second},
		{BaseURL: "https://api.example.com", BearerToken: "token", Timeout: 0},
		{BaseURL: "http://api.example.com", BearerToken: "token", Timeout: time.Second},
	}
	for i, cfg := range cases {
		if _, err := NewHTTPAPIAdapter(cfg); err == nil {
			t.Errorf("case %d: expected configuration rejection", i)
		}
	}
}
