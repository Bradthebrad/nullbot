package app

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
)

// Capability is what a tool call needs from the project it touches.
//
// Project permissions map to capabilities:
//
//	read-only   read
//	read-write  read, write, delete
//	full        read, write, delete, execute
//	custom      read plus the capabilities listed in Project.Allow
//
// Every tool call (built-in, local stdio MCP, or remote MCP) is authorized
// against the project that owns each path it names. Calls that name no path
// are authorized against the primary project. This is a policy check on tool
// arguments, not an OS sandbox: a server process can still do anything its
// own code does.
type Capability string

const (
	CapRead    Capability = "read"
	CapWrite   Capability = "write"
	CapDelete  Capability = "delete"
	CapExecute Capability = "execute"
)

var capabilityRank = map[Capability]int{CapRead: 1, CapWrite: 2, CapDelete: 3, CapExecute: 4}

// AllCapabilities lists capabilities in increasing order of risk.
var AllCapabilities = []Capability{CapRead, CapWrite, CapDelete, CapExecute}

// Tool policy override values accepted in MCPEntry.ToolPolicies.
const (
	PolicyAuto    = "auto"
	PolicyBlocked = "blocked"
)

// ValidPermission reports whether a project permission is recognized.
func ValidPermission(permission string) bool {
	switch permission {
	case "read-only", "read-write", "full", "custom":
		return true
	}
	return false
}

// ProjectCapabilities returns what a project permits, lowest risk first.
func ProjectCapabilities(p Project) []Capability {
	switch p.Permission {
	case "read-only":
		return []Capability{CapRead}
	case "read-write":
		return []Capability{CapRead, CapWrite, CapDelete}
	case "full":
		return append([]Capability(nil), AllCapabilities...)
	case "custom":
		out := []Capability{CapRead}
		for _, c := range AllCapabilities[1:] {
			for _, allowed := range p.Allow {
				if Capability(strings.ToLower(strings.TrimSpace(allowed))) == c {
					out = append(out, c)
					break
				}
			}
		}
		return out
	}
	return nil
}

// ProjectAllows reports whether p permits capability c.
func ProjectAllows(p Project, c Capability) bool {
	for _, allowed := range ProjectCapabilities(p) {
		if allowed == c {
			return true
		}
	}
	return false
}

func permissionLabel(p Project) string {
	switch p.Permission {
	case "read-only":
		return "read-only"
	case "read-write":
		return "read & write"
	case "full":
		return "full access"
	case "custom":
		caps := ProjectCapabilities(p)
		names := make([]string, len(caps))
		for i, c := range caps {
			names[i] = string(c)
		}
		return "custom (" + strings.Join(names, ", ") + ")"
	}
	return p.Permission
}

// PrimaryProject returns the project the bot works in by default.
func PrimaryProject(c Config) (Project, bool) {
	projects := effectiveProjects(c)
	for _, p := range projects {
		if p.ID == c.PrimaryProjectID {
			return p, true
		}
	}
	if len(projects) > 0 {
		return projects[0], true
	}
	return Project{}, false
}

// ToolRequirement is how a tool is classified for authorization.
type ToolRequirement struct {
	Tool       string     `json:"tool"`
	Server     string     `json:"server,omitempty"`
	Capability Capability `json:"capability"`
	// Source explains where the classification came from: override, builtin,
	// annotation, name or default.
	Source string `json:"source"`
	Reason string `json:"reason"`
	// Internal tools only touch NullBot's own app data and skip project checks.
	Internal bool `json:"internal,omitempty"`
	Blocked  bool `json:"blocked,omitempty"`
}

// builtinRequirements classifies NullBot's own tools, whose implementations
// are known.
var builtinRequirements = map[string]ToolRequirement{
	"workspace_info":         {Capability: CapRead, Reason: "reads workspace metadata"},
	"list_dir":               {Capability: CapRead, Reason: "lists a project directory"},
	"project_read_file":      {Capability: CapRead, Reason: "reads a project file"},
	"project_write_file":     {Capability: CapWrite, Reason: "writes a project file"},
	"create_skill":           {Capability: CapWrite, Reason: "writes SKILL.md files"},
	"market_install_package": {Capability: CapExecute, Reason: "downloads and installs a tool server"},
	"mcp_enable_server":      {Capability: CapExecute, Reason: "enables a tool server"},
	"mcp_disable_server":     {Capability: CapWrite, Reason: "changes tool server configuration"},
	"mcp_remove_server":      {Capability: CapDelete, Reason: "removes a tool server"},
}

