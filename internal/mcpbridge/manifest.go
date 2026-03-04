package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// discoverTools connects to all configured MCP servers, lists their tools,
// and builds a unified tool manifest with prefixed names.
func (b *Bridge) discoverTools(ctx context.Context) (*MCPToolManifest, error) {
	manifest := &MCPToolManifest{
		Tools: []MCPToolDef{},
	}

	for _, srv := range b.config.Servers {
		client := NewClient(srv)

		tools, err := client.DiscoverTools(ctx)
		if err != nil {
			log.Printf("WARNING: failed to discover tools from %q (%s): %v", srv.Name, srv.URL, err)
			continue
		}

		b.clients[srv.Name] = client

		for _, tool := range tools {
			prefixedName := srv.ToolsPrefix + "_" + tool.Name
			b.toolIndex[prefixedName] = srv.Name

			manifest.Tools = append(manifest.Tools, MCPToolDef{
				Name:        prefixedName,
				Description: tool.Description,
				Server:      srv.Name,
				Timeout:     srv.Timeout,
				InputSchema: tool.InputSchema,
			})
		}

		log.Printf("Discovered %d tools from %q (prefix=%q)", len(tools), srv.Name, srv.ToolsPrefix)
	}

	return manifest, nil
}

// WriteManifest writes the tool manifest atomically to the given path.
func WriteManifest(path string, manifest *MCPToolManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling manifest: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating manifest directory: %w", err)
	}

	// Write atomically: temp file + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming manifest: %w", err)
	}

	return nil
}
