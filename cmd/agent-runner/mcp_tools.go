package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mcpToolManifest mirrors mcpbridge.MCPToolManifest for JSON deserialization.
type mcpToolManifest struct {
	Tools []mcpToolEntry `json:"tools"`
}

// mcpToolEntry mirrors mcpbridge.MCPToolDef.
type mcpToolEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Server      string         `json:"server"`
	Timeout     int            `json:"timeout"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpRequest mirrors mcpbridge.MCPRequest for IPC.
type mcpRequest struct {
	ID        string            `json:"id"`
	Server    string            `json:"server,omitempty"`
	Tool      string            `json:"tool"`
	Arguments json.RawMessage   `json:"arguments"`
	Meta      map[string]string `json:"_meta,omitempty"`
}

// mcpResult mirrors mcpbridge.MCPResult for IPC.
type mcpResult struct {
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Content json.RawMessage `json:"content,omitempty"`
	Error   string          `json:"error,omitempty"`
	IsError bool            `json:"isError,omitempty"`
}

// mcpContent mirrors mcpbridge.MCPContent.
type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// mcpToolRegistry maps prefixed tool names to their definitions.
// Populated by loadMCPTools and used by executeToolCall dispatch.
var mcpToolRegistry = map[string]mcpToolEntry{}

// loadMCPTools reads the MCP tool manifest and returns ToolDef entries
// for the LLM tool list. It also populates mcpToolRegistry for dispatch.
// If the manifest file doesn't exist within the wait period, it returns nil.
func loadMCPTools(manifestPath string) []ToolDef {
	// Wait for the manifest file to appear (bridge may still be starting)
	var data []byte
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		data, err = os.ReadFile(manifestPath)
		if err == nil && len(data) > 0 {
			break
		}
		data = nil
		time.Sleep(500 * time.Millisecond)
	}

	if len(data) == 0 {
		return nil
	}

	var manifest mcpToolManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		log.Printf("WARNING: failed to parse MCP tool manifest: %v", err)
		return nil
	}

	if len(manifest.Tools) == 0 {
		return nil
	}

	var tools []ToolDef
	for _, t := range manifest.Tools {
		mcpToolRegistry[t.Name] = t

		desc := t.Description
		if desc == "" {
			desc = "MCP tool"
		}
		desc += fmt.Sprintf(" [MCP: %s]", t.Server)

		params := t.InputSchema
		if params == nil {
			params = map[string]any{"type": "object"}
		}

		tools = append(tools, ToolDef{
			Name:        t.Name,
			Description: desc,
			Parameters:  params,
		})
	}

	log.Printf("Loaded %d MCP tool(s) from manifest", len(tools))
	return tools
}

// executeMCPTool dispatches an MCP tool call via file-based IPC to the
// mcp-bridge sidecar. It mirrors the executeCommand pattern exactly.
func executeMCPTool(ctx context.Context, tool mcpToolEntry, argsJSON string) string {
	id := fmt.Sprintf("%d", time.Now().UnixNano())

	req := mcpRequest{
		ID:        id,
		Server:    tool.Server,
		Tool:      tool.Name,
		Arguments: json.RawMessage(argsJSON),
		Meta:      traceMetadata(ctx),
	}

	toolsDir := "/ipc/tools"
	reqPath := filepath.Join(toolsDir, fmt.Sprintf("mcp-request-%s.json", id))
	resPath := filepath.Join(toolsDir, fmt.Sprintf("mcp-result-%s.json", id))

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Sprintf("Error marshalling MCP request: %v", err)
	}

	_ = os.MkdirAll(toolsDir, 0o755)
	if err := os.WriteFile(reqPath, data, 0o644); err != nil {
		return fmt.Sprintf("Error writing MCP request: %v", err)
	}

	log.Printf("Wrote MCP request %s: tool=%s server=%s", id, tool.Name, tool.Server)

	// Poll for result with a deadline (server timeout + 10s buffer)
	timeoutSec := tool.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	deadline := time.Now().Add(time.Duration(timeoutSec+10) * time.Second)

	for time.Now().Before(deadline) {
		resData, err := os.ReadFile(resPath)
		if err == nil {
			if len(resData) == 0 {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			var result mcpResult
			if err := json.Unmarshal(resData, &result); err != nil {
				// Partial write — retry once
				time.Sleep(100 * time.Millisecond)
				resData2, err2 := os.ReadFile(resPath)
				if err2 != nil || json.Unmarshal(resData2, &result) != nil {
					return fmt.Sprintf("Error parsing MCP result: %v", err)
				}
			}

			_ = os.Remove(reqPath)
			_ = os.Remove(resPath)

			return formatMCPResult(result)
		}
		time.Sleep(150 * time.Millisecond)
	}

	return "Error: timed out waiting for MCP tool result. The mcp-bridge sidecar may not be running."
}

// formatMCPResult converts an MCP result to a string for the LLM.
func formatMCPResult(r mcpResult) string {
	if !r.Success || r.IsError {
		if r.Error != "" {
			return fmt.Sprintf("MCP Error: %s", r.Error)
		}
		// Try to extract error text from content
		var content []mcpContent
		if json.Unmarshal(r.Content, &content) == nil {
			for _, c := range content {
				if c.Text != "" {
					return fmt.Sprintf("MCP Error: %s", c.Text)
				}
			}
		}
		return "MCP Error: unknown error"
	}

	// Extract text from content blocks
	var content []mcpContent
	if err := json.Unmarshal(r.Content, &content); err != nil {
		// If content is not an array of content blocks, return raw
		return string(r.Content)
	}

	var sb strings.Builder
	for _, c := range content {
		if c.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(c.Text)
		}
	}

	output := sb.String()
	if output == "" {
		output = "(no output)"
	}
	if len(output) > 50_000 {
		output = output[:50_000] + "\n... (output truncated)"
	}
	return output
}
