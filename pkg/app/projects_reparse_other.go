//go:build !windows

package app

// Other platforms rely on canonicalPath's EvalSymlinks resolution.
func rejectProjectReparsePoints(path string) error { return nil }
