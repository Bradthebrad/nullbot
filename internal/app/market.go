package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const marketSchemaVersion = 1

type MarketManifest struct {
	SchemaVersion int             `json:"schema_version"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Sources       []MarketSource  `json:"sources"`
	Packages      []MarketPackage `json:"packages"`
}

type MarketSource struct {
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Owner string   `json:"owner"`
	Repos []string `json:"repos"`
}

type MarketPackage struct {
	ID               string            `json:"id"`
	Kind             string            `json:"kind"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	Repo             string            `json:"repo"`
	ReleaseTag       string            `json:"release_tag,omitempty"`
	Assets           []MarketAsset     `json:"assets,omitempty"`
	SkillFiles       []MarketSkillFile `json:"skill_files,omitempty"`
	DefaultTransport string            `json:"default_transport,omitempty"`
	DefaultArgs      []string          `json:"default_args,omitempty"`
	Permissions      []string          `json:"permissions,omitempty"`
	Installed        bool              `json:"installed"`
	Enabled          bool              `json:"enabled"`
	InstallDir       string            `json:"install_dir,omitempty"`
	InstalledAsset   string            `json:"installed_asset,omitempty"`
	InstalledAt      string            `json:"installed_at,omitempty"`
	Status           string            `json:"status"`
	Readme           string            `json:"readme,omitempty"`
	Error            string            `json:"error,omitempty"`
}

