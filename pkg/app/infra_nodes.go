package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/infra"
)

// What runs where: for each machine NullBot made, what listens on which
// port, which services run, what nginx serves and which containers are up,
// read over SSH with the machine's key from the vault; and the same for
// this computer. Tunnels bring a machine's private ports (its Ollama) here.

// Listener is a port something listens on.
type Listener struct {
	Address string `json:"address"` // 127.0.0.1, 0.0.0.0, ::
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
	// Known names the usual owner of the port (Ollama, SSH, nginx…).
	Known string `json:"known,omitempty"`
	// Public reports a port open to other machines, not only this one.
	Public bool `json:"public"`
}

// NginxSite is a server block nginx serves.
type NginxSite struct {
	ServerNames []string `json:"server_names,omitempty"`
	Listen      []string `json:"listen,omitempty"`
	ProxyPass   []string `json:"proxy_pass,omitempty"`
	Root        string   `json:"root,omitempty"`
}

// NodeReport is what a machine runs, as read just now.
type NodeReport struct {
	Resource   string      `json:"resource,omitempty"` // NullBot id; empty for this computer
	Name       string      `json:"name"`
	Address    string      `json:"address,omitempty"`
	Read       time.Time   `json:"read"`
	Listeners  []Listener  `json:"listeners"`
	Services   []string    `json:"services,omitempty"`
	Nginx      []NginxSite `json:"nginx,omitempty"`
	Containers []string    `json:"containers,omitempty"`
	// Setup is the first-boot setup's state: done, running or none.
	Setup      string   `json:"setup,omitempty"`
	SetupLines []string `json:"setup_lines,omitempty"`
	System     []string `json:"system,omitempty"`
	Error      string   `json:"error,omitempty"`
}

var knownPorts = map[int]string{
	22: "SSH", 80: "HTTP", 443: "HTTPS", 1234: "LM Studio", 3000: "web app", 5432: "PostgreSQL", 6379: "Redis",
	8000: "vLLM / web app", 8080: "llama.cpp / web app", 11434: "Ollama", 3306: "MySQL", 9090: "Prometheus",
}

func knownFor(port int, process string) string {
	p := strings.ToLower(process)
	switch {
	case strings.Contains(p, "ollama"):
		return "Ollama"
	case strings.Contains(p, "llama-server"):
		return "llama.cpp"
	case strings.Contains(p, "nginx"):
		return "nginx"
	case strings.Contains(p, "sshd"):
		return "SSH"
	case strings.Contains(p, "vllm") || strings.Contains(p, "python") && port == 8000:
		return "vLLM"
	case strings.Contains(p, "nullbot"):
		return "NullBot"
	}
	return knownPorts[port]
}

func publicAddress(addr string) bool {
	switch addr {
	case "127.0.0.1", "::1", "localhost", "[::1]":
		return false
	}
	return !strings.HasPrefix(addr, "127.")
}

// sshCommand finds ssh, or NULLBOT_SSH_BIN.
func sshCommand() (string, error) {
	if path := strings.TrimSpace(os.Getenv("NULLBOT_SSH_BIN")); path != "" {
		return path, nil
	}
	path, err := exec.LookPath("ssh")
	if err != nil {
		return "", errors.New("ssh was not found; install OpenSSH (it ships with Windows 10 and later, macOS and Linux)")
	}
	return path, nil
}

// nodeSSH is how to reach a machine: its address, user and key.
type nodeSSH struct {
	argv    []string // ssh and its options, ready for a command or -L
	host    string
	cleanup func()
}

func (a *App) nodeConnection(id string) (infra.Resource, nodeSSH, error) {
	return a.nodeConnectionFor(id, false)
}

