package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
)

const maxSkillReadBytes = 192 * 1024

type SkillSummary struct {
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Path         string           `json:"path"`
	Root         string           `json:"root"`
	AllowedTools []string         `json:"allowed_tools,omitempty"`
	References   []SkillReference `json:"references,omitempty"`
	Size         int64            `json:"size,omitempty"`
	ModifiedAt   time.Time        `json:"modified_at,omitempty"`
	Error        string           `json:"error,omitempty"`
}

type SkillReference struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Size        int64  `json:"size,omitempty"`
	Description string `json:"description,omitempty"`
}

type SkillReadResult struct {
	SkillName string `json:"skill_name"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

func scanSkillSummaries(config Config) []SkillSummary {
	paths := scanSkillFiles(config)
	summaries := make([]SkillSummary, 0, len(paths))
	for _, path := range paths {
		summary := skillSummary(path)
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return strings.ToLower(summaries[i].Name) < strings.ToLower(summaries[j].Name)
	})
	return summaries
}

func skillSummary(path string) SkillSummary {
	summary := SkillSummary{Path: filepath.ToSlash(path), Root: filepath.ToSlash(filepath.Dir(path))}
	info, err := os.Stat(path)
	if err != nil {
		summary.Error = err.Error()
		return summary
	}
	summary.Size = info.Size()
	summary.ModifiedAt = info.ModTime()
	skill, err := tcagent.ParseSkillFile(path)
	if err != nil {
		summary.Name = strings.TrimSuffix(filepath.Base(filepath.Dir(path)), string(os.PathSeparator))
		summary.Error = err.Error()
		return summary
	}
	summary.Name = skill.Name
	summary.Description = skill.Description
	summary.AllowedTools = append([]string{}, skill.AllowedTools...)
	if info.Size() <= maxSkillReadBytes {
		if data, err := os.ReadFile(path); err == nil {
			summary.References = skillReferencesFromContent(filepath.Dir(path), string(data))
		}
	}
	return summary
}

func skillReferencesFromContent(root, content string) []SkillReference {
	seen := map[string]bool{}
	var refs []SkillReference
	add := func(raw string) {
		raw = strings.TrimSpace(strings.Trim(raw, `"'`))
		raw = strings.TrimPrefix(raw, "./")
		raw = filepath.Clean(filepath.FromSlash(raw))
		if raw == "." || raw == "" || strings.HasPrefix(raw, "..") || filepath.IsAbs(raw) {
			return
		}
		if !strings.HasSuffix(strings.ToLower(raw), ".md") || strings.EqualFold(filepath.Base(raw), "SKILL.md") {
			return
		}
		display := filepath.ToSlash(raw)
		if seen[display] {
			return
		}
		seen[display] = true
		full, ok := safeSkillReferencePath(root, raw)
		ref := SkillReference{Path: display}
		if ok {
			if info, err := os.Stat(full); err == nil && !info.IsDir() {
				ref.Exists = true
				ref.Size = info.Size()
				ref.Description = firstMarkdownHeading(full)
			}
		}
		refs = append(refs, ref)
	}
	linkRe := regexp.MustCompile(`\]\(([^)]+\.md(?:#[^)]+)?)\)`)
	for _, match := range linkRe.FindAllStringSubmatch(content, -1) {
		add(strings.Split(match[1], "#")[0])
	}
	pathRe := regexp.MustCompile("`([^`]+\\.md)`")
	for _, match := range pathRe.FindAllStringSubmatch(content, -1) {
		add(strings.Split(match[1], "#")[0])
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })
	return refs
}

func firstMarkdownHeading(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return ""
}

func safeSkillReferencePath(root, rel string) (string, bool) {
	rootClean, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", false
	}
	full, err := filepath.Abs(filepath.Clean(filepath.Join(rootClean, rel)))
	if err != nil {
		return "", false
	}
	if full != rootClean && !strings.HasPrefix(full, rootClean+string(os.PathSeparator)) {
		return "", false
	}
	return full, true
}

func findSkillSummary(config Config, nameOrPath string) (SkillSummary, bool) {
	nameOrPath = strings.TrimSpace(nameOrPath)
	if nameOrPath == "" {
		return SkillSummary{}, false
	}
	needle := strings.ToLower(strings.Trim(nameOrPath, `"'`))
	for _, summary := range scanSkillSummaries(config) {
		if strings.ToLower(summary.Name) == needle ||
			strings.ToLower(filepath.Base(summary.Root)) == needle ||
			strings.EqualFold(summary.Path, nameOrPath) {
			return summary, true
		}
	}
	return SkillSummary{}, false
}

func readSkillMarkdown(config Config, nameOrPath, rel string) (SkillReadResult, error) {
	summary, ok := findSkillSummary(config, nameOrPath)
	if !ok {
		return SkillReadResult{}, fmt.Errorf("skill not found: %s", nameOrPath)
	}
	target := filepath.FromSlash(summary.Path)
	if strings.TrimSpace(rel) != "" && !strings.EqualFold(strings.TrimSpace(rel), "SKILL.md") {
		full, safe := safeSkillReferencePath(filepath.FromSlash(summary.Root), rel)
		if !safe {
			return SkillReadResult{}, fmt.Errorf("skill reference escapes skill directory")
		}
		target = full
	}
	info, err := os.Stat(target)
	if err != nil {
		return SkillReadResult{}, err
	}
	if info.IsDir() {
		return SkillReadResult{}, fmt.Errorf("%s is a directory", target)
	}
	if info.Size() > maxSkillReadBytes {
		return SkillReadResult{}, fmt.Errorf("%s is too large for skill_read", target)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return SkillReadResult{}, err
	}
	return SkillReadResult{SkillName: summary.Name, Path: filepath.ToSlash(target), Content: string(data)}, nil
}
