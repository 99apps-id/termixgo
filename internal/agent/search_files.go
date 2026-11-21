package agent

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/search"
)

// listSearchFiles returns the files a search visits under root, as absolute
// paths. Inside a git repository it asks git, which applies every .gitignore,
// .git/info/exclude and the global excludes file, so build output, generated
// code and logs the project ignores are not read and do not flood results.
// Outside one, or when git is missing, it walks the tree and skips the shared
// list of dependency and build directories.
func listSearchFiles(ctx context.Context, root string) []string {
	info, err := os.Stat(root)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		return []string{root}
	}
	if files, ok := gitListFiles(ctx, root); ok {
		return files
	}
	var files []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if path != root && search.IsSkippedDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files
}

// gitListFiles lists tracked and untracked-but-not-ignored files under root.
func gitListFiles(ctx context.Context, root string) ([]string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	output, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var files []string
	seen := map[string]bool{}
	for _, raw := range bytes.Split(output, []byte{0}) {
		relative := string(raw)
		if relative == "" || seen[relative] {
			continue
		}
		seen[relative] = true
		if inSkippedDir(relative) {
			continue
		}
		files = append(files, filepath.Join(root, filepath.FromSlash(relative)))
	}
	return files, true
}

// inSkippedDir reports a path under a directory no search enters, which keeps
// a committed vendor/ or the Termixgo state directory out of git results too.
func inSkippedDir(relative string) bool {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for _, part := range parts[:len(parts)-1] {
		if search.IsSkippedDir(part) {
			return true
		}
	}
	return false
}

// noiseFileNames and noiseSuffixes are files a content search skips unless
// asked: lockfiles, minified bundles and source maps match almost any
// identifier and are never where the code to change lives.
var noiseFileNames = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "bun.lockb": true,
	"go.sum": true, "cargo.lock": true, "poetry.lock": true, "composer.lock": true,
	"gemfile.lock": true, "pipfile.lock": true, "uv.lock": true, "mix.lock": true,
}

var noiseSuffixes = []string{".min.js", ".min.css", ".map", ".min.mjs", ".bundle.js", ".chunk.js", ".svg"}

func isNoiseFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	if noiseFileNames[name] {
		return true
	}
	for _, suffix := range noiseSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// globMatcher compiles a glob once; matchGlob compiled it again for every
// file of the walk.
type globMatcher struct {
	compiled *regexp.Regexp
	baseOnly bool
}

func newGlobMatcher(pattern string) (*globMatcher, error) {
	compiled, err := regexp.Compile("^" + globToRegex(pattern) + "$")
	if err != nil {
		return nil, err
	}
	return &globMatcher{compiled: compiled, baseOnly: !strings.Contains(pattern, "/")}, nil
}

func (g *globMatcher) match(path string) bool {
	if g.compiled.MatchString(path) {
		return true
	}
	return g.baseOnly && g.compiled.MatchString(filepath.Base(path))
}
