package app

import (
	"context"
	"fmt"
	"strings"
)

// MCPStatus snapshots every configured MCP server. detail adds tool lists and
// each server's stderr tail.
func (a *App) MCPStatus(detail bool) []MCPServerStatus {
	return a.mcp().Status(a.Config(), detail)
}

// MCPServerStatusByID returns one server's detailed status.
func (a *App) MCPServerStatusByID(id string) (MCPServerStatus, bool) {
	for _, status := range a.mcp().Status(a.Config(), true) {
		if status.ID == id {
			return status, true
		}
	}
	return MCPServerStatus{}, false
}

// StartMCPServer starts an enabled server now (it would otherwise start on
// first use) and clears any crash backoff.
func (a *App) StartMCPServer(ctx context.Context, id string) error {
	return a.mcp().Start(ctx, a.Config(), strings.TrimSpace(id))
}

// RestartMCPServer stops and starts an enabled server.
func (a *App) RestartMCPServer(ctx context.Context, id string) error {
	return a.mcp().Restart(ctx, a.Config(), strings.TrimSpace(id))
}

// StopMCPServer stops a running server without disabling it.
func (a *App) StopMCPServer(id string) {
	a.mcp().Stop(strings.TrimSpace(id))
}

// SetMCPToolPolicy overrides the capability a tool is treated as needing.
// policy is auto, read, write, delete, execute or blocked.
func (a *App) SetMCPToolPolicy(server, tool, policy string) error {
	server, tool = strings.TrimSpace(server), strings.TrimSpace(tool)
	policy = strings.ToLower(strings.TrimSpace(policy))
	if server == "" || tool == "" {
		return fmt.Errorf("server and tool are required")
	}
	switch policy {
	case "", PolicyAuto, PolicyBlocked, string(CapRead), string(CapWrite), string(CapDelete), string(CapExecute):
	default:
		return fmt.Errorf("unknown tool policy %q", policy)
	}
	return a.UpdateConfig(func(c *Config) {
		entry, ok := c.EnabledMCPServers[server]
		if !ok {
			return
		}
		policies := map[string]string{}
		for k, v := range entry.ToolPolicies {
			policies[k] = v
		}
		if policy == "" || policy == PolicyAuto {
			delete(policies, tool)
		} else {
			policies[tool] = policy
		}
		if len(policies) == 0 {
			policies = nil
		}
		entry.ToolPolicies = policies
		servers := make(map[string]MCPEntry, len(c.EnabledMCPServers))
		for k, v := range c.EnabledMCPServers {
			servers[k] = v
		}
		servers[server] = entry
		c.EnabledMCPServers = servers
	})
}

// Shutdown stops background work and every MCP server process.
func (a *App) Shutdown() {
	a.StopScheduler()
	a.mu.Lock()
	pool := a.mcpPool
	shared := a.hub != nil && a.chatID != MainChat
	a.mu.Unlock()
	if shared {
		// The tool servers belong to the main chat.
		return
	}
	if pool != nil {
		pool.Close()
	}
}
