package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/Bradthebrad/nullbot/pkg/hfhub"
	"github.com/Bradthebrad/nullbot/pkg/localmodels"
)

// Installing local models: search Hugging Face for GGUF builds, then put
// one where a runtime will find it: pulled into an Ollama server, saved into
// LM Studio's models folder, or saved into NullBot's own models folder for
// llama.cpp.

// HFModel is a Hugging Face search result.
type HFModel struct {
	ID           string   `json:"id"`
	Author       string   `json:"author,omitempty"`
	Pipeline     string   `json:"pipeline,omitempty"`
	Downloads    int64    `json:"downloads"`
	Likes        int64    `json:"likes"`
	LastModified string   `json:"last_modified,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	NeedsToken   bool     `json:"needs_token,omitempty"`
}

// HFBuild is one installable GGUF build in a repo.
type HFBuild struct {
	Name        string   `json:"name"`
	Quant       string   `json:"quant,omitempty"`
	Size        int64    `json:"size"`
	Files       []string `json:"files"`
	Parts       int      `json:"parts"`
	Complete    bool     `json:"complete"`
	Projector   bool     `json:"projector,omitempty"`
	Recommended bool     `json:"recommended,omitempty"`
}

// InstallTarget is where a model goes.
type InstallTarget string

const (
	InstallOllama   InstallTarget = "ollama"   // pulled into an Ollama server
	InstallLMStudio InstallTarget = "lmstudio" // saved into LM Studio's models folder
	InstallFiles    InstallTarget = "files"    // saved into NullBot's models folder
)

// InstallRequest says what to install and where.
type InstallRequest struct {
	Repo   string        `json:"repo"`
	Build  string        `json:"build,omitempty"` // a build's name; empty picks one
	Quant  string        `json:"quant,omitempty"`
	Target InstallTarget `json:"target"`
	Server string        `json:"server,omitempty"` // the Ollama server's id
}

// InstallProgress is reported while a model installs.
type InstallProgress struct {
	Status string `json:"status"`
	File   string `json:"file,omitempty"`
	Done   int64  `json:"done,omitempty"`
	Total  int64  `json:"total,omitempty"`
}

// InstallResult is what an install made.
type InstallResult struct {
	Target InstallTarget `json:"target"`
	// Model is the name to choose: an Ollama tag, or a file path.
	Model string   `json:"model"`
	Paths []string `json:"paths,omitempty"`
	// Next says how to start using it, when NullBot cannot do it itself.
	Next string `json:"next,omitempty"`
}

// hfTokenFrom finds a Hugging Face token in the vault: an API key secret
// for provider huggingface (or hf), or a generic secret giving HF_TOKEN.
func hfTokenFrom(payload vaultPayload) string {
	for _, s := range payload.Secrets {
		switch {
		case s.Kind == SecretAPIKey && (s.Public["provider"] == "huggingface" || s.Public["provider"] == "hf"):
			return s.Values["value"]
		case s.Kind == SecretGeneric && strings.EqualFold(s.Public["env"], "HF_TOKEN"):
			return s.Values["value"]
		}
	}
	return ""
}

// hfClientFunc builds the Hugging Face client; tests point it elsewhere.
var hfClientFunc = func(config Config) *hfhub.Client {
	client := &hfhub.Client{UserAgent: "NullBot"}
	if payload, err := loadSecretsPayload(config); err == nil {
		client.Token = hfTokenFrom(payload)
	}
	return client
}

// SearchHFModels searches Hugging Face for GGUF models.
func (a *App) SearchHFModels(ctx context.Context, query, sort string, limit int) ([]HFModel, error) {
	results, err := hfClientFunc(a.Config()).Search(ctx, hfhub.SearchOptions{Query: strings.TrimSpace(query), Filter: []string{"gguf"}, Sort: sort, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]HFModel, 0, len(results))
	for _, m := range results {
		info := HFModel{ID: m.ID, Author: m.Author, Pipeline: m.PipelineTag, Downloads: m.Downloads, Likes: m.Likes, NeedsToken: m.NeedsToken()}
		if !m.LastModified.IsZero() {
			info.LastModified = m.LastModified.Format("2006-01-02")
		}
		for _, t := range m.Tags {
			if len(info.Tags) < 12 && !strings.Contains(t, ":") {
				info.Tags = append(info.Tags, t)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// HFBuilds lists a repo's GGUF builds, smallest first, marking the one
// NullBot would pick.
func (a *App) HFBuilds(ctx context.Context, repo string) ([]HFBuild, error) {
	models, err := a.hfBuilds(ctx, repo)
	if err != nil {
		return nil, err
	}
	pick, picked := hfhub.Recommend(models, hfhub.Preference{})
	out := make([]HFBuild, 0, len(models))
	for _, m := range models {
		b := HFBuild{Name: m.Name, Quant: m.Quant, Size: m.Size, Parts: m.Parts, Complete: m.Complete, Projector: m.Projector}
		for _, f := range m.Files {
			b.Files = append(b.Files, f.Path)
		}
		b.Recommended = picked && m.Name == pick.Name
		out = append(out, b)
	}
	return out, nil
}

func (a *App) hfBuilds(ctx context.Context, repo string) ([]hfhub.GGUFModel, error) {
	if err := hfhub.ValidateRepoID(repo); err != nil {
		return nil, err
	}
	files, err := hfClientFunc(a.Config()).Files(ctx, repo, "")
	if err != nil {
		return nil, err
	}
	models := hfhub.GroupGGUF(files)
	if len(models) == 0 {
		return nil, fmt.Errorf("%s has no GGUF files; local runtimes need a GGUF build (search for one with gguf in its name)", repo)
	}
	return models, nil
}

// ModelsDir is NullBot's own folder for model files.
func ModelsDir(config Config) string { return filepath.Join(config.AppDir, "models") }

// lmStudioModelsDir is where LM Studio looks for models: ~/.lmstudio/models
// in current versions, ~/.cache/lm-studio/models in older ones.
func lmStudioModelsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, dir := range []string{filepath.Join(home, ".lmstudio", "models"), filepath.Join(home, ".cache", "lm-studio", "models")} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}
	return "", errors.New("LM Studio's models folder was not found; open LM Studio once, or save the model to NullBot's folder instead")
}

// InstallModel installs a GGUF build from Hugging Face.
func (a *App) InstallModel(ctx context.Context, req InstallRequest, progress func(InstallProgress)) (InstallResult, error) {
	if progress == nil {
		progress = func(InstallProgress) {}
	}
	if err := hfhub.ValidateRepoID(req.Repo); err != nil {
		return InstallResult{}, err
	}
	models, err := a.hfBuilds(ctx, req.Repo)
	if err != nil {
		return InstallResult{}, err
	}
	var build hfhub.GGUFModel
	found := false
	if req.Build != "" {
		for _, m := range models {
			if m.Name == req.Build {
				build, found = m, true
			}
		}
		if !found {
			return InstallResult{}, fmt.Errorf("%s has no build %s", req.Repo, req.Build)
		}
	} else if build, found = hfhub.Recommend(models, hfhub.Preference{Quant: req.Quant}); !found {
		return InstallResult{}, fmt.Errorf("%s has no complete GGUF build to install", req.Repo)
	} else if q := strings.TrimSpace(req.Quant); q != "" && !strings.EqualFold(build.Quant, q) {
		// Recommend falls back to the nearest quantization; an explicit
		// choice is not changed behind the user's back.
		var have []string
		for _, m := range models {
			if m.Complete && !m.Projector && m.Quant != "" && !slices.Contains(have, m.Quant) {
				have = append(have, m.Quant)
			}
		}
		return InstallResult{}, fmt.Errorf("%s has no %s build; it has %s", req.Repo, q, strings.Join(have, ", "))
	}
	if !build.Complete || build.Projector {
		return InstallResult{}, fmt.Errorf("%s is not a complete model build", build.Name)
	}
	a.logInfo("model install started", "repo", req.Repo, "build", build.Name, "target", req.Target)

	switch req.Target {
	case InstallOllama:
		if build.Quant == "" {
			return InstallResult{}, fmt.Errorf("Ollama pulls a Hugging Face build by its quantization, and %s names none; save it to a folder instead", build.Name)
		}
		if len(build.Files) > 1 {
			return InstallResult{}, fmt.Errorf("%s is split into %d files, which Ollama cannot pull from Hugging Face; choose a build in one file, or save it to a folder and serve it with llama.cpp", build.Name, len(build.Files))
		}
		server := req.Server
		if server == "" {
			for _, e := range a.Config().LocalEndpoints {
				if e.Kind == string(localmodels.Ollama) && !e.Disabled {
					server = e.ID
					break
				}
			}
		}
		if server == "" {
			return InstallResult{}, errors.New("no Ollama server is set up; add one in Settings > Models")
		}
		name := "hf.co/" + req.Repo + ":" + build.Quant
		err := a.PullLocalModel(ctx, server, name, func(p localmodels.Progress) {
			progress(InstallProgress{Status: p.Status, Done: p.Completed, Total: p.Total})
		})
		if err != nil {
			return InstallResult{}, err
		}
		return InstallResult{Target: InstallOllama, Model: name}, nil

	case InstallLMStudio, InstallFiles:
		var dir string
		if req.Target == InstallLMStudio {
			if dir, err = lmStudioModelsDir(); err != nil {
				return InstallResult{}, err
			}
		} else {
			dir = ModelsDir(a.Config())
		}
		// LM Studio reads models from <publisher>/<repo>/<file>.
		dir = filepath.Join(dir, filepath.FromSlash(req.Repo))
		client := hfClientFunc(a.Config())
		var paths []string
		for _, f := range build.Files {
			res, err := client.Download(ctx, hfhub.DownloadRequest{
				Repo: req.Repo, Path: f.Path, Dir: dir, Name: filepath.Base(f.Path), SHA256: f.SHA256, Size: f.Size,
				// A cancelled download continues where it stopped next time.
				KeepPartial: true,
				Progress: func(p hfhub.Progress) {
					progress(InstallProgress{Status: "downloading", File: p.Name, Done: p.Done, Total: p.Total})
				},
			})
			if err != nil {
				return InstallResult{}, err
			}
			paths = append(paths, res.Path)
		}
		result := InstallResult{Target: req.Target, Model: paths[0], Paths: paths}
		if req.Target == InstallLMStudio {
			result.Next = "LM Studio lists it now; load it there, and refresh the model list in NullBot."
		} else {
			bin := "llama-server"
			if runtime.GOOS == "windows" {
				bin = "llama-server.exe"
			}
			result.Next = fmt.Sprintf("Serve it with llama.cpp: %s -m %q --jinja -c 32768, then add the server in Settings > Models.", bin, paths[0])
		}
		a.logInfo("model installed", "repo", req.Repo, "build", build.Name, "target", req.Target)
		return result, nil
	}
	return InstallResult{}, fmt.Errorf("choose where to install it: ollama, lmstudio or files")
}