var internalBuiltins = map[string]string{
	"config_dir_list":       "reads NullBot app data",
	"config_dir_read":       "reads NullBot app data",
	"skills_list":           "reads installed skills",
	"skill_references":      "reads installed skills",
	"skill_read":            "reads installed skills",
	"plans_list":            "reads saved plans",
	"plan_read":             "reads saved plans",
	"plan_update_step":      "updates NullBot's own plan state",
	"history_recent":        "reads chat history",
	"history_sessions":      "reads chat history",
	"history_session_read":  "reads chat history",
	"history_search":        "searches chat history",
	"logs_recent":           "reads NullBot logs",
	"market_list":           "reads the market index",
	"market_refresh":        "refreshes the market index",
	"market_list_available": "reads the market index",
	"market_read_package":   "reads the market index",
	"mcp_list_servers":      "reads tool server configuration",
	"mcp_list":              "reads tool server configuration",
	"spawn_subagent":        "starts a subagent with the same permissions",
	"subagent_status":       "reads subagent status",
	"subagent_wait":         "waits for a subagent",
	"read_skill":            "reads installed skills",
}

var (
	executeWords = wordSet("run", "exec", "execute", "command", "cmd", "shell", "bash", "powershell", "terminal", "eval", "evaluate", "launch", "spawn", "kill", "process", "script", "invoke", "attach")
	deleteWords  = wordSet("delete", "remove", "rm", "rmdir", "unlink", "drop", "purge", "erase", "trash", "destroy", "wipe", "truncate")
	writeWords   = wordSet("write", "edit", "create", "update", "save", "put", "set", "patch", "apply", "move", "mv", "rename", "copy", "cp", "mkdir", "make", "upload", "send", "post", "publish", "reply", "forward", "generate", "resize", "crop", "compose", "convert", "render", "export", "download", "add", "insert", "append", "modify", "change", "replace", "commit", "push", "merge", "install", "enable", "disable", "click", "type", "fill", "submit", "press", "schedule", "cancel", "archive", "mark", "label", "share", "sync", "store", "record", "transform", "draw", "paint", "compress", "extract_to", "unzip", "zip")
	readWords    = wordSet("read", "get", "list", "ls", "search", "find", "fetch", "query", "lookup", "look", "view", "show", "inspect", "detect", "parse", "extract", "describe", "info", "status", "stat", "check", "count", "head", "tail", "grep", "browse", "navigate", "open", "screenshot", "capture", "preview", "summarize", "analyze", "scan", "tabs", "accounts", "models", "providers", "metadata", "parts", "tables", "text", "vision", "ping", "health", "version", "help", "workspace", "whoami", "explain", "diff", "log", "history", "watch", "poll", "wait", "resolve", "translate", "classify", "measure", "estimate", "compare", "validate", "verify", "test", "lint", "format_check")
)

func wordSet(words ...string) map[string]bool {
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}

var camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func nameTokens(name string) []string {
	name = camelBoundary.ReplaceAllString(name, "${1}_${2}")
	name = strings.ToLower(name)
	return strings.FieldsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

// classifyByName infers a capability from a tool's name. Unknown verbs are
// treated as write so a read-only project never silently runs them.
func classifyByName(name string) (Capability, string) {
	tokens := nameTokens(name)
	var hits [5][]string
	for _, token := range tokens {
		switch {
		case executeWords[token]:
			hits[4] = append(hits[4], token)
		case deleteWords[token]:
			hits[3] = append(hits[3], token)
		case writeWords[token]:
			hits[2] = append(hits[2], token)
		case readWords[token]:
			hits[1] = append(hits[1], token)
		}
	}
	switch {
	case len(hits[4]) > 0:
		return CapExecute, fmt.Sprintf("name contains %q", hits[4][0])
	case len(hits[3]) > 0:
		return CapDelete, fmt.Sprintf("name contains %q", hits[3][0])
	case len(hits[2]) > 0:
		return CapWrite, fmt.Sprintf("name contains %q", hits[2][0])
	case len(hits[1]) > 0:
		return CapRead, fmt.Sprintf("name contains %q", hits[1][0])
	}
	return "", ""
}

// ClassifyTool decides what capability a tool needs. Overrides win; then
// built-in knowledge; then the higher of the server's annotations and the
// name heuristic (so a server cannot downgrade "delete_files" by calling it
// read-only); unknown tools default to write.
func ClassifyTool(name, server string, info agent.ToolInfo, override string) ToolRequirement {
	req := ToolRequirement{Tool: name, Server: server}
	override = strings.ToLower(strings.TrimSpace(override))
	switch override {
	case "", PolicyAuto:
	case PolicyBlocked:
		req.Capability, req.Source, req.Reason, req.Blocked = CapExecute, "override", "blocked in tool settings", true
		return req
	default:
		if _, ok := capabilityRank[Capability(override)]; ok {
			req.Capability, req.Source, req.Reason = Capability(override), "override", "set in tool settings"
			return req
		}
	}
	if server == "" {
		if reason, ok := internalBuiltins[name]; ok {
			req.Capability, req.Source, req.Reason, req.Internal = CapRead, "builtin", reason, true
			if name == "plan_update_step" {
				req.Capability = CapWrite
			}
			return req
		}
		if known, ok := builtinRequirements[name]; ok {
			req.Capability, req.Source, req.Reason = known.Capability, "builtin", known.Reason
			return req
		}
	}
	nameCap, nameReason := classifyByName(name)
	var annCap Capability
	var annReason string
	switch {
	case info.Destructive != nil && *info.Destructive:
		annCap, annReason = CapDelete, "server marks it destructive"
	case info.ReadOnly != nil && *info.ReadOnly:
		annCap, annReason = CapRead, "server marks it read-only"
	case info.ReadOnly != nil && !*info.ReadOnly:
		annCap, annReason = CapWrite, "server marks it as modifying"
	}
	switch {
	case annCap != "" && (nameCap == "" || capabilityRank[annCap] >= capabilityRank[nameCap]):
		req.Capability, req.Source, req.Reason = annCap, "annotation", annReason
	case nameCap != "":
		req.Capability, req.Source, req.Reason = nameCap, "name", nameReason
		if annCap != "" {
			req.Reason += " (server hint: " + annReason + ")"
		}
	default:
		req.Capability, req.Source, req.Reason = CapWrite, "default", "unrecognized tool; treated as modifying"
	}
	return req
}

// PathCheck is one path argument and how it was authorized.
type PathCheck struct {
	Arg        string     `json:"arg"`
	Path       string     `json:"path"`
	Capability Capability `json:"capability"`
	Project    string     `json:"project,omitempty"`
	Allowed    bool       `json:"allowed"`
	Reason     string     `json:"reason,omitempty"`
}

// PermissionDecision is the outcome of authorizing one call.
type PermissionDecision struct {
	Allowed    bool        `json:"allowed"`
	Capability Capability  `json:"capability"`
	Source     string      `json:"source"`
	Rule       string      `json:"rule"`
	Project    string      `json:"project,omitempty"`
	Permission string      `json:"permission,omitempty"`
	Paths      []PathCheck `json:"paths,omitempty"`
	Reason     string      `json:"reason"`
}

func (d PermissionDecision) Map() map[string]any {
	out := map[string]any{
		"allowed":    d.Allowed,
		"capability": string(d.Capability),
		"source":     d.Source,
		"rule":       d.Rule,
		"reason":     d.Reason,
	}
	if d.Project != "" {
		out["project"] = d.Project
	}
	if d.Permission != "" {
		out["permission"] = d.Permission
	}
	if len(d.Paths) > 0 {
		paths := make([]map[string]any, 0, len(d.Paths))
		for _, p := range d.Paths {
			entry := map[string]any{"arg": p.Arg, "path": p.Path, "capability": string(p.Capability), "allowed": p.Allowed}
			if p.Project != "" {
				entry["project"] = p.Project
			}
			if p.Reason != "" {
				entry["reason"] = p.Reason
			}
			paths = append(paths, entry)
		}
		out["paths"] = paths
	}
	return out
}

// Err returns a model-readable error for a denied decision.
func (d PermissionDecision) Err() error {
	if d.Allowed {
		return nil
	}
	return fmt.Errorf("permission denied: %s", d.Reason)
}

// AuthorizeToolCall checks one call against the live project configuration.
func AuthorizeToolCall(c Config, req ToolRequirement, args map[string]any) PermissionDecision {
	decision := PermissionDecision{Capability: req.Capability, Source: req.Source, Rule: req.Reason}
	if req.Blocked {
		decision.Reason = fmt.Sprintf("%s is blocked in tool settings", req.Tool)
		return decision
	}
	if req.Internal {
		decision.Allowed = true
		decision.Reason = req.Reason
		return decision
	}
	if err := ValidateProjects(c); err != nil {
		decision.Reason = "project configuration is invalid: " + err.Error()
		return decision
	}
	refs := FindPathArgs(args)
	if len(refs) == 0 {
		primary, ok := PrimaryProject(c)
		if !ok {
			decision.Reason = "no primary project is configured"
			return decision
		}
		decision.Project, decision.Permission = primary.Name, primary.Permission
		if ProjectAllows(primary, req.Capability) {
			decision.Allowed = true
			decision.Reason = fmt.Sprintf("%s allowed in primary project %s (%s)", req.Capability, projectDisplayName(primary), permissionLabel(primary))
		} else {
			decision.Reason = fmt.Sprintf("%s needs %s, but primary project %s is %s", req.Tool, capabilityNeed(req.Capability), projectDisplayName(primary), permissionLabel(primary))
		}
		return decision
	}
	decision.Allowed = true
	for _, ref := range refs {
		need := req.Capability
		switch ref.Role {
		case "read":
			need = CapRead
		case "write":
			if capabilityRank[need] < capabilityRank[CapWrite] {
				need = CapWrite
			}
		}
		check := PathCheck{Arg: ref.Arg, Path: ref.Value, Capability: need}
		resolved, project, err := checkProjectCapability(c, ref.Value, need)
		if resolved != "" {
			check.Path = resolved
		}
		if project.ID != "" {
			check.Project = projectDisplayName(project)
			if decision.Project == "" {
				decision.Project, decision.Permission = check.Project, project.Permission
			}
		}
		if err != nil {
			check.Reason = err.Error()
			decision.Allowed = false
			if decision.Reason == "" {
				decision.Reason = fmt.Sprintf("%s %q: %v", ref.Arg, ref.Value, err)
			}
		} else {
			check.Allowed = true
		}
		decision.Paths = append(decision.Paths, check)
	}
	if decision.Allowed {
		decision.Reason = fmt.Sprintf("%s allowed for %d path(s)", req.Capability, len(refs))
		if decision.Project != "" {
			decision.Reason += " in " + decision.Project
		}
	}
	return decision
}

func capabilityNeed(c Capability) string {
	switch c {
	case CapRead:
		return "read access"
	case CapWrite:
		return "write access"
	case CapDelete:
		return "delete access"
	case CapExecute:
		return "permission to run programs"
	}
	return string(c)
}

func projectDisplayName(p Project) string {
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	if p.Path != "" {
		return filepath.Base(p.Path)
	}
	return p.ID
}

// checkProjectCapability resolves path (relative paths are relative to the
// primary project) and checks every overlapping project grant. The deepest
// containing project is returned for display.
func checkProjectCapability(c Config, path string, need Capability) (string, Project, error) {
	if !filepath.IsAbs(path) {
		base := c.WorkspaceDir
		if primary, ok := PrimaryProject(c); ok && primary.Path != "" {
			base = primary.Path
		}
		path = filepath.Join(base, path)
	}
	target, err := canonicalPath(path)
	if err != nil {
		return "", Project{}, err
	}
	tree := need == CapDelete || need == CapExecute
	var owner Project
	ownerDepth := -1
	matched := false
	for _, p := range effectiveProjects(c) {
		root, err := canonicalPath(p.Path)
		if err != nil {
			return target, Project{}, fmt.Errorf("project %s unavailable: %w", projectDisplayName(p), err)
		}
		inside := pathWithin(root, target)
		contains := tree && pathWithin(target, root)
		if !inside && !contains {
			continue
		}
		if inside {
			matched = true
			if depth := len(root); depth > ownerDepth {
				owner, ownerDepth = p, depth
			}
		}
		if !ProjectAllows(p, need) {
			if inside {
				return target, p, fmt.Errorf("project %s is %s and does not allow %s", projectDisplayName(p), permissionLabel(p), need)
			}
			return target, p, fmt.Errorf("%s would affect project %s, which is %s", target, projectDisplayName(p), permissionLabel(p))
		}
		if need == CapDelete && pathWithin(target, root) {
			return target, p, fmt.Errorf("cannot delete project root %s", projectDisplayName(p))
		}
	}
	if !matched {
		return target, Project{}, fmt.Errorf("%s is outside all configured projects", target)
	}
	return target, owner, nil
}

// PathRef is a filesystem path found in tool arguments.
type PathRef struct {
	Arg   string
	Value string
	// Role is "" (use the tool's capability), "read" (a source) or "write" (an
	// output destination).
	Role string
}

var strongPathKeys = map[string]string{
	"path": "", "paths": "", "file": "", "files": "", "file_path": "", "filepath": "", "file_paths": "", "filename": "", "file_name": "",
	"dir": "", "dirs": "", "directory": "", "folder": "", "cwd": "", "workdir": "", "work_dir": "", "working_dir": "", "working_directory": "",
	"root": "", "workspace_dir": "", "project_dir": "", "target_path": "", "image_path": "", "pdf_path": "", "document_path": "", "local_path": "",
	"input_path": "read", "source_path": "read", "src_path": "read", "from_path": "read", "input_paths": "read", "source_paths": "read",
	"output_path": "write", "output_dir": "write", "output_file": "write", "out_path": "write", "out_dir": "write", "dest_path": "write",
	"destination_path": "write", "save_path": "write", "save_to": "write", "save_dir": "write", "to_path": "write", "export_path": "write", "download_path": "write",
}

var weakPathKeys = map[string]string{
	"source": "read", "src": "read", "input": "read", "from": "read", "image": "read", "images": "read", "attachment": "read", "attachments": "read", "document": "read",
	"output": "write", "out": "write", "dest": "write", "destination": "write", "to": "write", "target": "", "location": "",
}

const maxPathScanDepth = 5

// FindPathArgs walks tool arguments for filesystem paths. Keys that name a
// path are always treated as paths; other string values are treated as paths
// only when they are unambiguous absolute local paths or file:// URIs.
func FindPathArgs(args map[string]any) []PathRef {
	var refs []PathRef
	seen := map[string]bool{}
	add := func(arg, value, role string) {
		key := arg + "\x00" + value
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, PathRef{Arg: arg, Value: value, Role: role})
	}
	var walk func(prefix string, key string, value any, depth int)
	walk = func(prefix, key string, value any, depth int) {
		if depth > maxPathScanDepth {
			return
		}
		lower := strings.ToLower(key)
		switch typed := value.(type) {
		case string:
			arg := prefix
			if role, strong := strongPathKeys[lower]; strong {
				if p, ok := normalizePathValue(typed, true); ok {
					add(arg, p, role)
				}
				return
			}
			if role, weak := weakPathKeys[lower]; weak {
				if p, ok := normalizePathValue(typed, false); ok && looksLikeRelativeOrAbsPath(typed) {
					add(arg, p, role)
					return
				}
			}
			if p, ok := absoluteLocalPath(typed); ok {
				add(arg, p, "")
			}
		case []any:
			for i, item := range typed {
				walk(fmt.Sprintf("%s[%d]", prefix, i), key, item, depth+1)
			}
		case []string:
			for i, item := range typed {
				walk(fmt.Sprintf("%s[%d]", prefix, i), key, item, depth+1)
			}
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for k := range typed {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				next := k
				if prefix != "" {
					next = prefix + "." + k
				}
				walk(next, k, typed[k], depth+1)
			}
		}
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		walk(k, k, args[k], 0)
	}
	return refs
}

var windowsAbsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func isURLLike(value string) bool {
	if i := strings.Index(value, "://"); i > 0 {
		scheme := strings.ToLower(value[:i])
		return scheme != "file"
	}
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "mailto:") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "javascript:")
}

// normalizePathValue converts file:// URIs and ~ to local paths. Values that
// are URLs, multi-line text or empty are rejected.
func normalizePathValue(value string, strong bool) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\n\r\x00") || isURLLike(value) {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(value), "file://") {
		u, err := url.Parse(value)
		if err != nil {
			return "", false
		}
		p := u.Path
		if runtime.GOOS == "windows" {
			p = strings.TrimPrefix(p, "/")
			if u.Host != "" && u.Host != "localhost" {
				p = `\\` + u.Host + `\` + p
			}
		}
		return filepath.FromSlash(p), p != ""
	}
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(home, value[1:]), true
	}
	if !strong && !looksLikeRelativeOrAbsPath(value) {
		return "", false
	}
	return value, true
}

func looksLikeRelativeOrAbsPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "." || value == ".." || filepath.IsAbs(value) || windowsAbsPath.MatchString(value) {
		return true
	}
	if strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, `.\`) || strings.HasPrefix(value, `..\`) || strings.HasPrefix(value, "~") {
		return true
	}
	if strings.ContainsAny(value, " \t") && !strings.ContainsAny(value, `/\`) {
		return false
	}
	return strings.ContainsAny(value, `/\`) || filepath.Ext(value) != ""
}

// absoluteLocalPath recognizes paths inside free-form values (for example a
// command line) only when they are unambiguous: Windows drive or UNC paths,
// file:// URIs, ~/, or unix paths whose first component exists on this
// machine.
func absoluteLocalPath(value string) (string, bool) {
	value = strings.TrimSpace(strings.Trim(value, `"'`))
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\n\r\x00") {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(value), "file://") || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		return normalizePathValue(value, true)
	}
	if strings.ContainsAny(value, " \t") {
		return "", false
	}
	if windowsAbsPath.MatchString(value) || strings.HasPrefix(value, `\\`) {
		if runtime.GOOS == "windows" {
			return value, true
		}
		return "", false
	}
	if runtime.GOOS != "windows" && strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
		if len(parts) < 2 || parts[0] == "" {
			return "", false
		}
		if _, err := os.Stat("/" + parts[0]); err == nil {
			return value, true
		}
	}
	return "", false
}

