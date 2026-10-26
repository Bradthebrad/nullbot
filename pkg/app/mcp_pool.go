package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/mcp"
)

const (
	mcpStartTimeout  = 20 * time.Second
	mcpIdleTimeout   = 15 * time.Minute
	mcpReapInterval  = time.Minute
	mcpCrashWindow   = 15 * time.Second
	mcpMaxQuickFails = 3
)

// MCP server states reported to UIs.
const (
	MCPStateDisabled = "disabled"
	MCPStateIdle     = "idle"
	MCPStateStarting = "starting"
	MCPStateReady    = "ready"
	MCPStateError    = "error"
)

// MCPToolStatus describes one tool a server exposes.
type MCPToolStatus struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema map[string]any  `json:"input_schema,omitempty"`
	ReadOnly    *bool           `json:"read_only,omitempty"`
	Destructive *bool           `json:"destructive,omitempty"`
	OpenWorld   *bool           `json:"open_world,omitempty"`
	Policy      string          `json:"policy,omitempty"`
	Requirement ToolRequirement `json:"requirement"`
}

// MCPServerStatus is a snapshot of one configured server.
type MCPServerStatus struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Transport     string          `json:"transport"`
	Command       string          `json:"command"`
	Args          []string        `json:"args,omitempty"`
	Remote        bool            `json:"remote"`
	Enabled       bool            `json:"enabled"`
	State         string          `json:"state"`
	Error         string          `json:"error,omitempty"`
	ServerName    string          `json:"server_name,omitempty"`
	ServerVersion string          `json:"server_version,omitempty"`
	Instructions  string          `json:"instructions,omitempty"`
	ToolCount     int             `json:"tool_count"`
	Tools         []MCPToolStatus `json:"tools,omitempty"`
	StartedAt     time.Time       `json:"started_at,omitempty"`
	LastUsedAt    time.Time       `json:"last_used_at,omitempty"`
	Calls         int             `json:"calls"`
	Errors        int             `json:"errors"`
	AvgLatencyMs  int64           `json:"avg_latency_ms"`
	Restarts      int             `json:"restarts"`
	Pid           int             `json:"pid,omitempty"`
	Cwd           string          `json:"cwd,omitempty"`
	Stderr        string          `json:"stderr,omitempty"`
}

// mcpPool keeps one long-lived session per enabled MCP server, shared by the
// manager, subagents and Also runs. Sessions start lazily (in parallel), stop
// after an idle period, restart after a crash, and are reconciled whenever the
// configuration changes.
type mcpPool struct {
	app    *App
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*mcpSession
	closed   bool
	reaping  bool
}

type mcpSession struct {
	id          string
	fingerprint string

	mu           sync.Mutex
	entry        MCPEntry
	cwd          string
	client       *mcp.Client
	tools        []mcp.Tool
	state        string
	err          string
	starting     chan struct{}
	startedAt    time.Time
	lastUsed     time.Time
	calls        int
	errors       int
	totalLatency time.Duration
	restarts     int
	quickFails   int
	inflight     int
	draining     []*mcp.Client // stopped clients still serving in-flight calls
	generation   int
	pid          int
	serverInfo   mcp.Implementation
	instructions string
}

func newMCPPool(app *App) *mcpPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &mcpPool{app: app, ctx: ctx, cancel: cancel, sessions: map[string]*mcpSession{}}
}

// mcp returns the app's MCP pool, creating it on first use.
func (a *App) mcp() *mcpPool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mcpPool == nil {
		a.mcpPool = newMCPPool(a)
	}
	return a.mcpPool
}

func isRemoteTransport(transport string) bool {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "http", "streamable-http", "streamable_http", "sse":
		return true
	}
	return false
}

// mcpLaunch is everything that determines a server's process or session.
type mcpLaunch struct {
	entry MCPEntry
	cwd   string
	env   map[string]string
}