// nodeConnectionFor is nodeConnection for an agent when agent is set: the
// machine's SSH key must then be allowed for agents.
func (a *App) nodeConnectionFor(id string, agent bool) (infra.Resource, nodeSSH, error) {
	config := a.Config()
	resources, err := infraStore(config).Resources()
	if err != nil {
		return infra.Resource{}, nodeSSH{}, err
	}
	var node infra.Resource
	for _, r := range resources {
		if (r.ID == id || r.Name == id) && r.Kind == infra.KindInstance && r.Live() {
			node = r
		}
	}
	if node.ID == "" {
		return node, nodeSSH{}, fmt.Errorf("there is no running machine %s that NullBot made", id)
	}
	ip := node.Details["public_ip"]
	if net.ParseIP(ip) == nil {
		return node, nodeSSH{}, fmt.Errorf("%s has no public address yet", node.Name)
	}
	keyName := node.Details["ssh_key_secret"]
	if keyName == "" {
		return node, nodeSSH{}, fmt.Errorf("%s was made without an SSH key from the vault, so NullBot cannot log in", node.Name)
	}
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return node, nodeSSH{}, err
	}
	var key Secret
	for _, s := range payload.Secrets {
		if s.Name == keyName && s.Kind == SecretSSHKey {
			key = s
		}
	}
	if key.Name == "" {
		return node, nodeSSH{}, fmt.Errorf("the SSH key %s for %s is no longer in the vault", keyName, node.Name)
	}
	if agent {
		if err := agentMayUse(key); err != nil {
			return node, nodeSSH{}, err
		}
	}
	bin, err := sshCommand()
	if err != nil {
		return node, nodeSSH{}, err
	}
	sweepSecretTemp()
	env := &secretEnv{vars: map[string]string{}}
	keyFile, err := env.file("node.key", key.Values["private_key"])
	if err != nil {
		env.cleanup()
		return node, nodeSSH{}, err
	}
	knownHosts := filepath.Join(config.AppDir, "infra", "known_hosts")
	_ = os.MkdirAll(filepath.Dir(knownHosts), 0o700)
	user := firstNonEmpty(node.Details["ssh_user"], "ubuntu")
	// -F none: nothing from your ~/.ssh/config (proxies, forwarding, agents,
	// local commands) applies. The known_hosts path is quoted, with forward
	// slashes, as ssh splits that option at spaces.
	argv := []string{bin, "-F", "none", "-i", keyFile, "-l", user, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new", "-o", `UserKnownHostsFile="` + filepath.ToSlash(knownHosts) + `"`, "-o", "ConnectTimeout=10",
		"-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=no",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ServerAliveInterval=30"}
	return node, nodeSSH{argv: argv, host: ip, cleanup: env.cleanup}, nil
}

// nodeProbe is the read-only script a machine runs to say what it runs.
const nodeProbe = `echo '##ports'; (ss -Htlnp 2>/dev/null || ss -Htln 2>/dev/null) | head -200
echo '##services'; systemctl list-units --type=service --state=running --no-legend --plain --no-pager 2>/dev/null | awk '{print $1}' | head -200
echo '##nginx'; if command -v nginx >/dev/null; then sudo -n nginx -T 2>/dev/null | grep -E '^\s*(server\s*\{|server_name|listen|proxy_pass|root)\b' | head -400; fi
echo '##docker'; if command -v docker >/dev/null; then sudo -n docker ps --format '{{.Names}} ({{.Image}}) {{.Ports}}' 2>/dev/null | head -100; fi
echo '##setup'; if [ -f /var/lib/nullbot/bootstrap.done ]; then echo done; elif [ -f /var/log/nullbot-bootstrap.log ]; then echo running; tail -n 4 /var/log/nullbot-bootstrap.log; else echo none; fi
echo '##system'; uptime; df -h / | tail -1; free -m | sed -n 2p; nvidia-smi --query-gpu=name,memory.used,memory.total,utilization.gpu --format=csv,noheader 2>/dev/null
`

// InspectNode reads what a machine NullBot made is running.
func (a *App) InspectNode(ctx context.Context, id string) (NodeReport, error) {
	return a.inspectNode(ctx, id, false)
}

