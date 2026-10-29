package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitHubIdentity is who a GitHub token signs in as.
type GitHubIdentity struct {
	Login  string `json:"login"`
	Name   string `json:"name,omitempty"`
	Scopes string `json:"scopes,omitempty"`
	Host   string `json:"host"`
}

var githubHTTP = &http.Client{Timeout: 20 * time.Second}

// githubAPIBase is the REST API for host (a var for tests).
var githubAPIBase = func(host string) string {
	if host == "" || host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

func githubRequest(ctx context.Context, token, method, url string, body any, out any) (http.Header, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "NullBot")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub could not be reached: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &msg)
		return resp.Header, githubStatusError{status: resp.StatusCode, message: msg.Message}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("GitHub sent an unexpected reply: %v", err)
		}
	}
	return resp.Header, nil
}

type githubStatusError struct {
	status  int
	message string
}

func (e githubStatusError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "GitHub refused the token: it is wrong, expired or revoked"
	case http.StatusForbidden, http.StatusNotFound:
		if e.message != "" {
			return "GitHub refused this for the token's permissions: " + e.message
		}
		return "GitHub refused this for the token's permissions"
	}
	if e.message != "" {
		return fmt.Sprintf("GitHub answered %d: %s", e.status, e.message)
	}
	return fmt.Sprintf("GitHub answered %d", e.status)
}

func githubTokenSecret(config Config, name string) (Secret, error) {
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return Secret{}, err
	}
	i := secretIndex(payload, name)
	if i < 0 || payload.Secrets[i].Kind != SecretGitHubToken {
		return Secret{}, fmt.Errorf("there is no GitHub token named %s", name)
	}
	return payload.Secrets[i], nil
}

// VerifyGitHubToken asks GitHub who the token belongs to and records the
// login and scopes on the secret.
func VerifyGitHubToken(ctx context.Context, config Config, name string) (GitHubIdentity, error) {
	s, err := githubTokenSecret(config, name)
	if err != nil {
		return GitHubIdentity{}, err
	}
	host := firstNonEmpty(s.Public["host"], "github.com")
	var user struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	header, err := githubRequest(ctx, s.Values["token"], http.MethodGet, githubAPIBase(host)+"/user", nil, &user)
	if err != nil {
		return GitHubIdentity{}, err
	}
	id := GitHubIdentity{Login: user.Login, Name: user.Name, Host: host, Scopes: strings.TrimSpace(header.Get("X-OAuth-Scopes"))}
	err = updateSecrets(config, func(payload *vaultPayload) error {
		i := secretIndex(*payload, name)
		if i < 0 {
			return errors.New("the token was removed")
		}
		p := payload.Secrets[i].Public
		if p == nil {
			p = map[string]string{}
		}
		p["login"] = id.Login
		if id.Scopes != "" {
			p["scopes"] = id.Scopes
		} else {
			p["scopes"] = "fine-grained"
		}
		p["verified"] = time.Now().UTC().Format(time.RFC3339)
		payload.Secrets[i].Public = p
		return nil
	})
	return id, err
}

// AddSSHKeyToGitHub uploads an SSH secret's public key to the GitHub
// account of a token secret. The token needs permission to write public
// keys (admin:public_key, or "Git SSH keys" for fine-grained tokens).
func AddSSHKeyToGitHub(ctx context.Context, config Config, sshName, tokenName, title string) error {
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return err
	}
	i := secretIndex(payload, sshName)
	if i < 0 || payload.Secrets[i].Kind != SecretSSHKey || payload.Secrets[i].Public["public_key"] == "" {
		return fmt.Errorf("there is no SSH key named %s", sshName)
	}
	token, err := githubTokenSecret(config, tokenName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" {
		title = "NullBot " + sshName
	}
	host := firstNonEmpty(token.Public["host"], "github.com")
	_, err = githubRequest(ctx, token.Values["token"], http.MethodPost, githubAPIBase(host)+"/user/keys",
		map[string]string{"title": title, "key": payload.Secrets[i].Public["public_key"]}, nil)
	var status githubStatusError
	if errors.As(err, &status) && status.status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(status.message), "already") {
		return nil
	}
	if err != nil {
		return err
	}
	return updateSecrets(config, func(payload *vaultPayload) error {
		if i := secretIndex(*payload, sshName); i >= 0 {
			if payload.Secrets[i].Public == nil {
				payload.Secrets[i].Public = map[string]string{}
			}
			payload.Secrets[i].Public["host"] = firstNonEmpty(payload.Secrets[i].Public["host"], host)
			payload.Secrets[i].Public["added_to"] = host
		}
		return nil
	})
}