type MarketAsset struct {
	Name       string `json:"name"`
	Platform   string `json:"platform,omitempty"`
	Arch       string `json:"arch,omitempty"`
	URL        string `json:"url"`
	SHA256     string `json:"sha256,omitempty"`
	Compressed bool   `json:"compressed,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

type MarketSkillFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	URL  string `json:"url"`
}

func marketDir(config Config) string {
	return filepath.Join(config.AppDir, "market")
}

func manifestPath(config Config) string {
	return filepath.Join(marketDir(config), "manifest.json")
}

func sourcesPath(config Config) string {
	return filepath.Join(marketDir(config), "sources.json")
}

func LoadMarketManifest(config Config) (MarketManifest, error) {
	if err := EnsureAppDir(config); err != nil {
		return MarketManifest{}, err
	}
	if err := ensureMarketSources(config); err != nil {
		return MarketManifest{}, err
	}
	path := manifestPath(config)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			manifest := defaultMarketManifest(config)
			if err := SaveMarketManifest(config, manifest); err != nil {
				return MarketManifest{}, err
			}
			return manifest, nil
		}
		return MarketManifest{}, err
	}
	var manifest MarketManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return MarketManifest{}, err
	}
	manifest = mergeInstalledState(config, mergePackages(defaultMarketManifest(config), manifest))
	return manifest, nil
}

func SaveMarketManifest(config Config, manifest MarketManifest) error {
	if err := os.MkdirAll(marketDir(config), 0700); err != nil {
		return err
	}
	manifest.SchemaVersion = marketSchemaVersion
	manifest.UpdatedAt = time.Now().UTC()
	sort.Slice(manifest.Packages, func(i, j int) bool { return manifest.Packages[i].ID < manifest.Packages[j].ID })
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath(config), append(data, '\n'), 0600)
}

func ensureMarketSources(config Config) error {
	path := sourcesPath(config)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	sources := defaultMarketSources()
	data, err := json.MarshalIndent(map[string]any{"schema_version": marketSchemaVersion, "sources": sources}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func defaultMarketSources() []MarketSource {
	return []MarketSource{{
		Name:  "nullbot-official",
		Type:  "github_releases",
		Owner: "Bradthebrad",
		Repos: []string{
			"nullbot-code-mcp",
			"nullbot-parsers-mcp",
			"nullbot-imagetools-mcp",
			"nullbot-web-mcp",
			"nullbot-skills",
		},
	}}
}

func defaultMarketManifest(config Config) MarketManifest {
	return MarketManifest{
		SchemaVersion: marketSchemaVersion,
		UpdatedAt:     time.Now().UTC(),
		Sources:       defaultMarketSources(),
		Packages: []MarketPackage{
			{
				ID:               "nullbot-code-mcp",
				Kind:             "mcp_server",
				Name:             "NullBot Code MCP",
				Description:      "Workspace-bounded coding tools: files, search, exact edits, and async commands.",
				Repo:             "Bradthebrad/nullbot-code-mcp",
				ReleaseTag:       "v0.1.0",
				DefaultTransport: "stdio",
				DefaultArgs:      []string{"--workspace", "{{workspace}}"},
				Permissions:      []string{"workspace_read", "workspace_write", "command_execute"},
				Status:           "available",
				Assets:           defaultReleaseAssets("Bradthebrad/nullbot-code-mcp", "v0.1.0", "nullbot-code-mcp"),
			},
			{
				ID:               "nullbot-parsers-mcp",
				Kind:             "mcp_server",
				Name:             "NullBot Parsers MCP",
				Description:      "Document/file parsing tools for PDF, Office, EPUB, archives, email, notebooks, tables, and image metadata.",
				Repo:             "Bradthebrad/nullbot-parsers-mcp",
				ReleaseTag:       "v0.1.0",
				DefaultTransport: "stdio",
				DefaultArgs:      []string{"--workspace", "{{workspace}}"},
				Permissions:      []string{"document_parse", "workspace_read"},
				Status:           "available",
				Assets:           defaultReleaseAssets("Bradthebrad/nullbot-parsers-mcp", "v0.1.0", "nullbot-parsers-mcp"),
			},
			{
				ID:               "nullbot-imagetools-mcp",
				Kind:             "mcp_server",
				Name:             "NullBot Image Tools MCP",
				Description:      "Image generation, editing, thumbnail composition, contact sheets, and image PDFs with OpenAI/OpenRouter plus local Go tools.",
				Repo:             "Bradthebrad/nullbot-imagetools-mcp",
				ReleaseTag:       "v0.1.1",
				DefaultTransport: "stdio",
				DefaultArgs:      []string{"--workspace", "{{workspace}}"},
				Permissions:      []string{"image_generation", "workspace_read", "workspace_write"},
				Status:           "available",
				Assets:           defaultReleaseAssets("Bradthebrad/nullbot-imagetools-mcp", "v0.1.1", "nullbot-imagetools-mcp"),
			},
			{
				ID:               "nullbot-web-mcp",
				Kind:             "mcp_server",
				Name:             "NullBot Web MCP",
				Description:      "Web search, safe URL fetching/readability, and Chromium browser automation through localhost CDP.",
				Repo:             "Bradthebrad/nullbot-web-mcp",
				ReleaseTag:       "v0.1.0",
				DefaultTransport: "stdio",
				DefaultArgs:      []string{"--workspace", "{{workspace}}"},
				Permissions:      []string{"network_fetch", "browser_control", "workspace_write"},
				Status:           "available",
				Assets:           defaultReleaseAssets("Bradthebrad/nullbot-web-mcp", "v0.1.0", "nullbot-web-mcp"),
			},
			{
				ID:          "api-probe",
				Kind:        "skill_pack",
				Name:        "API Probe Skill",
				Description: "Investigate websites/docs for APIs, auth methods, specs, SDK choices, and future MCP designs.",
				Repo:        "Bradthebrad/nullbot-skills",
				Permissions: []string{"skill_install"},
				Status:      "available",
				SkillFiles: []MarketSkillFile{{
					Name: "api-probe",
					Path: "skills/api-probe/SKILL.md",
					URL:  "https://raw.githubusercontent.com/Bradthebrad/nullbot-skills/main/skills/api-probe/SKILL.md",
				}},
			},
			{
				ID:          "mcp-skill",
				Kind:        "skill_pack",
				Name:        "MCP Skill",
				Description: "Design, build, document, release, and marketplace-register Go MCP servers.",
				Repo:        "Bradthebrad/nullbot-skills",
				Permissions: []string{"skill_install"},
				Status:      "available",
				SkillFiles: []MarketSkillFile{{
					Name: "mcp-skill",
					Path: "skills/mcp-skill/SKILL.md",
					URL:  "https://raw.githubusercontent.com/Bradthebrad/nullbot-skills/main/skills/mcp-skill/SKILL.md",
				}},
			},
		},
	}
}

func defaultReleaseAssets(repo, tag, base string) []MarketAsset {
	prefix := "https://github.com/" + repo + "/releases/download/" + tag + "/"
	warning := "UPX-compressed binaries can trigger antivirus or SmartScreen heuristics on Windows."
	return []MarketAsset{
		{Name: base + ".exe", Platform: "windows", Arch: "amd64", URL: prefix + base + ".exe"},
		{Name: base + "-small.exe", Platform: "windows", Arch: "amd64", URL: prefix + base + "-small.exe", Compressed: true, Warning: warning},
	}
}

func mergePackages(base, overlay MarketManifest) MarketManifest {
	byID := map[string]MarketPackage{}
	for _, pkg := range base.Packages {
		byID[pkg.ID] = pkg
	}
	for _, pkg := range overlay.Packages {
		existing := byID[pkg.ID]
		if existing.ID == "" {
			byID[pkg.ID] = pkg
			continue
		}
		pkg.Installed = existing.Installed || pkg.Installed
		pkg.Enabled = existing.Enabled || pkg.Enabled
		if pkg.InstallDir == "" {
			pkg.InstallDir = existing.InstallDir
		}
		if pkg.InstalledAsset == "" {
			pkg.InstalledAsset = existing.InstalledAsset
		}
		if pkg.InstalledAt == "" {
			pkg.InstalledAt = existing.InstalledAt
		}
		byID[pkg.ID] = pkg
	}
	out := base
	out.Packages = out.Packages[:0]
	for _, pkg := range byID {
		out.Packages = append(out.Packages, pkg)
	}
	if len(overlay.Sources) > 0 {
		out.Sources = overlay.Sources
	}
	return out
}

func mergeInstalledState(config Config, manifest MarketManifest) MarketManifest {
	for i := range manifest.Packages {
		pkg := &manifest.Packages[i]
		switch pkg.Kind {
		case "mcp_server":
			dir := filepath.Join(config.AppDir, "mcp", pkg.ID)
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				pkg.Installed = true
				pkg.InstallDir = dir
				if entry, ok := config.EnabledMCPServers[pkg.ID]; ok && entry.Enabled {
					pkg.Enabled = true
				}
			}
		case "skill_pack":
			allInstalled := len(pkg.SkillFiles) > 0
			for _, file := range pkg.SkillFiles {
				if _, err := os.Stat(filepath.Join(primarySkillDir(config), file.Name, "SKILL.md")); err != nil {
					allInstalled = false
				}
			}
			pkg.Installed = allInstalled
		}
		if pkg.Status == "" {
			pkg.Status = "available"
		}
	}
	return manifest
}

func RefreshMarket(ctx context.Context, config Config) (MarketManifest, error) {
	manifest, err := LoadMarketManifest(config)
	if err != nil {
		return MarketManifest{}, err
	}
	for i := range manifest.Packages {
		pkg := &manifest.Packages[i]
		if pkg.Repo == "" {
			continue
		}
		readme, _ := githubReadme(ctx, pkg.Repo)
		if readme != "" {
			pkg.Readme = truncate(readme, 6000)
		}
		if pkg.Kind != "mcp_server" {
			continue
		}
		release, err := githubLatestRelease(ctx, pkg.Repo)
		if err != nil {
			pkg.Error = err.Error()
			continue
		}
		pkg.ReleaseTag = release.TagName
		pkg.Assets = assetsFromRelease(release, pkg.ID)
		applyChecksums(pkg.Assets, release.Checksums)
		pkg.Status = "available"
		pkg.Error = ""
	}
	manifest = mergeInstalledState(config, manifest)
	return manifest, SaveMarketManifest(config, manifest)
}

type githubRelease struct {
	TagName   string        `json:"tag_name"`
	Assets    []githubAsset `json:"assets"`
	Body      string        `json:"body"`
	Checksums map[string]string
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func githubLatestRelease(ctx context.Context, repo string) (githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return githubRelease{}, fmt.Errorf("github release %s: %s", repo, resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, err
	}
	for _, asset := range release.Assets {
		if strings.EqualFold(asset.Name, "SHA256SUMS.txt") {
			release.Checksums = fetchChecksums(ctx, asset.BrowserDownloadURL)
			break
		}
	}
	return release, nil
}

func githubReadme(ctx context.Context, repo string) (string, error) {
	url := "https://raw.githubusercontent.com/" + repo + "/main/README.md"
	data, err := downloadBytes(ctx, url, 256*1024)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func fetchChecksums(ctx context.Context, url string) map[string]string {
	data, err := downloadBytes(ctx, url, 64*1024)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			out[fields[1]] = fields[0]
		}
	}
	return out
}

func assetsFromRelease(release githubRelease, pkgID string) []MarketAsset {
	var assets []MarketAsset
	for _, asset := range release.Assets {
		if !strings.HasSuffix(strings.ToLower(asset.Name), ".exe") {
			continue
		}
		assets = append(assets, MarketAsset{
			Name:       asset.Name,
			Platform:   "windows",
			Arch:       "amd64",
			URL:        asset.BrowserDownloadURL,
			Compressed: strings.Contains(strings.ToLower(asset.Name), "-small"),
			Warning:    upxWarning(asset.Name),
		})
	}
	if len(assets) == 0 {
		return defaultReleaseAssets("Bradthebrad/"+pkgID, release.TagName, pkgID)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	return assets
}

func upxWarning(name string) string {
	if strings.Contains(strings.ToLower(name), "-small") {
		return "UPX-compressed binaries can trigger antivirus or SmartScreen heuristics on Windows."
	}
	return ""
}

func applyChecksums(assets []MarketAsset, checksums map[string]string) {
	for i := range assets {
		if sum := checksums[assets[i].Name]; sum != "" {
			assets[i].SHA256 = sum
		}
	}
}

func InstallMarketPackage(ctx context.Context, config Config, packageID string, small bool) (MarketPackage, error) {
	manifest, err := LoadMarketManifest(config)
	if err != nil {
		return MarketPackage{}, err
	}
	pkg, idx, err := findMarketPackage(manifest, packageID)
	if err != nil {
		return MarketPackage{}, err
	}
	switch pkg.Kind {
	case "mcp_server":
		pkg, err = installMCPPackage(ctx, config, pkg, small)
	case "skill_pack":
		pkg, err = installSkillPackage(ctx, config, pkg)
	default:
		err = fmt.Errorf("unsupported package kind %q", pkg.Kind)
	}
	if err != nil {
		manifest.Packages[idx].Error = err.Error()
		_ = SaveMarketManifest(config, manifest)
		return MarketPackage{}, err
	}
	manifest.Packages[idx] = pkg
	if err := SaveMarketManifest(config, mergeInstalledState(config, manifest)); err != nil {
		return MarketPackage{}, err
	}
	return pkg, nil
}

func installMCPPackage(ctx context.Context, config Config, pkg MarketPackage, small bool) (MarketPackage, error) {
	asset, err := selectAsset(pkg, small)
	if err != nil {
		return pkg, err
	}
	dir := filepath.Join(config.AppDir, "mcp", pkg.ID)
	tmpDir := filepath.Join(marketDir(config), "tmp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return pkg, err
	}
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return pkg, err
	}
	tmp := filepath.Join(tmpDir, asset.Name+".download")
	if err := downloadFile(ctx, asset.URL, tmp, 250*1024*1024); err != nil {
		return pkg, err
	}
	if asset.SHA256 != "" {
		ok, err := verifySHA256(tmp, asset.SHA256)
		if err != nil {
			return pkg, err
		}
		if !ok {
			return pkg, fmt.Errorf("checksum mismatch for %s", asset.Name)
		}
	}
	target := filepath.Join(dir, asset.Name)
	if err := os.Rename(tmp, target); err != nil {
		if copyErr := copyFile(tmp, target); copyErr != nil {
			return pkg, err
		}
		_ = os.Remove(tmp)
	}
	pkg.Installed = true
	pkg.InstallDir = dir
	pkg.InstalledAsset = asset.Name
	pkg.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	pkg.Status = "installed"
	pkg.Error = ""
	_ = writeJSON(filepath.Join(dir, "package.json"), pkg)
	_ = writeJSON(filepath.Join(dir, "server.json"), mcpEntryForPackage(config, pkg, target))
	return pkg, nil
}

func installSkillPackage(ctx context.Context, config Config, pkg MarketPackage) (MarketPackage, error) {
	root := primarySkillDir(config)
	if err := os.MkdirAll(root, 0700); err != nil {
		return pkg, err
	}
	for _, file := range pkg.SkillFiles {
		if file.URL == "" || file.Name == "" {
			return pkg, fmt.Errorf("invalid skill file metadata for %s", pkg.ID)
		}
		data, err := downloadBytes(ctx, file.URL, 512*1024)
		if err != nil {
			return pkg, err
		}
		dir := filepath.Join(root, slugSkillName(file.Name))
		if err := os.MkdirAll(dir, 0700); err != nil {
			return pkg, err
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), data, 0600); err != nil {
			return pkg, err
		}
	}
	pkg.Installed = true
	pkg.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	pkg.Status = "installed"
	pkg.Error = ""
	return pkg, nil
}

func selectAsset(pkg MarketPackage, small bool) (MarketAsset, error) {
	var fallback *MarketAsset
	for i := range pkg.Assets {
		asset := &pkg.Assets[i]
		if asset.Platform != "" && asset.Platform != runtime.GOOS {
			continue
		}
		if fallback == nil {
			fallback = asset
		}
		if asset.Compressed == small {
			return *asset, nil
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	return MarketAsset{}, fmt.Errorf("no installable asset for %s", pkg.ID)
}

func findMarketPackage(manifest MarketManifest, id string) (MarketPackage, int, error) {
	for i, pkg := range manifest.Packages {
		if pkg.ID == id {
			return pkg, i, nil
		}
	}
	return MarketPackage{}, -1, fmt.Errorf("market package %q not found", id)
}

func EnableMCPServer(config Config, id string) (Config, error) {
	manifest, err := LoadMarketManifest(config)
	if err != nil {
		return config, err
	}
	pkg, _, err := findMarketPackage(manifest, id)
	if err != nil {
		return config, err
	}
	if !pkg.Installed {
		return config, fmt.Errorf("%s is not installed", id)
	}
	serverPath := filepath.Join(pkg.InstallDir, pkg.InstalledAsset)
	if pkg.InstalledAsset == "" {
		return config, fmt.Errorf("%s has no installed asset", id)
	}
	entry := mcpEntryForPackage(config, pkg, serverPath)
	entry.Enabled = true
	config.EnabledMCPServers[id] = entry
	if err := SaveConfig(config); err != nil {
		return config, err
	}
	manifest = setPackageEnabled(manifest, id, true)
	_ = SaveMarketManifest(config, manifest)
	return config, nil
}

func DisableMCPServer(config Config, id string) (Config, error) {
	if _, ok := config.EnabledMCPServers[id]; !ok {
		return config, fmt.Errorf("%s is not enabled", id)
	}
	delete(config.EnabledMCPServers, id)
	if err := SaveConfig(config); err != nil {
		return config, err
	}
	manifest, _ := LoadMarketManifest(config)
	manifest = setPackageEnabled(manifest, id, false)
	_ = SaveMarketManifest(config, manifest)
	return config, nil
}

func RemoveMCPServer(config Config, id string) (Config, error) {
	delete(config.EnabledMCPServers, id)
	dir := filepath.Join(config.AppDir, "mcp", id)
	if err := os.RemoveAll(dir); err != nil {
		return config, err
	}
	if err := SaveConfig(config); err != nil {
		return config, err
	}
	manifest, _ := LoadMarketManifest(config)
	for i := range manifest.Packages {
		if manifest.Packages[i].ID == id {
			manifest.Packages[i].Installed = false
			manifest.Packages[i].Enabled = false
			manifest.Packages[i].InstallDir = ""
			manifest.Packages[i].InstalledAsset = ""
			manifest.Packages[i].InstalledAt = ""
			manifest.Packages[i].Status = "available"
		}
	}
	_ = SaveMarketManifest(config, manifest)
	return config, nil
}

func setPackageEnabled(manifest MarketManifest, id string, enabled bool) MarketManifest {
	for i := range manifest.Packages {
		if manifest.Packages[i].ID == id {
			manifest.Packages[i].Enabled = enabled
			if enabled {
				manifest.Packages[i].Status = "enabled"
			} else if manifest.Packages[i].Installed {
				manifest.Packages[i].Status = "installed"
			}
		}
	}
	return manifest
}

func mcpEntryForPackage(config Config, pkg MarketPackage, command string) MCPEntry {
	return MCPEntry{
		Name:      pkg.Name,
		Command:   command,
		Args:      expandDefaultArgs(config, pkg.DefaultArgs),
		Transport: defaultString(pkg.DefaultTransport, "stdio"),
		Enabled:   pkg.Enabled,
	}
}

func expandDefaultArgs(config Config, args []string) []string {
	out := append([]string{}, args...)
	workspace := config.WorkspaceDir
	if strings.TrimSpace(workspace) == "" {
		workspace = defaultWorkspaceDir()
	}
	for i, arg := range out {
		if arg == "{{workspace}}" {
			out[i] = workspace
		}
	}
	return out
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func downloadBytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func downloadFile(ctx context.Context, url, path string, limit int64) error {
	data, err := downloadBytes(ctx, url, limit)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func verifySHA256(path, expected string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	return strings.EqualFold(got, expected), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func MarketSummary(manifest MarketManifest) string {
	if len(manifest.Packages) == 0 {
		return "No market packages found."
	}
	var b strings.Builder
	for _, pkg := range manifest.Packages {
		state := pkg.Status
		if pkg.Enabled {
			state = "enabled"
		} else if pkg.Installed {
			state = "installed"
		}
		fmt.Fprintf(&b, "- %s (%s, %s): %s\n", pkg.ID, pkg.Kind, state, pkg.Description)
	}
	return strings.TrimSpace(b.String())
}