func (a *App) inspectNode(ctx context.Context, id string, agent bool) (NodeReport, error) {
	node, conn, err := a.nodeConnectionFor(id, agent)
	if err != nil {
		return NodeReport{Name: id, Error: err.Error()}, err
	}
	defer conn.cleanup()
	report := NodeReport{Resource: node.ID, Name: node.Name, Address: conn.host, Read: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	argv := append(append([]string(nil), conn.argv...), conn.host, "bash -s")
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(nodeProbe)
	cmd.Env = envList(commandBaseEnv())
	var out, errOut cappedBuffer
	out.limit, errOut.limit = 1<<20, 32<<10
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Run(); err != nil && out.buf.Len() == 0 {
		report.Error = RedactSecrets(a.Config(), firstNonEmpty(strings.TrimSpace(errOut.buf.String()), err.Error()))
		return report, errors.New(report.Error)
	}
	parseNodeProbe(&report, RedactSecrets(a.Config(), out.buf.String()))
	return report, nil
}

var usersPattern = regexp.MustCompile(`\(\("([^"]+)",pid=(\d+)`)

func parseNodeProbe(r *NodeReport, text string) {
	section := ""
	var site *NginxSite
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.HasPrefix(line, "##") {
			section = strings.TrimPrefix(line, "##")
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch section {
		case "ports":
			if l, ok := parseSSLine(trimmed); ok {
				r.Listeners = append(r.Listeners, l)
			}
		case "services":
			r.Services = append(r.Services, strings.TrimSuffix(trimmed, ".service"))
		case "nginx":
			fields := strings.Fields(strings.TrimSuffix(trimmed, ";"))
			if len(fields) == 0 {
				continue
			}
			switch fields[0] {
			case "server":
				r.Nginx = append(r.Nginx, NginxSite{})
				site = &r.Nginx[len(r.Nginx)-1]
			case "server_name":
				if site != nil {
					site.ServerNames = append(site.ServerNames, fields[1:]...)
				}
			case "listen":
				if site != nil && len(fields) > 1 {
					site.Listen = append(site.Listen, strings.Join(fields[1:], " "))
				}
			case "proxy_pass":
				if site != nil && len(fields) > 1 {
					site.ProxyPass = append(site.ProxyPass, fields[1])
				}
			case "root":
				if site != nil && len(fields) > 1 {
					site.Root = fields[1]
				}
			}
		case "docker":
			r.Containers = append(r.Containers, trimmed)
		case "setup":
			if r.Setup == "" {
				r.Setup = trimmed
			} else {
				r.SetupLines = append(r.SetupLines, trimmed)
			}
		case "system":
			r.System = append(r.System, trimmed)
		}
	}
	r.Listeners = dedupeListeners(r.Listeners)
}

// parseSSLine reads one line of `ss -Htlnp`:
// LISTEN 0 4096 127.0.0.1:11434 0.0.0.0:* users:(("ollama",pid=812,fd=3))
func parseSSLine(line string) (Listener, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return Listener{}, false
	}
	local := fields[3]
	i := strings.LastIndex(local, ":")
	if i < 0 {
		return Listener{}, false
	}
	port, err := strconv.Atoi(local[i+1:])
	if err != nil {
		return Listener{}, false
	}
	addr := strings.Trim(local[:i], "[]")
	if j := strings.Index(addr, "%"); j >= 0 {
		addr = addr[:j]
	}
	if addr == "*" {
		addr = "0.0.0.0"
	}
	l := Listener{Address: addr, Port: port, Public: publicAddress(addr)}
	if m := usersPattern.FindStringSubmatch(line); m != nil {
		l.Process = m[1]
		l.PID, _ = strconv.Atoi(m[2])
	}
	l.Known = knownFor(port, l.Process)
	return l, true
}

func sortListeners(ls []Listener) {
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Port != ls[j].Port {
			return ls[i].Port < ls[j].Port
		}
		return ls[i].Address < ls[j].Address
	})
}

// dedupeListeners shows the same port on IPv4 and IPv6 once.
func dedupeListeners(ls []Listener) []Listener {
	sortListeners(ls)
	var out []Listener
	for _, l := range ls {
		if n := len(out); n > 0 && l.Port == out[n-1].Port && l.Process == out[n-1].Process {
			out[n-1].Public = out[n-1].Public || l.Public
			continue
		}
		out = append(out, l)
	}
	return out
}

// localListenersFunc reads this computer's listening ports; tests replace
// it.
var localListenersFunc = localListeners

