package hfhub

import (
	"net/url"
	"path/filepath"
	"strings"
)

// ValidateRepoID checks that id is a Hugging Face model id of the form
// owner/name, using the Hub's own rules: each part is 1 to 96 letters,
// digits, '.', '_' or '-', starts and ends with a letter, digit or '_', and
// contains neither "--" nor "..". Checking before building a URL keeps ids
// from smuggling extra path segments or queries into requests.
func ValidateRepoID(id string) error {
	owner, name, ok := strings.Cut(id, "/")
	if !ok || !validRepoPart(owner) || !validRepoPart(name) {
		return newError(ErrInvalidRepo, "%q is not a Hugging Face model id; use the form owner/name, for example Qwen/Qwen3-8B-GGUF", id)
	}
	return nil
}

func validRepoPart(s string) bool {
	if len(s) == 0 || len(s) > 96 || strings.Contains(s, "--") || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		word := c == '_' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
		if word {
			continue
		}
		if (c == '-' || c == '.') && i > 0 && i < len(s)-1 {
			continue
		}
		return false
	}
	return true
}

// ValidateRevision checks a branch, tag or commit name. Hub revisions such
// as "refs/pr/3" contain slashes; they are escaped as one path segment when
// sent, so only plain characters are allowed and ".." never is.
func ValidateRevision(rev string) error {
	bad := len(rev) == 0 || len(rev) > 255 || strings.Contains(rev, "..")
	for _, seg := range strings.Split(rev, "/") {
		if bad {
			break
		}
		bad = seg == "" || seg[0] == '.' || seg[0] == '-'
		for i := 0; i < len(seg) && !bad; i++ {
			c := seg[i]
			bad = !(c == '_' || c == '-' || c == '.' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z'))
		}
	}
	if bad {
		return newError(ErrInvalidRevision, "%q is not a valid revision; use a branch, tag or commit such as main", rev)
	}
	return nil
}

func repoAndRevision(repo, rev string) (string, error) {
	if err := ValidateRepoID(repo); err != nil {
		return "", err
	}
	if rev == "" {
		rev = DefaultRevision
	}
	if err := ValidateRevision(rev); err != nil {
		return "", err
	}
	return rev, nil
}

// checkRepoPath checks a file path inside a repo before it goes into a URL.
// Repo files may have spaces and other characters, which are escaped, but
// the path must be relative and free of "." and ".." segments so it cannot
// reach outside the resolve endpoint.
func checkRepoPath(p string) error {
	bad := p == "" || len(p) > 1024 || strings.HasPrefix(p, "/") || strings.Contains(p, `\`)
	for _, seg := range strings.Split(p, "/") {
		if bad {
			break
		}
		bad = seg == "" || seg == "." || seg == ".."
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				bad = true
			}
		}
	}
	if bad {
		return newError(ErrUnsafePath, "%q is not a valid file path inside a Hugging Face repo", p)
	}
	return nil
}

// escapeRepoPath escapes each segment of a checked repo path for a URL.
func escapeRepoPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// windowsReserved are device names Windows treats specially in any folder,
// with or without an extension.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
	"COM0": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT0": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// checkLocalName checks the name a file will get inside the destination
// folder. File names come from a remote listing, so the rules are the same
// on every OS: a name that would be dangerous on Windows (a drive letter, a
// backslash, a device name) is refused on Linux too, so a folder copied
// between machines stays safe. os.Root then enforces the folder boundary
// again at write time, which also covers symlinks.
func checkLocalName(name string) error {
	bad := name == "" || len(name) > 1024 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\:`)
	for _, seg := range strings.Split(name, "/") {
		if bad {
			break
		}
		bad = seg == "" || seg == "." || seg == ".." ||
			strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") || // Windows silently drops these
			strings.ContainsAny(seg, `<>"|?*`)
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				bad = true
			}
		}
		stem, _, _ := strings.Cut(seg, ".")
		if windowsReserved[strings.ToUpper(strings.TrimSpace(stem))] {
			bad = true
		}
	}
	if !bad && !filepath.IsLocal(filepath.FromSlash(name)) {
		bad = true
	}
	if bad {
		return newError(ErrUnsafePath, "refusing to save %q: file names must stay inside the destination folder (no \"..\", absolute paths, drive letters or backslashes)", name)
	}
	return nil
}