func (p *mcpPool) launchFor(config Config, id string, entry MCPEntry) mcpLaunch {
	launch := mcpLaunch{entry: entry}
	if primary, ok := PrimaryProject(config); ok {
		launch.cwd = primary.Path
	}
	env := mcpRuntimeEnv(config, id, entry)
	if env == nil {
		env = map[string]string{}
	}
	for k, v := range projectEnv(config) {
		env[k] = v
	}
	launch.env = env
	return launch
}

func (l mcpLaunch) fingerprint() string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, part := range parts {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
	}
	write(strings.ToLower(l.entry.Transport), l.entry.Command, l.cwd)
	write(l.entry.Args...)
	for _, m := range []map[string]string{l.env, l.entry.Headers} {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			write(k, m[k])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// projectEnv tells cooperative servers about the projects and their
// permissions. NullBot enforces the same rules on every call regardless.
func projectEnv(config Config) map[string]string {
	type envProject struct {
		ID           string       `json:"id"`
		Name         string       `json:"name"`
		Path         string       `json:"path"`
		Permission   string       `json:"permission"`
		Capabilities []Capability `json:"capabilities"`
		Primary      bool         `json:"primary"`
	}
	var projects []envProject
	primaryPath := ""
	for _, p := range effectiveProjects(config) {
		primary := p.ID == config.PrimaryProjectID
		if primary {
			primaryPath = p.Path
		}
		projects = append(projects, envProject{ID: p.ID, Name: projectDisplayName(p), Path: p.Path, Permission: p.Permission, Capabilities: ProjectCapabilities(p), Primary: primary})
	}
	if primaryPath == "" && len(projects) > 0 {
		primaryPath = projects[0].Path
	}
	data, _ := json.Marshal(projects)
	return map[string]string{
		"NULLBOT_PROJECTS":        string(data),
		"NULLBOT_PRIMARY_PROJECT": primaryPath,
	}
}

// projectRoots lists projects as MCP roots so servers that honor roots stay
// inside them.
func projectRoots(config Config) []mcp.Root {
	var roots []mcp.Root
	for _, p := range effectiveProjects(config) {
		if strings.TrimSpace(p.Path) == "" {
			continue
		}
		roots = append(roots, mcp.Root{URI: fileURI(p.Path), Name: projectDisplayName(p) + " (" + permissionLabel(p) + ")"})
	}
	return roots
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	slashed := filepath.ToSlash(abs)
	if runtime.GOOS == "windows" && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

// Tools returns agent tools for every enabled server, starting servers that
// are not running yet. It waits at most until ctx ends for slow starters; a
// server that is not ready simply contributes no tools to this turn.
func (p *mcpPool) Tools(ctx context.Context, config Config) []tcagent.Tool {
	p.reconcile(config)
	ids := enabledServerIDs(config)
	type result struct {
		id    string
		tools []tcagent.Tool
	}
	results := make(chan result, len(ids))
	for _, id := range ids {
		go func(id string) {
			session := p.session(id)
			if session == nil {
				results <- result{id: id}
				return
			}
			if err := session.ensure(ctx, p); err != nil {
				results <- result{id: id}
				return
			}
			results <- result{id: id, tools: p.agentTools(config, session)}
		}(id)
	}
	byID := map[string][]tcagent.Tool{}
	for range ids {
		r := <-results
		byID[r.id] = r.tools
	}
	var tools []tcagent.Tool
	for _, id := range ids {
		tools = append(tools, byID[id]...)
	}
	return tools
}

func enabledServerIDs(config Config) []string {
	var ids []string
	for id, entry := range config.EnabledMCPServers {
		if entry.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (p *mcpPool) session(id string) *mcpSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}

// reconcile brings sessions in line with config: removed or disabled servers
// stop, changed launch settings restart lazily, and new servers get an idle
// session. Live sessions are told when project roots change.
func (p *mcpPool) reconcile(config Config) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	var stop []*mcpSession
	changed := false
	for id, session := range p.sessions {
		entry, ok := config.EnabledMCPServers[id]
		if !ok || !entry.Enabled {
			stop = append(stop, session)
			delete(p.sessions, id)
			changed = true
		}
	}
	for _, id := range enabledServerIDs(config) {
		entry := config.EnabledMCPServers[id]
		launch := p.launchFor(config, id, entry)
		fp := launch.fingerprint()
		session := p.sessions[id]
		if session != nil && session.fingerprint == fp {
			session.mu.Lock()
			session.entry = entry // tool policies can change without a restart
			session.mu.Unlock()
			continue
		}
		if session != nil {
			stop = append(stop, session)
		}
		next := &mcpSession{id: id, fingerprint: fp, entry: entry, cwd: launch.cwd, state: MCPStateIdle}
		if session != nil {
			session.mu.Lock()
			next.restarts = session.restarts
			next.calls, next.errors, next.totalLatency = session.calls, session.errors, session.totalLatency
			session.mu.Unlock()
		}
		p.sessions[id] = next
		changed = true
	}
	if !p.reaping {
		p.reaping = true
		go p.reapLoop()
	}
	p.mu.Unlock()
	for _, session := range stop {
		session.stop()
	}
	if changed {
		p.app.notifyChange("mcp")
	}
}

// Reconfigure applies a configuration change immediately.
func (p *mcpPool) Reconfigure(config Config) {
	p.reconcile(config)
}

func (p *mcpPool) reapLoop() {
	ticker := time.NewTicker(mcpReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.reapIdle(time.Now())
		}
	}
}

func (p *mcpPool) reapIdle(now time.Time) {
	p.mu.Lock()
	sessions := make([]*mcpSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	p.mu.Unlock()
	reaped := false
	for _, s := range sessions {
		s.mu.Lock()
		idle := s.state == MCPStateReady && s.inflight == 0 && now.Sub(s.lastUsed) > mcpIdleTimeout
		s.mu.Unlock()
		if idle {
			s.stopTo(MCPStateIdle, "")
			reaped = true
		}
	}
	if reaped {
		p.app.notifyChange("mcp")
	}
}

// ensure starts the session if needed and waits for it.
func (s *mcpSession) ensure(ctx context.Context, p *mcpPool) error {
	for {
		s.mu.Lock()
		switch s.state {
		case MCPStateReady:
			if s.client != nil {
				select {
				case <-s.client.Done():
					// The process exited on its own; restart below.
					s.markCrashedLocked()
				default:
					s.mu.Unlock()
					return nil
				}
			}
		}
		if s.state == MCPStateError && s.quickFails >= mcpMaxQuickFails {
			err := s.err
			s.mu.Unlock()
			return fmt.Errorf("%s", err)
		}
		if s.starting == nil {
			s.starting = make(chan struct{})
			s.state = MCPStateStarting
			s.err = ""
			done := s.starting
			gen := s.generation
			s.mu.Unlock()
			p.app.notifyChange("mcp")
			go s.start(p, done, gen)
		} else {
			s.mu.Unlock()
		}
		s.mu.Lock()
		wait := s.starting
		s.mu.Unlock()
		if wait == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		}
		s.mu.Lock()
		state, errText := s.state, s.err
		s.mu.Unlock()
		switch state {
		case MCPStateReady:
			return nil
		case MCPStateIdle:
			continue // stopped while starting; try again
		}
		return fmt.Errorf("%s", errText)
	}
}

func (s *mcpSession) markCrashedLocked() {
	reason := "server exited"
	if s.client != nil && s.client.Err() != nil {
		reason = s.client.Err().Error()
	}
	if time.Since(s.startedAt) < mcpCrashWindow {
		s.quickFails++
	}
	s.client = nil
	s.tools = nil
	s.pid = 0
	s.state = MCPStateError
	s.err = reason
	s.restarts++
}

func (s *mcpSession) start(p *mcpPool, done chan struct{}, gen int) {
	config := p.app.Config()
	s.mu.Lock()
	entry := s.entry
	s.mu.Unlock()
	launch := p.launchFor(config, s.id, entry)
	p.app.logInfo("mcp start", "id", s.id, "transport", entry.Transport, "command", entry.Command, "cwd", launch.cwd)
	client, err := p.dial(launch)
	var tools []mcp.Tool
	var info mcp.Implementation
	instructions := ""
	if err == nil {
		client.SetRoots(func() []mcp.Root { return projectRoots(p.app.Config()) })
		client.SetClientInfo(mcp.Implementation{Name: "nullbot", Title: DisplayName(config)})
		initCtx, cancel := context.WithTimeout(p.ctx, mcpStartTimeout)
		var result *mcp.InitializeResult
		result, err = client.Initialize(initCtx)
		if err == nil {
			info = result.ServerInfo
			instructions = result.Instructions
			tools, err = client.ListTools(initCtx)
		}
		cancel()
		if err != nil {
			if tail := strings.TrimSpace(client.Stderr()); tail != "" {
				err = fmt.Errorf("%w\n%s", err, lastLinesOf(tail, 6))
			}
			_ = client.Close()
			client = nil
		}
	}
	s.mu.Lock()
	if s.generation != gen {
		// Stopped or replaced while starting: discard what we launched.
		s.starting = nil
		s.mu.Unlock()
		close(done)
		if client != nil {
			_ = client.Close()
		}
		return
	}
	if err != nil {
		s.state = MCPStateError
		s.err = err.Error()
		s.quickFails++
		p.app.logError("mcp start failed", "id", s.id, "error", err)
	} else {
		s.client = client
		s.tools = tools
		s.serverInfo = info
		s.instructions = instructions
		s.state = MCPStateReady
		s.startedAt = time.Now().UTC()
		s.lastUsed = s.startedAt
		if transport, ok := clientPid(client); ok {
			s.pid = transport
		}
		p.app.logInfo("mcp ready", "id", s.id, "tools", len(tools))
	}
	s.starting = nil
	s.mu.Unlock()
	close(done)
	p.app.notifyChange("mcp")
	if client != nil {
		go s.watch(p, client)
	}
}

// watch notices a server that exits on its own.
func (s *mcpSession) watch(p *mcpPool, client *mcp.Client) {
	done := client.Done()
	if done == nil {
		return
	}
	<-done
	s.mu.Lock()
	if s.client == client {
		s.markCrashedLocked()
		s.mu.Unlock()
		p.app.logError("mcp server exited", "id", s.id, "error", s.err)
		p.app.notifyChange("mcp")
		return
	}
	s.mu.Unlock()
}

func clientPid(client *mcp.Client) (int, bool) {
	type pider interface{ Pid() int }
	if transport, ok := client.Transport().(pider); ok {
		return transport.Pid(), transport.Pid() > 0
	}
	return 0, false
}

func (p *mcpPool) dial(launch mcpLaunch) (*mcp.Client, error) {
	entry := launch.entry
	switch strings.ToLower(strings.TrimSpace(entry.Transport)) {
	case "", "stdio":
		return mcp.NewStdioClientWithOptions(p.ctx, mcp.StdioOptions{Command: entry.Command, Args: entry.Args, Env: launch.env, Dir: launch.cwd})
	case "http", "streamable-http", "streamable_http", "sse":
		transport := &mcp.HTTPTransport{URL: entry.Command, Headers: entry.Headers}
		return mcp.NewClient(transport), nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", entry.Transport)
	}
}

func (s *mcpSession) stop() { s.stopTo(MCPStateIdle, "") }

func (s *mcpSession) stopTo(state, errText string) {
	s.mu.Lock()
	client := s.client
	s.client = nil
	s.tools = nil
	s.pid = 0
	s.state = state
	s.err = errText
	s.generation++
	if client != nil && s.inflight > 0 {
		// Let in-flight calls finish; the last one closes the client.
		s.draining = append(s.draining, client)
		client = nil
	}
	s.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// agentTools adapts a ready session's tools. Each tool resolves the live
// session at call time, so restarts are transparent to running agents.
func (p *mcpPool) agentTools(config Config, s *mcpSession) []tcagent.Tool {
	s.mu.Lock()
	specs := append([]mcp.Tool(nil), s.tools...)
	title := firstNonEmpty(s.entry.Name, s.id)
	s.mu.Unlock()
	tools := make([]tcagent.Tool, 0, len(specs))
	for _, spec := range specs {
		tools = append(tools, &pooledTool{pool: p, serverID: s.id, serverTitle: title, spec: spec})
	}
	return guardExternalTools(p.app, s.id, isRemoteTransport(config.EnabledMCPServers[s.id].Transport), tools)
}

type pooledTool struct {
	pool        *mcpPool
	serverID    string
	serverTitle string
	spec        mcp.Tool
}

func (t *pooledTool) Definition() lc.ToolDefinition {
	return lc.ToolDefinition{Name: t.spec.Name, Description: t.spec.Description, ArgsSchema: tcagent.NormalizeToolSchema(t.spec.InputSchema)}
}

func (t *pooledTool) ToolInfo() tcagent.ToolInfo {
	remote := mcp.RemoteTool{Spec: t.spec, ServerID: t.serverID, ServerTitle: t.serverTitle}
	return remote.ToolInfo()
}

func (t *pooledTool) Call(ctx context.Context, args map[string]any) (string, error) {
	out, _, err := t.CallWithMeta(ctx, args)
	return out, err
}

func (t *pooledTool) CallWithMeta(ctx context.Context, args map[string]any) (string, map[string]any, error) {
	session, client, err := t.acquire(ctx)
	if err != nil {
		return "", nil, err
	}
	started := time.Now()
	remote := &mcp.RemoteTool{Client: client, Spec: t.spec, ServerID: t.serverID, ServerTitle: t.serverTitle}
	out, meta, err := remote.CallWithMeta(ctx, args)
	elapsed := time.Since(started)
	session.mu.Lock()
	session.inflight--
	session.calls++
	session.totalLatency += elapsed
	session.lastUsed = time.Now().UTC()
	if err != nil {
		session.errors++
	}
	var drained []*mcp.Client
	if session.inflight == 0 && len(session.draining) > 0 {
		drained = session.draining
		session.draining = nil
	}
	session.mu.Unlock()
	for _, old := range drained {
		_ = old.Close()
	}
	t.pool.app.notifyChange("mcp")
	return out, meta, err
}

// acquire returns a live client and counts the call as in flight. It retries
// when the session is stopped or replaced between becoming ready and the call.
func (t *pooledTool) acquire(ctx context.Context) (*mcpSession, *mcp.Client, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		session := t.pool.session(t.serverID)
		if session == nil {
			return nil, nil, fmt.Errorf("tool server %s is not enabled", t.serverID)
		}
		if err := session.ensure(ctx, t.pool); err != nil {
			return nil, nil, fmt.Errorf("tool server %s is unavailable: %w", t.serverID, err)
		}
		session.mu.Lock()
		client := session.client
		if client != nil {
			session.inflight++
		}
		session.mu.Unlock()
		if client != nil {
			return session, client, nil
		}
		lastErr = fmt.Errorf("tool server %s restarted during the call", t.serverID)
	}
	return nil, nil, lastErr
}

// Status snapshots every configured server. Detail adds tool lists and the
// stderr tail.
func (p *mcpPool) Status(config Config, detail bool) []MCPServerStatus {
	p.reconcile(config)
	ids := make([]string, 0, len(config.EnabledMCPServers))
	for id := range config.EnabledMCPServers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]MCPServerStatus, 0, len(ids))
	for _, id := range ids {
		entry := config.EnabledMCPServers[id]
		status := MCPServerStatus{ID: id, Name: firstNonEmpty(entry.Name, id), Transport: firstNonEmpty(entry.Transport, "stdio"), Command: entry.Command, Args: entry.Args, Remote: isRemoteTransport(entry.Transport), Enabled: entry.Enabled, State: MCPStateDisabled}
		if session := p.session(id); session != nil && entry.Enabled {
			session.mu.Lock()
			status.State = session.state
			status.Error = session.err
			status.ServerName = session.serverInfo.Name
			status.ServerVersion = session.serverInfo.Version
			status.Instructions = session.instructions
			status.ToolCount = len(session.tools)
			status.StartedAt = session.startedAt
			status.LastUsedAt = session.lastUsed
			status.Calls = session.calls
			status.Errors = session.errors
			if session.calls > 0 {
				status.AvgLatencyMs = (session.totalLatency / time.Duration(session.calls)).Milliseconds()
			}
			status.Restarts = session.restarts
			status.Pid = session.pid
			status.Cwd = session.cwd
			if detail {
				status.Tools = toolStatuses(id, entry, session.tools)
				if session.client != nil {
					status.Stderr = session.client.Stderr()
				}
			}
			session.mu.Unlock()
		}
		out = append(out, status)
	}
	return out
}

