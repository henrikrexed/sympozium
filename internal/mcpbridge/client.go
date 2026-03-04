package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// MaxResponseSize is the default maximum response body size (1MB).
const MaxResponseSize = 1 << 20

// Client communicates with a single MCP server via JSON-RPC 2.0 Streamable HTTP.
type Client struct {
	serverConfig ServerConfig
	httpClient   *http.Client
	sessionID    string
	nextID       int
}

// NewClient creates an MCP client for the given server configuration.
func NewClient(cfg ServerConfig) *Client {
	return &Client{
		serverConfig: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
	}
}

// DiscoverTools initializes the MCP session and lists available tools.
func (c *Client) DiscoverTools(ctx context.Context) ([]MCPTool, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return c.listTools(ctx)
}

// CallTool invokes a tool on the MCP server and returns the result.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage, meta map[string]any) (*MCPToolCallResult, error) {
	return c.callTool(ctx, name, arguments, meta)
}

// initialize sends the MCP initialize request to establish a session.
func (c *Client) initialize(ctx context.Context) error {
	params := MCPInitializeParams{
		ProtocolVersion: "2025-03-26",
		Capabilities:    MCPCapabilities{},
		ClientInfo: MCPImplementation{
			Name:    "sympozium-mcp-bridge",
			Version: "1.0.0",
		},
	}

	var result MCPInitializeResult
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return err
	}

	// Send initialized notification (no response expected, but we do it as a best-effort POST)
	_ = c.notify(ctx, "notifications/initialized")

	return nil
}

// listTools sends the tools/list request and returns discovered tools.
func (c *Client) listTools(ctx context.Context) ([]MCPTool, error) {
	var result MCPToolsListResult
	if err := c.call(ctx, "tools/list", nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// callTool sends a tools/call request.
func (c *Client) callTool(ctx context.Context, name string, arguments json.RawMessage, meta map[string]any) (*MCPToolCallResult, error) {
	params := MCPToolCallParams{
		Name:      name,
		Arguments: arguments,
		Meta:      meta,
	}

	var result MCPToolCallResult
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// call sends a JSON-RPC 2.0 request and unmarshals the result.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	c.nextID++
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverConfig.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	// Set session ID if we have one from a previous response
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	// Apply auth
	c.applyAuth(httpReq)

	// Apply custom headers
	for k, v := range c.serverConfig.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("HTTP request to %s: %w", c.serverConfig.URL, err)
	}
	defer resp.Body.Close()

	// Capture session ID from response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	// Read response with size limit
	limited := io.LimitReader(resp.Body, MaxResponseSize+1)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if len(respBody) > MaxResponseSize {
		return fmt.Errorf("response exceeds maximum size (%d bytes)", MaxResponseSize)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, c.serverConfig.URL, string(respBody))
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return fmt.Errorf("parsing JSON-RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		return fmt.Errorf("JSON-RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	if result != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("parsing result: %w", err)
		}
	}

	return nil
}

// notify sends a JSON-RPC 2.0 notification (no id, no response expected).
func (c *Client) notify(ctx context.Context, method string) error {
	type notification struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
	}

	body, err := json.Marshal(notification{JSONRPC: "2.0", Method: method})
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverConfig.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	c.applyAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// applyAuth adds authentication headers to the request.
func (c *Client) applyAuth(req *http.Request) {
	if c.serverConfig.Auth == nil {
		return
	}

	token := os.Getenv(c.serverConfig.Auth.SecretKey)
	if token == "" {
		return
	}

	switch c.serverConfig.Auth.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+token)
	case "header":
		req.Header.Set(c.serverConfig.Auth.HeaderName, token)
	}
}
