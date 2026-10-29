package infra

import (
	"fmt"
	"regexp"
	"strings"
)

// Bootstrap is what a new machine sets up on its first boot.
type Bootstrap struct {
	// Ollama installs Ollama (listening on the machine itself only) and
	// pulls these models, run with Context tokens of context.
	Ollama       bool     `json:"ollama,omitempty"`
	OllamaModels []string `json:"ollama_models,omitempty"`
	Context      int      `json:"context,omitempty"`
	// GPUDrivers installs the NVIDIA driver Ubuntu recommends, then
	// restarts once. Set it for GPU machines.
	GPUDrivers bool `json:"gpu_drivers,omitempty"`
	// NullBot installs a headless NullBot for SSHUser, using the machine's
	// Ollama when there is one, and runs "nullbot serve" as a service.
	NullBot bool `json:"nullbot,omitempty"`
	// NullBotVersion is a release tag such as v0.5.0; empty means latest.
	NullBotVersion string `json:"nullbot_version,omitempty"`
	// Packages are extra Ubuntu packages to install.
	Packages []string `json:"packages,omitempty"`
}

// BootstrapDone is the file a machine writes when its first-boot setup
// finished; its log is BootstrapLog.
const (
	BootstrapDone = "/var/lib/nullbot/bootstrap.done"
	BootstrapLog  = "/var/log/nullbot-bootstrap.log"
)

var (
	ollamaModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]{0,199}$`)
	packagePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,62}$`)
	versionPattern     = regexp.MustCompile(`^v\d+\.\d+\.\d+([.-][0-9A-Za-z.-]+)?$`)
)

