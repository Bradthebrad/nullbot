package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type skillSpec struct {
	Name        string
	Description string
	Content     string
}

func skillSpecsFromArgs(args map[string]any) []skillSpec {
	var specs []skillSpec
	if raw, ok := args["skills"].([]any); ok {
		for _, item := range raw {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			specs = append(specs, skillSpec{
				Name:        stringArg(obj, "name"),
				Description: stringArg(obj, "description"),
				Content:     stringArg(obj, "content"),
			})
		}
	}
	if len(specs) == 0 && strings.TrimSpace(stringArg(args, "name")) != "" {
		specs = append(specs, skillSpec{
			Name:        stringArg(args, "name"),
			Description: stringArg(args, "description"),
			Content:     stringArg(args, "content"),
		})
	}
	return specs
}

func writeSkillSpec(root string, spec skillSpec, overwrite bool) (string, bool, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return "", false, fmt.Errorf("skill name is required")
	}
	slug := slugSkillName(name)
	if slug == "" {
		return "", false, fmt.Errorf("skill name %q cannot be slugged safely", name)
	}
	dir := filepath.Join(root, slug)
	path := filepath.Join(dir, "SKILL.md")
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
		return "", false, fmt.Errorf("skill path escapes skills directory")
	}
	if _, err := os.Stat(path); err == nil && !overwrite {
		return path, false, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	content := strings.TrimSpace(spec.Content)
	if content == "" {
		content = defaultSkillMarkdown(name, spec.Description)
	}
	if len(content) > 128*1024 {
		return "", false, fmt.Errorf("skill content exceeds 128KB")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", false, err
	}
	return path, true, os.WriteFile(path, []byte(content+"\n"), 0600)
}

func primarySkillDir(config Config) string {
	if len(config.SkillDirs) > 0 && strings.TrimSpace(config.SkillDirs[0]) != "" {
		return config.SkillDirs[0]
	}
	return filepath.Join(config.AppDir, "skills")
}

func slugSkillName(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ' || r == '/':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func defaultSkillMarkdown(name, description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		description = "Use this skill when the user asks for help related to " + name + "."
	}
	return fmt.Sprintf(`---
name: %s
description: %s
---

# %s

%s

## Instructions

- Clarify the user's goal before taking action when requirements are ambiguous.
- Use available tools conservatively and explain important limitations.
- Keep outputs concise unless the user asks for depth.
`, slugSkillName(name), description, name, description)
}
