package app

import (
	"context"
	"slices"
)

// App methods for the desktop UI and other front ends. Each one works on
// the live configuration and tells subscribers that secrets changed.

func (a *App) VaultStatus() VaultInfo { return VaultStatus(a.Config()) }

func (a *App) Secrets() ([]SecretInfo, error) { return ListSecrets(a.Config()) }

func (a *App) SecretKinds() []SecretKindInfo { return SecretKinds() }

func (a *App) SaveSecret(input SecretInput) (SecretInfo, error) {
	config := a.Config()
	before := boundServers(config, input.Name)
	info, err := SaveSecret(config, input)
	if err != nil {
		a.logError("secret save failed", "name", input.Name, "error", RedactSecrets(config, err.Error()))
		return SecretInfo{}, err
	}
	a.logInfo("secret saved", "name", info.Name, "kind", info.Kind, "agent_use", info.AgentUse, "servers", len(info.Servers))
	a.secretsChanged(info.Kind == SecretAPIKey, append(before, info.Servers...))
	return info, nil
}

func (a *App) RemoveSecret(name string) error {
	config := a.Config()
	before := boundServers(config, name)
	if err := RemoveSecret(config, name); err != nil {
		return err
	}
	a.logInfo("secret removed", "name", name)
	a.secretsChanged(true, before)
	return nil
}

func (a *App) GenerateSSHKey(name, host, description string, agentUse bool) (SecretInfo, error) {
	info, err := GenerateSSHKey(a.Config(), name, host, description, agentUse)
	if err == nil {
		a.logInfo("ssh key generated", "name", name, "fingerprint", info.Public["fingerprint"])
		a.secretsChanged(false, nil)
	}
	return info, err
}

func (a *App) VerifyGitHubToken(ctx context.Context, name string) (GitHubIdentity, error) {
	id, err := VerifyGitHubToken(ctx, a.Config(), name)
	if err == nil {
		a.secretsChanged(false, nil)
	}
	return id, err
}

func (a *App) AddSSHKeyToGitHub(ctx context.Context, sshName, tokenName, title string) error {
	err := AddSSHKeyToGitHub(ctx, a.Config(), sshName, tokenName, title)
	if err == nil {
		a.logInfo("ssh key added to github", "key", sshName, "token", tokenName)
		a.secretsChanged(false, nil)
	}
	return err
}

func (a *App) CreateVault(protection, passphrase string) error {
	err := CreateVault(a.Config(), protection, passphrase)
	if err == nil {
		a.secretsChanged(true, nil)
	}
	return err
}

func (a *App) UnlockVault(passphrase string) error {
	err := UnlockVault(a.Config(), passphrase)
	if err == nil {
		a.logInfo("vault unlocked")
		a.secretsChanged(true, nil)
	}
	return err
}

func (a *App) LockVault() {
	LockVault(a.Config())
	a.logInfo("vault locked")
	// Servers that were given secrets keep them while they run; stop them.
	a.secretsChanged(true, serversWithSecrets())
}

func (a *App) ReprotectVault(protection, passphrase string) error {
	err := ReprotectVault(a.Config(), protection, passphrase)
	if err == nil {
		a.logInfo("vault protection changed", "protection", protection)
		a.secretsChanged(false, nil)
	}
	return err
}

func boundServers(config Config, name string) []string {
	secrets, err := ListSecrets(config)
	if err != nil {
		return nil
	}
	for _, s := range secrets {
		if s.Name == name {
			return s.Servers
		}
	}
	return nil
}

// secretsChanged restarts tool servers whose secrets changed and, for model
// keys, rebuilds the model clients.
func (a *App) secretsChanged(keys bool, servers []string) {
	slices.Sort(servers)
	for _, id := range slices.Compact(servers) {
		a.StopMCPServer(id)
	}
	if keys {
		a.MarkRuntimeDirty("secrets changed")
	}
	a.notifyChange(ChangeSecrets)
}

// apiKeyServers lists tool servers given an API key secret.
func apiKeyServers(config Config) []string {
	secrets, err := ListSecrets(config)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range secrets {
		if s.Kind == SecretAPIKey {
			out = append(out, s.Servers...)
		}
	}
	return out
}
