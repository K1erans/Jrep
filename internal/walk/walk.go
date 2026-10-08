// Package walk lists the files jrep searches. Inside a git repo it asks git,
// so ignore rules match git exactly; elsewhere it reads .gitignore files.
package walk

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// binarySniffLen is how much of a file is checked for a NUL byte.
const binarySniffLen = 8 << 10

type Options struct {
	NoIgnore    bool  // also list gitignored files
	MaxFileSize int64 // skip larger files; 0 means no limit
}

type File struct {
	Path string // as shown to the user, relative to the working directory
	Rel  string // slash path from the repo root; stable across working directories
	Size int64
}

type Result struct {
	Root     string // git top-level folder, or the first search path outside git
	Files    []File // sorted by Path
	Oversize []File // text files skipped for being over MaxFileSize
}

// Walk lists text files under paths. Named files are always included, even
// if ignored; directories follow .gitignore unless opts.NoIgnore is set.
func Walk(paths []string, opts Options) (Result, error) {
	var res Result
	if len(paths) == 0 {
		paths = []string{"."}
	}
	root, err := Root(paths[0])
	if err != nil {
		return res, err
	}
	res.Root = root

	seen := map[string]bool{}
	add := func(display string) error {
		display = filepath.Clean(display)
		if seen[display] {
			return nil
		}
		seen[display] = true
		f, ok, err := inspect(display)
		if err != nil || !ok {
			return err
		}
		f.Rel = relTo(root, display)
		if f.Size > opts.MaxFileSize && opts.MaxFileSize > 0 {
			res.Oversize = append(res.Oversize, f)
		} else {
			res.Files = append(res.Files, f)
		}
		return nil
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return res, err
		}
		if !info.IsDir() {
			if err := add(p); err != nil {
				return res, err
			}
			continue
		}
		var list []string
		if gitTop(p) != "" && !opts.NoIgnore {
			list, err = gitFiles(p)
		} else {
			list, err = walkDir(p, !opts.NoIgnore)
		}
		if err != nil {
			return res, err
		}
		for _, f := range list {
			if err := add(f); err != nil {
				return res, err
			}
		}
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })
	sort.Slice(res.Oversize, func(i, j int) bool { return res.Oversize[i].Path < res.Oversize[j].Path })
	return res, nil
}

// Root is the git top-level folder containing path, or path itself (its
// folder, for a file) outside git. Symlinks are resolved.
func Root(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	dir := path
	if !info.IsDir() {
		dir = filepath.Dir(path)
	}
	if top := gitTop(dir); top != "" {
		return top, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func gitTop(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	top := strings.TrimSpace(string(out))
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		return resolved
	}
	return top
}

// gitFiles lists tracked and untracked-but-not-ignored files under dir.
func gitFiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %v: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			files = append(files, filepath.Join(dir, filepath.FromSlash(name)))
		}
	}
	return files, nil
}

// walkDir lists every file under dir except .git folders, applying
// .gitignore files found along the way when ignore is set.
func walkDir(dir string, ignore bool) ([]string, error) {
	var files []string
	rules := map[string]*ruleSet{} // folder → rules in effect there
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil // unreadable entries are skipped, as git does
		}
		parent := rules[filepath.Dir(p)]
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if p != dir && parent.ignored(p, true) {
				return filepath.SkipDir
			}
			rs := parent
			if ignore {
				rs = parent.with(p)
			}
			rules[p] = rs
			return nil
		}
		if !parent.ignored(p, false) {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

// inspect stats a file and reports whether it is a searchable text file.
func inspect(path string) (File, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, false, nil // e.g. tracked by git but deleted
	}
	if err != nil {
		return File{}, false, err
	}
	if !info.Mode().IsRegular() {
		return File{}, false, nil
	}
	fh, err := os.Open(path)
	if err != nil {
		return File{}, false, err
	}
	defer fh.Close()
	head := make([]byte, binarySniffLen)
	n, err := io.ReadFull(fh, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return File{}, false, err
	}
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return File{}, false, nil
	}
	return File{Path: path, Size: info.Size()}, true, nil
}

func relTo(root, path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}