// ThisComputer reports what listens on this computer.
func ThisComputer(ctx context.Context) NodeReport {
	host, _ := os.Hostname()
	report := NodeReport{Name: firstNonEmpty(host, "This computer"), Read: time.Now().UTC()}
	ls, err := localListenersFunc(ctx)
	if err != nil {
		report.Error = err.Error()
	}
	report.Listeners = ls
	return report
}

func localListeners(ctx context.Context) ([]Listener, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ls []Listener
	switch runtime.GOOS {
	case "linux":
		out, err := exec.CommandContext(ctx, "ss", "-Htlnp").Output()
		if err != nil {
			// No ss (a minimal system): the kernel's own table.
			return dedupeListeners(procNetListeners()), nil
		}
		for _, line := range strings.Split(string(out), "\n") {
			if l, ok := parseSSLine(strings.TrimSpace(line)); ok {
				ls = append(ls, l)
			}
		}
	case "darwin":
		out, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
		if err != nil && len(out) == 0 {
			return nil, fmt.Errorf("lsof could not list ports: %w", err)
		}
		ls = parseLsof(string(out))
	case "windows":
		out, err := exec.CommandContext(ctx, "netstat", "-ano", "-p", "TCP").Output()
		if err != nil {
			return nil, fmt.Errorf("netstat could not list ports: %w", err)
		}
		names := windowsProcessNames(ctx)
		ls = parseNetstat(string(out), names)
	default:
		return nil, fmt.Errorf("listing ports is not supported on %s", runtime.GOOS)
	}
	return dedupeListeners(ls), nil
}

// parseLsof reads `lsof -nP -iTCP -sTCP:LISTEN`:
// ollama  812 me  3u IPv4 0x… 0t0 TCP 127.0.0.1:11434 (LISTEN)
func parseLsof(text string) []Listener {
	var ls []Listener
	for _, line := range strings.Split(text, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		name := fields[len(fields)-2]
		i := strings.LastIndex(name, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(name[i+1:])
		if err != nil {
			continue
		}
		addr := strings.Trim(name[:i], "[]")
		if addr == "*" {
			addr = "0.0.0.0"
		}
		pid, _ := strconv.Atoi(fields[1])
		ls = append(ls, Listener{Address: addr, Port: port, Process: fields[0], PID: pid, Known: knownFor(port, fields[0]), Public: publicAddress(addr)})
	}
	return ls
}

// parseNetstat reads `netstat -ano -p TCP` on Windows:
// TCP    127.0.0.1:11434   0.0.0.0:0   LISTENING   812
func parseNetstat(text string, names map[int]string) []Listener {
	var ls []Listener
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		local := fields[1]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil {
			continue
		}
		addr := strings.Trim(local[:i], "[]")
		pid, _ := strconv.Atoi(fields[4])
		ls = append(ls, Listener{Address: addr, Port: port, PID: pid, Process: names[pid], Known: knownFor(port, names[pid]), Public: publicAddress(addr)})
	}
	return ls
}

func windowsProcessNames(ctx context.Context) map[int]string {
	names := map[int]string{}
	out, err := exec.CommandContext(ctx, "tasklist", "/fo", "csv", "/nh").Output()
	if err != nil {
		return names
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\",\"")
		if len(parts) < 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.Trim(parts[1], "\""))
		if err == nil {
			names[pid] = strings.TrimSuffix(strings.Trim(parts[0], "\""), ".exe")
		}
	}
	return names
}

// --- tunnels -----------------------------------------------------------------

// Tunnel forwards a port on this computer to a port on a machine, over SSH.
type Tunnel struct {
	ID         string    `json:"id"` // resource:remote port
	Resource   string    `json:"resource"`
	Name       string    `json:"name"`
	LocalPort  int       `json:"local_port"`
	RemotePort int       `json:"remote_port"`
	Started    time.Time `json:"started"`
	Error      string    `json:"error,omitempty"`
	stop       func()
}

var tunnels = struct {
	sync.Mutex
	open     map[string]*Tunnel
	starting map[string]bool
}{open: map[string]*Tunnel{}, starting: map[string]bool{}}