func toolStatuses(serverID string, entry MCPEntry, tools []mcp.Tool) []MCPToolStatus {
	out := make([]MCPToolStatus, 0, len(tools))
	for _, tool := range tools {
		remote := mcp.RemoteTool{Spec: tool, ServerID: serverID}
		info := remote.ToolInfo()
		policy := entry.ToolPolicies[tool.Name]
		out = append(out, MCPToolStatus{
			Name: tool.Name, Title: info.Title, Description: tool.Description, InputSchema: tool.InputSchema,
			ReadOnly: info.ReadOnly, Destructive: info.Destructive, OpenWorld: info.OpenWorld,
			Policy:      firstNonEmpty(policy, PolicyAuto),
			Requirement: ClassifyTool(tool.Name, serverID, info, policy),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Start starts one server now and waits up to the start timeout.
func (p *mcpPool) Start(ctx context.Context, config Config, id string) error {
	p.reconcile(config)
	session := p.session(id)
	if session == nil {
		return fmt.Errorf("tool server %s is not enabled", id)
	}
	session.mu.Lock()
	session.quickFails = 0
	session.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, mcpStartTimeout+5*time.Second)
	defer cancel()
	return session.ensure(ctx, p)
}

// Restart stops and starts one server.
func (p *mcpPool) Restart(ctx context.Context, config Config, id string) error {
	if session := p.session(id); session != nil {
		session.mu.Lock()
		session.restarts++
		session.mu.Unlock()
		session.stop()
	}
	return p.Start(ctx, config, id)
}

// Stop stops one server without disabling it; it restarts on next use.
func (p *mcpPool) Stop(id string) {
	if session := p.session(id); session != nil {
		session.stop()
		p.app.notifyChange("mcp")
	}
}

// NotifyRootsChanged tells live sessions their roots changed.
func (p *mcpPool) NotifyRootsChanged() {
	p.mu.Lock()
	sessions := make([]*mcpSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	p.mu.Unlock()
	for _, s := range sessions {
		s.mu.Lock()
		client := s.client
		s.mu.Unlock()
		if client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = client.NotifyRootsChanged(ctx)
			cancel()
		}
	}
}

// Close stops every server.
func (p *mcpPool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	sessions := make([]*mcpSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	p.sessions = map[string]*mcpSession{}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *mcpSession) {
			defer wg.Done()
			s.stop()
			s.mu.Lock()
			drained := s.draining
			s.draining = nil
			s.mu.Unlock()
			for _, old := range drained {
				_ = old.Close()
			}
		}(s)
	}
	wg.Wait()
	p.cancel()
}

func lastLinesOf(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
