package app

import "os"

const defaultSkill = `---
name: api-operator
description: Safely discover APIs, explain API behavior, write focused skills, and search available MCP tools when tool discovery is installed.
allowed-tools: config_dir_list, config_dir_read, skills_list, history_recent, market_list, mcp_list
---

# API Operator

Use this skill when the user asks the bot to interact with APIs, understand API
documentation, write new SKILL.md files, or discover tools that could extend the
bot.

Principles:

- Prefer documented APIs and explicit user-provided credentials.
- When web tooling is installed, inspect official docs or visible API pages
  before forming requests.
- When web tooling is not installed, explain what tool category is needed.
- Write new skills as small SKILL.md files with clear trigger descriptions.
- Search installed MCP tools before claiming a capability is unavailable.
- Never pretend coding, shell, email, or browser tooling exists unless the user
  installed and enabled the relevant MCP server.
`

func ensureDefaultSkill(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(defaultSkill), 0600)
}