// OpenTunnel forwards 127.0.0.1:localPort here to 127.0.0.1:remotePort on
// a machine NullBot made. localPort 0 picks a free one. The tunnel lasts
// until it is closed or NullBot exits.
func (a *App) OpenTunnel(id string, remotePort, localPort int) (Tunnel, error) {
	if remotePort < 1 || remotePort > 65535 || localPort < 0 || localPort > 65535 {
		return Tunnel{}, errors.New("choose ports between 1 and 65535")
	}
	node, conn, err := a.nodeConnection(id)
	if err != nil {
		return Tunnel{}, err
	}
	key := fmt.Sprintf("%s:%d", node.ID, remotePort)
	tunnels.Lock()
	if t, ok := tunnels.open[key]; ok {
		tunnels.Unlock()
		conn.cleanup()
		return *t, nil
	}
	if tunnels.starting[key] {
		tunnels.Unlock()
		conn.cleanup()
		return Tunnel{}, fmt.Errorf("a tunnel to port %d on %s is opening already", remotePort, node.Name)
	}
	tunnels.starting[key] = true
	tunnels.Unlock()
	defer func() {
		tunnels.Lock()
		delete(tunnels.starting, key)
		tunnels.Unlock()
	}()
	// The port must be free here, or the readiness check below would
	// answer for whatever already listens on it.
	if localPort == 0 {
		localPort = freeLocalPort(0)
	} else if freeLocalPort(localPort) != localPort {
		conn.cleanup()
		return Tunnel{}, fmt.Errorf("port %d is in use on this computer; choose another", localPort)
	}
	if localPort == 0 {
		conn.cleanup()
		return Tunnel{}, errors.New("no free port was found on this computer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	argv := append(append([]string(nil), conn.argv...), "-N", "-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort), conn.host)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = envList(commandBaseEnv())
	var errOut cappedBuffer
	errOut.limit = 16 << 10
	cmd.Stderr = &errOut
	proc := newSecretProcess(cmd)
	if err := proc.start(); err != nil {
		cancel()
		conn.cleanup()
		return Tunnel{}, err
	}
	t := &Tunnel{ID: key, Resource: node.ID, Name: node.Name, LocalPort: localPort, RemotePort: remotePort, Started: time.Now().UTC()}
	done := make(chan struct{})
	t.stop = func() {
		cancel()
		<-done
	}
	go func() {
		_ = cmd.Wait()
		proc.finish()
		conn.cleanup()
		tunnels.Lock()
		if tunnels.open[key] == t {
			delete(tunnels.open, key)
		}
		tunnels.Unlock()
		close(done)
	}()
	// Ready when the local port accepts connections, or failed if ssh ends.
	deadline := time.Now().Add(20 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), 300*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		select {
		case <-done:
			return Tunnel{}, fmt.Errorf("the tunnel to %s closed: %s", node.Name, RedactSecrets(a.Config(), firstNonEmpty(strings.TrimSpace(errOut.buf.String()), "ssh ended")))
		default:
		}
		if time.Now().After(deadline) {
			t.stop()
			return Tunnel{}, fmt.Errorf("the tunnel to %s did not open in time", node.Name)
		}
		time.Sleep(200 * time.Millisecond)
	}
	tunnels.Lock()
	tunnels.open[key] = t
	tunnels.Unlock()
	a.logInfo("tunnel opened", "machine", node.Name, "local", localPort, "remote", remotePort)
	return *t, nil
}

