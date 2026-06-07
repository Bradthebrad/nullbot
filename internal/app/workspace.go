package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func workspaceRoot(config Config) (string, error) {
	root := strings.TrimSpace(config.WorkspaceDir)
	if root == "" {
		root = defaultWorkspaceDir()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace is not a directory: %s", abs)
	}
	return abs, nil
}

func safeWorkspacePath(config Config, rel string) (string, error) {
	root, err := workspaceRoot(config)
	if err != nil {
		return "", err
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return root, nil
	}
	if filepath.IsAbs(rel) {
		rel = filepath.Clean(rel)
	} else {
		rel = filepath.Join(root, rel)
	}
	abs, err := filepath.Abs(rel)
	if err != nil {
		return "", err
	}
	rootWithSep := root
	if !strings.HasSuffix(rootWithSep, string(os.PathSeparator)) {
		rootWithSep += string(os.PathSeparator)
	}
	if abs != root && !strings.HasPrefix(abs, rootWithSep) {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	return abs, nil
}

func listWorkspaceDir(config Config, rel string, maxItems int) (string, error) {
	if maxItems <= 0 {
		maxItems = 200
	}
	dir, err := safeWorkspacePath(config, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", rel)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	root, _ := workspaceRoot(config)
	var b strings.Builder
	fmt.Fprintf(&b, "Workspace: `%s`\n", root)
	fmt.Fprintf(&b, "Directory: `%s`\n\n", relOrDot(root, dir))
	b.WriteString("```text\n")
	fmt.Fprintf(&b, "%-10s %10s  %-16s  %s\n", "mode", "size", "modified", "name")
	fmt.Fprintf(&b, "%-10s %10s  %-16s  %s\n", "----------", "----------", "----------------", "----")
	fmt.Fprintf(&b, "%-10s %10d  %-16s  %s\n", "drwxr-xr-x", int64(0), time.Now().Format("2006-01-02 15:04"), "./")
	if dir != root {
		fmt.Fprintf(&b, "%-10s %10d  %-16s  %s\n", "drwxr-xr-x", int64(0), time.Now().Format("2006-01-02 15:04"), "../")
	}
	for i, entry := range entries {
		if i >= maxItems {
			fmt.Fprintf(&b, "... %d more entries\n", len(entries)-i)
			break
		}
		entryInfo, _ := entry.Info()
		name := entry.Name()
		kind := "file"
		size := int64(0)
		mod := time.Time{}
		if entryInfo != nil {
			size = entryInfo.Size()
			mod = entryInfo.ModTime()
		}
		if entry.IsDir() {
			kind = "drwxr-xr-x"
			name += "/"
		} else {
			kind = fileModeString(entryInfo)
		}
		fmt.Fprintf(&b, "%-10s %10d  %-16s  %s\n", kind, size, mod.Format("2006-01-02 15:04"), name)
	}
	b.WriteString("```")
	return strings.TrimRight(b.String(), "\n"), nil
}

func fileModeString(info os.FileInfo) string {
	if info == nil {
		return "-rw-r--r--"
	}
	mode := info.Mode()
	chars := []byte("-rw-r--r--")
	if mode&0111 != 0 {
		chars[3] = 'x'
		chars[6] = 'x'
		chars[9] = 'x'
	}
	if mode&0200 == 0 {
		chars[2] = '-'
	}
	if mode&0040 != 0 {
		chars[5] = 'w'
	}
	if mode&0004 != 0 {
		chars[8] = 'r'
	}
	return string(chars)
}

func relOrDot(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return "."
	}
	return rel
}

func removeWorkspacePath(config Config, rel string, recursive bool, dirsOnly bool) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("path is required")
	}
	path, err := safeWorkspacePath(config, rel)
	if err != nil {
		return "", err
	}
	root, _ := workspaceRoot(config)
	if path == root {
		return "", fmt.Errorf("refusing to remove workspace root")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if dirsOnly && !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", rel)
	}
	if !dirsOnly && info.IsDir() && !recursive {
		return "", fmt.Errorf("%s is a directory; use /rmdir or /rm --recursive", rel)
	}
	if recursive {
		if err := os.RemoveAll(path); err != nil {
			return "", err
		}
	} else if err := os.Remove(path); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed `%s`.", relOrDot(root, path)), nil
}
