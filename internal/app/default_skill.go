package app

import "os"

const defaultSkill = `---
name: nullbot-basics
description: Understand NullBot's local config, installed skills, configured MCP servers, and recent history without assuming extra capabilities.
allowed-tools: config_dir_list, config_dir_read, skills_list, history_recent, market_list, mcp_list
---

# NullBot Basics

Use this skill when the user asks what NullBot can currently do, which skills are
installed, which MCP servers are configured, where local app files live, or what
recent conversation context is available.

Principles:

- Be precise about available tools.
- Never claim coding, shell, email, browser, web search, or arbitrary filesystem
  access unless an enabled MCP server exposes that capability.
- Use skills_list to inspect installed skills.
- Use mcp_list to inspect configured MCP servers.
- Use history_recent for a compact look at recent conversation context.
- Use config-directory tools only for NullBot's own app data files.
`

func ensureDefaultSkill(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(defaultSkill), 0600)
}