// Tunnels lists the open tunnels.
func Tunnels() []Tunnel {
	tunnels.Lock()
	defer tunnels.Unlock()
	out := make([]Tunnel, 0, len(tunnels.open))
	for _, t := range tunnels.open {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CloseTunnel closes a tunnel by id.
func CloseTunnel(id string) {
	tunnels.Lock()
	t := tunnels.open[id]
	tunnels.Unlock()
	if t != nil {
		t.stop()
	}
}

// CloseTunnels closes every tunnel, when NullBot exits.
func CloseTunnels() {
	for _, t := range Tunnels() {
		CloseTunnel(t.ID)
	}
}

// ConnectNodeOllama opens a tunnel to a machine's Ollama and adds it as a
// local model server named after the machine.
func (a *App) ConnectNodeOllama(id string) (LocalEndpoint, error) {
	// The same machine gets the same port here each time when it can, so
	// the model server saved for it keeps working after a reconnect.
	port := 0
	if resources, err := infraStore(a.Config()).Resources(); err == nil {
		for _, r := range resources {
			if r.ID == id || (r.Name == id && r.Kind == infra.KindInstance && r.Live()) {
				port = freeLocalPort(stablePort(r.ID))
			}
		}
	}
	t, err := a.OpenTunnel(id, 11434, port)
	if err != nil {
		return LocalEndpoint{}, err
	}
	id, previous, err := nodeServerID(a.Config(), t.Name)
	if err == nil {
		var e LocalEndpoint
		if e, err = a.SaveLocalEndpoint(LocalEndpoint{ID: id, Name: t.Name + " (cloud)", Kind: "ollama", BaseURL: fmt.Sprintf("http://127.0.0.1:%d", t.LocalPort)}, previous); err == nil {
			return e, nil
		}
	}
	CloseTunnel(t.ID)
	return LocalEndpoint{}, err
}

// freeLocalPort returns port if it is free on 127.0.0.1, or, given 0, a
// free port; 0 when there is none.
func freeLocalPort(port int) int {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// stablePort is a port between 41000 and 48999 that depends only on id.
func stablePort(id string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return 41000 + int(h.Sum32()%8000)
}

// nodeServerID is the local model server name for a machine's tunnel, and
// the server it replaces: reconnecting updates the one added last time,
// while a server of your own with the same name is left alone.
func nodeServerID(config Config, machine string) (id, previous string, err error) {
	title := machine + " (cloud)"
	id = strings.ToLower(machine)
	if !localIDPattern.MatchString(id) {
		id = "node"
	}
	if e, ok := localEndpointByID(config, id); ok && e.Name != title {
		id = strings.TrimSuffix(id[:min(len(id), 26)], "-") + "-cloud"
		if e, ok := localEndpointByID(config, id); ok && e.Name != title {
			return "", "", fmt.Errorf("local model servers %s and %s are already set up; remove one to connect %s", strings.ToLower(machine), id, machine)
		}
	}
	if _, ok := localEndpointByID(config, id); ok {
		previous = id
	}
	return id, previous, nil
}

// procNetListeners reads listening TCP sockets from /proc/net/tcp{,6}, and
// which process owns each where /proc lets us see it.
func procNetListeners() []Listener {
	owners := procSocketOwners()
	var ls []Listener
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) < 10 || f[3] != "0A" { // 0A is LISTEN
				continue
			}
			addrHex, portHex, ok := strings.Cut(f[1], ":")
			if !ok {
				continue
			}
			port, err := strconv.ParseInt(portHex, 16, 32)
			if err != nil {
				continue
			}
			addr := procAddr(addrHex)
			l := Listener{Address: addr, Port: int(port), Public: publicAddress(addr)}
			if o, ok := owners[f[9]]; ok {
				l.Process, l.PID = o.name, o.pid
			}
			l.Known = knownFor(l.Port, l.Process)
			ls = append(ls, l)
		}
	}
	return ls
}

// procAddr turns /proc/net's little-endian hex address into text.
func procAddr(h string) string {
	b := make([]byte, 0, 16)
	for i := 0; i+8 <= len(h); i += 8 {
		word, err := strconv.ParseUint(h[i:i+8], 16, 32)
		if err != nil {
			return h
		}
		b = append(b, byte(word), byte(word>>8), byte(word>>16), byte(word>>24))
	}
	ip := net.IP(b)
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

type procOwner struct {
	name string
	pid  int
}

func procSocketOwners() map[string]procOwner {
	owners := map[string]procOwner{}
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", d.Name(), "fd"))
		if err != nil {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", d.Name(), "comm"))
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join("/proc", d.Name(), "fd", fd.Name()))
			if err == nil && strings.HasPrefix(target, "socket:[") {
				owners[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = procOwner{strings.TrimSpace(string(comm)), pid}
			}
		}
	}
	return owners
}