// --- tool wrapper ----------------------------------------------------------

// policyTool authorizes every call against the live configuration before it
// reaches the wrapped tool, and reports the decision in the result metadata.
type policyTool struct {
	inner  agent.Tool
	state  *App
	server string
	remote bool
}

func (t *policyTool) Definition() lc.ToolDefinition { return t.inner.Definition() }

func (t *policyTool) ToolInfo() agent.ToolInfo {
	info, _ := agent.InfoOf(t.inner)
	if info.Source == "" {
		if t.server == "" {
			info.Source = "builtin"
		} else {
			info.Source = "mcp"
		}
	}
	if info.Server == "" {
		info.Server = t.server
	}
	return info
}

// Requirement classifies the tool under the current configuration.
func (t *policyTool) Requirement() ToolRequirement {
	name := t.inner.Definition().Name
	info, _ := agent.InfoOf(t.inner)
	override := ""
	if t.server != "" && t.state != nil {
		if entry, ok := t.state.Config().EnabledMCPServers[t.server]; ok {
			override = entry.ToolPolicies[name]
		}
	}
	return ClassifyTool(name, t.server, info, override)
}

func (t *policyTool) Call(ctx context.Context, args map[string]any) (string, error) {
	out, _, err := t.CallWithMeta(ctx, args)
	return out, err
}

func (t *policyTool) CallWithMeta(ctx context.Context, args map[string]any) (string, map[string]any, error) {
	req := t.Requirement()
	decision := AuthorizeToolCall(t.state.Config(), req, args)
	meta := map[string]any{"permission": decision.Map()}
	if !decision.Allowed {
		t.state.logInfo("tool call denied", "tool", req.Tool, "server", t.server, "reason", decision.Reason)
		return "", meta, decision.Err()
	}
	var out string
	var innerMeta map[string]any
	var err error
	if rich, ok := t.inner.(agent.MetaTool); ok {
		out, innerMeta, err = rich.CallWithMeta(ctx, args)
	} else {
		out, err = t.inner.Call(ctx, args)
	}
	for k, v := range innerMeta {
		meta[k] = v
	}
	return out, meta, err
}

// guardTools wraps tools with per-call project authorization. server is empty
// for NullBot's built-in tools.
func guardTools(state *App, server string, remote bool, tools []agent.Tool) []agent.Tool {
	if state == nil {
		return tools
	}
	out := make([]agent.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, &policyTool{inner: tool, state: state, server: server, remote: remote})
	}
	return out
}
