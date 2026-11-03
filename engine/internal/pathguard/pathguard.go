// Package pathguard confines file access to a project root. Every path is
// resolved through symlinks and must stay inside the root.
package pathguard

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Root is a project root with symlinks resolved.
type Root struct {
	real string
}

// NewRoot resolves dir to an absolute, symlink-free directory.
func NewRoot(dir string) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("project root: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("project root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("project root %s is not a directory", real)
	}
	return &Root{real: real}, nil
}

// Path is the resolved root directory.
func (r *Root) Path() string { return r.real }

// Within reports whether p is root or lies below it. Both must be cleaned,
// symlink-free paths.
func Within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ResolveExisting resolves p, relative to the root or absolute, to the real
// path of an existing regular file inside the root. Errors name the path the
// caller supplied and never include file contents.
func (r *Root) ResolveExisting(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path contains a NUL byte")
	}
	joined := p
	if !filepath.IsAbs(p) {
		joined = filepath.Join(r.real, p)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(joined))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if !Within(r.real, filepath.Clean(joined)) {
				return "", fmt.Errorf("%s is outside the project root", p)
			}
			return "", fmt.Errorf("%s does not exist", p)
		}
		return "", fmt.Errorf("%s: cannot be resolved", p)
	}
	if !Within(r.real, real) {
		return "", fmt.Errorf("%s is outside the project root", p)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("%s: cannot be read", p)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", p)
	}
	return real, nil
}

// ReadFile resolves p with ResolveExisting and reads it, refusing a file larger
// than maxBytes. The size is enforced on the bytes read, not on the stat.
func (r *Root) ReadFile(p string, maxBytes int64) ([]byte, error) {
	real, err := r.ResolveExisting(p)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, fmt.Errorf("%s: cannot be opened", p)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: cannot be read", p)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, maxBytes)
	}
	return data, nil
}

// ResolveDestination returns the real path rel (relative to realRoot, forward
// slashes) would be written to. It follows existing directory symlinks, refuses
// any that resolve outside realRoot, refuses an existing non-directory in the
// path, and refuses an existing destination that is not a regular file.
func ResolveDestination(realRoot, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	cur := realRoot
	for i, part := range parts {
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(append([]string{cur}, parts[i:]...)...), nil
		}
		if err != nil {
			return "", err
		}
		last := i == len(parts)-1
		if info.Mode()&os.ModeSymlink != 0 {
			if last {
				return "", fmt.Errorf("%s is a symlink; refusing to overwrite it", next)
			}
			resolved, err := filepath.EvalSymlinks(next)
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("%s is a symlink whose target does not exist yet; create the target directory or remove the link, then re-run", next)
			}
			if err != nil {
				return "", fmt.Errorf("%s: %w", next, err)
			}
			if !Within(realRoot, resolved) {
				return "", fmt.Errorf("%s resolves to %s, outside %s; refusing to install through it", next, resolved, realRoot)
			}
			next = resolved
			if info, err = os.Stat(next); err != nil {
				return "", err
			}
		}
		if last {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%s exists and is not a regular file", next)
			}
		} else if !info.IsDir() {
			return "", fmt.Errorf("%s exists and is not a directory", next)
		}
		cur = next
	}
	return cur, nil
}
