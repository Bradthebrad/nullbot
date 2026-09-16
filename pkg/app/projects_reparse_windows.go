//go:build windows

package app

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// rejectProjectReparsePoints checks ancestors first, including those above a
// configured root. Missing leaves are allowed, but their existing ancestors must
// still be checked. Reject every reparse tag, not just symlinks and junctions.
// Like the enclosing policy check, this is not protection against filesystem races.
func rejectProjectReparsePoints(path string) error {
	var components []string
	for {
		components = append(components, path)
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	for i := len(components) - 1; i >= 0; i-- {
		component := components[i]
		name, err := windows.UTF16PtrFromString(component)
		if err != nil {
			return fmt.Errorf("inspect project path %q: %w", component, err)
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect project path %q: %w", component, err)
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("project path contains a reparse point: %s", component)
		}
	}
	return nil
}
