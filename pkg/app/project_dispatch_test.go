package app

import (
	"context"
	"testing"
)

func TestRestrictedProjectsNeverStartMCP(t *testing.T) {
	c := projectTestConfig(t, "read-write")
	c.EnabledMCPServers = map[string]MCPEntry{"untrusted": {Command: "must-not-execute", Enabled: true, Transport: "stdio"}}
	a := New(c)
	tools, closers := a.loadMCPTools(context.Background(), c)
	if len(tools) != 0 || len(closers) != 0 {
		t.Fatal("restricted MCP loaded")
	}
}

func TestProjectValidationDoesNotMutateLivePermissions(t *testing.T) {
	a := New(projectTestConfig(t, "read-only"))
	err := a.UpdateConfig(func(c *Config) { c.Projects[0].Permission = "invalid" })
	if err == nil {
		t.Fatal("invalid grant saved")
	}
	if a.Config().Projects[0].Permission != "read-only" {
		t.Fatal("failed update mutated live permission")
	}
}
