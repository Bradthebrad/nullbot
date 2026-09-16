package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Permission string `json:"permission"`
}

// Full permits unsandboxed external tools/processes, not filesystem isolation.
const FullAccessWarning = "Full allows unsandboxed MCP servers and commands, which can access paths outside projects. All projects must be Full to enable external tools. Existing processes are not revoked."

func effectiveProjects(c Config) []Project {
	if len(c.Projects) != 0 {
		return c.Projects
	}
	return []Project{{ID: "workspace", Name: "Workspace", Path: c.WorkspaceDir, Permission: "read-only"}}
}

func normalizeProjects(c *Config) {
	if len(c.Projects) == 0 {
		c.Projects = effectiveProjects(*c)
		c.PrimaryProjectID = "workspace"
	}
	c.Projects = append([]Project(nil), c.Projects...)
	for _, p := range c.Projects {
		if p.ID == c.PrimaryProjectID {
			c.WorkspaceDir = p.Path
			return
		}
	}
	// Invalid primary IDs are rejected by validation, not silently granted.
}

func ValidateProjects(c Config) error {
	seen := map[string]bool{}
	if len(c.Projects) == 0 {
		return nil
	}
	primary := false
	for _, p := range c.Projects {
		if strings.TrimSpace(p.ID) == "" || seen[p.ID] {
			return fmt.Errorf("project IDs must be nonempty and unique")
		}
		seen[p.ID] = true
		if p.Permission != "read-only" && p.Permission != "read-write" && p.Permission != "full" {
			return fmt.Errorf("invalid permission for project %s", p.ID)
		}
		if !filepath.IsAbs(p.Path) {
			return fmt.Errorf("project path must be absolute: %s", p.Path)
		}
		info, err := os.Stat(p.Path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("project path must be a directory")
		}
		if p.ID == c.PrimaryProjectID {
			primary = true
		}
	}
	if !primary {
		return fmt.Errorf("select a valid primary project")
	}
	return nil
}

func allProjectsFull(c Config) bool {
	ps := effectiveProjects(c)
	if len(ps) == 0 {
		return false
	}
	for _, p := range ps {
		if p.Permission != "full" {
			return false
		}
	}
	return ValidateProjects(c) == nil
}

// canonicalPath resolves existing ancestors for new files. On Windows it rejects
// all reparse-point components, since EvalSymlinks does not reliably resolve junctions.
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := rejectProjectReparsePoints(abs); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return "", err
	}
	base, err := canonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, filepath.Base(abs)), nil
}
func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// CheckProjectPath uses the most restrictive overlapping grant. Tree mutations
// cannot cross a protected nested project. This is a policy check, not an OS sandbox.
func CheckProjectPath(c Config, path string, write, tree bool) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.WorkspaceDir, path)
	}
	target, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	matched := false
	for _, p := range effectiveProjects(c) {
		root, e := canonicalPath(p.Path)
		if e != nil {
			return "", fmt.Errorf("project unavailable: %w", e)
		}
		inside := pathWithin(root, target)
		if inside {
			matched = true
		}
		if inside || (tree && pathWithin(target, root)) {
			if p.Permission != "read-only" && p.Permission != "read-write" && p.Permission != "full" {
				return "", fmt.Errorf("invalid project permission")
			}
			if write && p.Permission == "read-only" {
				return "", fmt.Errorf("project %s is read-only", p.Name)
			}
			if write && tree && pathWithin(target, root) {
				return "", fmt.Errorf("cannot mutate project root %s", p.Name)
			}
		}
	}
	if !matched {
		return "", fmt.Errorf("path is outside configured projects")
	}
	return target, nil
}
