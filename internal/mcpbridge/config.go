package mcpbridge

import (
	"fmt"
	"net/url"
	"os"

	"gopkg.in/yaml.v3"
)

// DefaultTimeout is the default per-server call timeout in seconds.
const DefaultTimeout = 30

// LoadConfig reads and parses the MCP server registry from a YAML file.
func LoadConfig(path string) (*ServersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var cfg ServersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	// Apply defaults
	for i := range cfg.Servers {
		if cfg.Servers[i].Timeout <= 0 {
			cfg.Servers[i].Timeout = DefaultTimeout
		}
		if cfg.Servers[i].Transport == "" {
			cfg.Servers[i].Transport = "streamable-http"
		}
	}

	return &cfg, nil
}

// ValidateConfig checks the server registry for errors.
func ValidateConfig(cfg *ServersConfig) error {
	if len(cfg.Servers) == 0 {
		return nil // empty config is valid — bridge exits gracefully
	}

	names := make(map[string]bool, len(cfg.Servers))
	prefixes := make(map[string]bool, len(cfg.Servers))

	for i, s := range cfg.Servers {
		if s.Name == "" {
			return fmt.Errorf("server[%d]: name is required", i)
		}
		if s.URL == "" {
			return fmt.Errorf("server[%d] %q: url is required", i, s.Name)
		}
		if s.ToolsPrefix == "" {
			return fmt.Errorf("server[%d] %q: toolsPrefix is required", i, s.Name)
		}

		// Validate URL format
		if _, err := url.ParseRequestURI(s.URL); err != nil {
			return fmt.Errorf("server[%d] %q: invalid url %q: %w", i, s.Name, s.URL, err)
		}

		// Check unique names
		if names[s.Name] {
			return fmt.Errorf("server[%d]: duplicate server name %q", i, s.Name)
		}
		names[s.Name] = true

		// Check unique prefixes
		if prefixes[s.ToolsPrefix] {
			return fmt.Errorf("server[%d] %q: duplicate toolsPrefix %q", i, s.Name, s.ToolsPrefix)
		}
		prefixes[s.ToolsPrefix] = true

		// Validate transport
		if s.Transport != "streamable-http" {
			return fmt.Errorf("server[%d] %q: unsupported transport %q (only \"streamable-http\" is supported)", i, s.Name, s.Transport)
		}

		// Validate auth
		if s.Auth != nil {
			if s.Auth.Type != "bearer" && s.Auth.Type != "header" {
				return fmt.Errorf("server[%d] %q: unsupported auth type %q", i, s.Name, s.Auth.Type)
			}
			if s.Auth.SecretKey == "" {
				return fmt.Errorf("server[%d] %q: auth.secretKey is required", i, s.Name)
			}
			if s.Auth.Type == "header" && s.Auth.HeaderName == "" {
				return fmt.Errorf("server[%d] %q: auth.headerName is required for type \"header\"", i, s.Name)
			}
		}
	}

	return nil
}
