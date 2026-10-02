package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CleanFolder resolves a folder to the absolute, cleaned, symlink-resolved
// path. Case is preserved exactly as the filesystem reports it, because this
// string is what the operator sees in the banner and the status bar.
func CleanFolder(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = filepath.Clean(path)
	}
	return filepath.Clean(resolveExistingPrefix(absolute))
}

// resolveExistingPrefix resolves symlinks in the longest existing ancestor of a
// path and re-appends the components that do not exist yet.
//
// A path that does not exist cannot be resolved directly, and its unresolved
// tail would then compare unequal to its own resolved parent: on macOS /var is
// a symlink to /private/var, and on Windows a short name like RUNNER~1 differs
// from the long name the filesystem reports. Either would make a folder under a
// trusted root look untrusted until it is created.
func resolveExistingPrefix(absolute string) string {
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	current := absolute
	suffix := ""
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return absolute
		}
		suffix = filepath.Join(filepath.Base(current), suffix)
		current = parent
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, suffix)
		}
	}
}

// TrustKey is the string two folders are compared by.
//
// On Windows the comparison folds case because the filesystem does, while the
// stored and displayed folder keeps its real casing. Folding the display value
// instead would show the operator a path that is not the one on disk.
func TrustKey(path string) string {
	cleaned := CleanFolder(path)
	if cleaned == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func samePath(a, b string) bool {
	key := TrustKey(a)
	return key != "" && key == TrustKey(b)
}

// IsTrusted reports whether the folder (or one of its parents) has been
// trusted. Trusting a project root covers everything under it, which is what
// lets a nested package folder inherit the decision.
func (c Config) IsTrusted(folder string) bool {
	target := TrustKey(folder)
	if target == "" {
		return false
	}
	for _, trusted := range c.TrustedFolders {
		key := TrustKey(trusted)
		if key == "" {
			continue
		}
		if key == target || strings.HasPrefix(target, key+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Trust records a folder, returning a copy so callers can persist it. The
// folder is stored with its real casing.
func (c Config) Trust(folder string) Config {
	cleaned := CleanFolder(folder)
	if cleaned == "" {
		return c
	}
	for _, trusted := range c.TrustedFolders {
		if samePath(trusted, cleaned) {
			return c
		}
	}
	c.TrustedFolders = append(append([]string{}, c.TrustedFolders...), cleaned)
	return c
}

// Untrust removes a folder from the trust list.
func (c Config) Untrust(folder string) Config {
	key := TrustKey(folder)
	kept := make([]string, 0, len(c.TrustedFolders))
	for _, trusted := range c.TrustedFolders {
		if TrustKey(trusted) != key {
			kept = append(kept, trusted)
		}
	}
	c.TrustedFolders = kept
	return c
}

// FolderExists reports whether a path is an existing directory, which the
// trust prompt uses before offering to trust it.
func FolderExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