// Script returns the first-boot script for a machine, checking every value
// that goes into it, so the script cannot be made to run anything else.
func (b Bootstrap) Script(machine, sshUser string, arm bool) (string, error) {
	if !b.Ollama && !b.NullBot && !b.GPUDrivers && len(b.Packages) == 0 && len(b.OllamaModels) == 0 {
		return "", nil
	}
	if !ValidName(sshUser) {
		return "", fmt.Errorf("%q is not a user name", sshUser)
	}
	for _, m := range b.OllamaModels {
		if !ollamaModelPattern.MatchString(m) || strings.Contains(m, "..") {
			return "", fmt.Errorf("%q is not an Ollama model name", m)
		}
	}
	if len(b.OllamaModels) > 0 && !b.Ollama {
		return "", fmt.Errorf("models to pull need ollama: true")
	}
	for _, p := range b.Packages {
		if !packagePattern.MatchString(p) {
			return "", fmt.Errorf("%q is not an Ubuntu package name", p)
		}
	}
	if b.NullBotVersion != "" && !versionPattern.MatchString(b.NullBotVersion) {
		return "", fmt.Errorf("%q is not a NullBot release tag such as v0.5.0", b.NullBotVersion)
	}
	if b.Context == 0 {
		b.Context = 16384
	}
	if b.Context < 2048 || b.Context > 1<<20 {
		return "", fmt.Errorf("choose a context between 2,048 and 1,048,576 tokens")
	}
	arch := "amd64"
	if arm {
		arch = "arm64"
	}

	var s strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&s, format+"\n", args...) }
	w("#!/bin/bash")
	w("# First-boot setup for %s, written by NullBot. Its log: %s", machine, BootstrapLog)
	w("set -euo pipefail")
	w("exec >>%s 2>&1", BootstrapLog)
	w("export DEBIAN_FRONTEND=noninteractive")
	w("mkdir -p /var/lib/nullbot")
	w("if [ -f /var/lib/nullbot/drivers.done ] && [ ! -f %s ]; then echo 'resuming after the driver restart'; fi", BootstrapDone)
	w("apt-get update -y")
	pkgs := append([]string{"curl", "ca-certificates", "jq"}, b.Packages...)
	w("apt-get install -y %s", strings.Join(pkgs, " "))
	if b.GPUDrivers {
		w("if [ ! -f /var/lib/nullbot/drivers.done ]; then")
		w("  apt-get install -y ubuntu-drivers-common")
		w("  ubuntu-drivers install --gpgpu")
		w("  touch /var/lib/nullbot/drivers.done")
		// cloud-init runs user data only on the first boot; the rest runs
		// from this unit after the restart.
		w("  cp \"$0\" /var/lib/nullbot/bootstrap.sh && chmod 700 /var/lib/nullbot/bootstrap.sh")
		w("  cat >/etc/systemd/system/nullbot-bootstrap.service <<'UNIT'")
		w("[Unit]\nDescription=NullBot first-boot setup, after the driver restart\nAfter=network-online.target\nWants=network-online.target\nConditionPathExists=!%s", BootstrapDone)
		w("[Service]\nType=oneshot\nExecStart=/var/lib/nullbot/bootstrap.sh")
		w("[Install]\nWantedBy=multi-user.target\nUNIT")
		w("  systemctl enable nullbot-bootstrap.service")
		w("  reboot")
		w("  exit 0")
		w("fi")
	}
	if b.Ollama {
		w("if ! command -v ollama >/dev/null; then curl -fsSL https://ollama.com/install.sh | sh; fi")
		// Ollama listens on the machine itself only; reach it through SSH.
		w("mkdir -p /etc/systemd/system/ollama.service.d")
		w("cat >/etc/systemd/system/ollama.service.d/nullbot.conf <<'UNIT'")
		w("[Service]\nEnvironment=OLLAMA_HOST=127.0.0.1:11434\nEnvironment=OLLAMA_CONTEXT_LENGTH=%d\nUNIT", b.Context)
		w("systemctl daemon-reload")
		w("systemctl enable --now ollama")
		w("systemctl restart ollama")
		w("for i in $(seq 1 60); do curl -fsS http://127.0.0.1:11434/api/version >/dev/null && break; sleep 2; done")
		for _, m := range b.OllamaModels {
			w("ollama pull %s", m)
		}
	}
	if b.NullBot {
		base := "https://github.com/Bradthebrad/nullbot/releases/latest/download"
		if b.NullBotVersion != "" {
			base = "https://github.com/Bradthebrad/nullbot/releases/download/" + b.NullBotVersion
		}
		w("cd /tmp")
		w("curl -fsSLO %s/nullbot-linux-%s", base, arch)
		w("curl -fsSLO %s/SHA256SUMS.txt", base)
		w("grep ' nullbot-linux-%s$' SHA256SUMS.txt | sha256sum -c -", arch)
		w("install -m 0755 nullbot-linux-%s /usr/local/bin/nullbot", arch)
		w("rm -f nullbot-linux-%s SHA256SUMS.txt", arch)
		home := "/home/" + sshUser
		w("id %s >/dev/null 2>&1 || useradd -m -s /bin/bash %s", sshUser, sshUser)
		if b.Ollama {
			// The node's NullBot uses the node's own Ollama. Releases before
			// v0.6.0 have no "models" command; the rest of the setup goes on
			// without it, and the log says what to do.
			w("if sudo -u %s nullbot --app-dir %s/.nullbot models local add ollama; then", sshUser, home)
			w("  echo 'Ollama on this machine is now a model server for NullBot'")
			if len(b.OllamaModels) > 0 {
				w("  sudo -u %s nullbot --app-dir %s/.nullbot models use local:ollama/%s || true", sshUser, home, b.OllamaModels[0])
			}
			w("else")
			w("  echo 'this NullBot release cannot set up models; choose the model in its config.json, or use a release from v0.6.0'")
			w("fi")
		}
		w("cat >/etc/systemd/system/nullbot.service <<'UNIT'")
		w("[Unit]\nDescription=NullBot (headless)\nAfter=network-online.target ollama.service\nWants=network-online.target")
		w("[Service]\nUser=%s\nEnvironment=NULLBOT_APP_DIR=%s/.nullbot\nExecStart=/usr/local/bin/nullbot serve\nStandardInput=null\nRestart=on-failure\nRestartSec=10", sshUser, home)
		w("[Install]\nWantedBy=multi-user.target\nUNIT")
		w("systemctl daemon-reload")
		w("systemctl enable --now nullbot.service")
	}
	w("date -u +%%FT%%TZ >%s", BootstrapDone)
	w("echo 'NullBot first-boot setup finished'")
	return s.String(), nil
}
