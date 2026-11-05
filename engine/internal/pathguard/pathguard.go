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
	"runtime"
	"strings"
	"syscall"
)

// Root is a project root. Files are opened through an os.Root on its
// resolved directory, so every path component is resolved inside the root at
// open time: a symlink swapped in after a check cannot lead out.
type Root struct {
	real  string // symlink-free absolute path
	given string // absolute path as supplied, accepted as an alias of real
	fs    *os.Root
}

// NewRoot resolves dir to an absolute, symlink-free directory and opens it.
// Close the root when done.
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
	fsRoot, err := os.OpenRoot(real)
	if err != nil {
		return nil, fmt.Errorf("project root: %w", err)
	}
	return &Root{real: real, given: abs, fs: fsRoot}, nil
}

// Close releases the root directory handle.
func (r *Root) Close() error { return r.fs.Close() }

// Path is the resolved root directory.
func (r *Root) Path() string { return r.real }

// RefuseAbsoluteSymlinks checks every component of rel (relative to realRoot,
// forward slashes) that exists and refuses an absolute symlink, inside the root
// or not. A writer that opens files through an os.Root needs this: os.Root
// refuses absolute links, so they must be rejected while planning, with a
// message that says what to change.
func RefuseAbsoluteSymlinks(realRoot, rel string) error {
	cur := realRoot
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(cur)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) {
			return fmt.Errorf("%s is an absolute symlink; use a relative link", cur)
		}
		// A relative link: continue from where it points.
		resolved, err := filepath.EvalSymlinks(cur)
		if err != nil {
			return nil // a dangling link is reported by ResolveDestination
		}
		cur = resolved
	}
	return nil
}

// Within reports whether p is root or lies below it. Both must be cleaned,
// symlink-free paths.
func Within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ErrUnreadable is the one error for a path that cannot be read inside the
// root, whatever the cause (outside, missing, a bad link, no permission), so
// the message cannot reveal whether a path outside the root exists.
type ErrUnreadable struct{ Path string }

func (e *ErrUnreadable) Error() string {
	return fmt.Sprintf("%s cannot be read inside the project root", e.Path)
}

// relative returns p, relative to the root or absolute, as a clean path
// relative to the root, judged lexically before any file is touched.
func (r *Root) relative(p string) (string, bool) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", false
	}
	joined := p
	if !filepath.IsAbs(p) {
		joined = filepath.Join(r.real, p)
	}
	joined = filepath.Clean(joined)
	for _, base := range []string{r.real, r.given} {
		if Within(base, joined) {
			rel, err := filepath.Rel(base, joined)
			if err != nil || rel == "." {
				return "", false
			}
			return rel, true
		}
	}
	return "", false
}

// Open opens the regular file p (relative to the root or absolute inside it)
// for reading. Every failure is *ErrUnreadable, except a path that resolves to
// something other than a regular file, which says so.
func (r *Root) Open(p string) (*os.File, error) {
	rel, ok := r.relative(p)
	if !ok {
		return nil, &ErrUnreadable{Path: p}
	}
	f, err := r.fs.OpenFile(rel, os.O_RDONLY|nonBlock, 0)
	if err != nil {
		return nil, &ErrUnreadable{Path: p}
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, &ErrUnreadable{Path: p}
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	return f, nil
}

// ReadFile reads the regular file p, refusing one larger than maxBytes. The
// size is enforced on the bytes read, not on the stat.
func (r *Root) ReadFile(p string, maxBytes int64) ([]byte, error) {
	f, err := r.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, &ErrUnreadable{Path: p}
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, maxBytes)
	}
	return data, nil
}

// ReadExisting reads rel (relative to the root, forward slashes) when it
// exists. It reports its permission bits; a missing file is (nil, 0, false, nil).
func (r *Root) ReadExisting(rel string, maxBytes int64) (data []byte, mode fs.FileMode, exists bool, err error) {
	f, err := r.fs.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|nonBlock, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("%s: cannot be read", rel)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("%s exists and is not a regular file", rel)
	}
	data, err = io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, 0, false, fmt.Errorf("%s: cannot be read in full", rel)
	}
	return data, info.Mode().Perm(), true, nil
}

// WriteAtomic replaces rel (relative to the root, forward slashes) with data
// through a temporary file in the same directory: the file is synced, renamed
// into place and the directory synced. An existing file keeps mode; a new one
// gets 0644. Parents are created. Every component resolves inside the root.
func (r *Root) WriteAtomic(rel string, data []byte, mode fs.FileMode, exists bool) error {
	rel = filepath.FromSlash(rel)
	dir := filepath.Dir(rel)
	if err := r.fs.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !exists {
		mode = 0o644
	}
	var name string
	var tmp *os.File
	for i := 0; ; i++ {
		name = filepath.Join(dir, fmt.Sprintf(".attest-tmp-%d-%d", os.Getpid(), i))
		var err error
		tmp, err = r.fs.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i > 100 {
			return err
		}
	}
	fail := func(err error) error {
		tmp.Close()
		_ = r.fs.Remove(name) // best-effort temp cleanup; the failure being returned takes precedence
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = r.fs.Remove(name) // best-effort temp cleanup; the failure being returned takes precedence
		return err
	}
	if err := r.fs.Rename(name, rel); err != nil {
		_ = r.fs.Remove(name) // best-effort temp cleanup; the failure being returned takes precedence
		return err
	}
	return r.syncDir(dir)
}

func (r *Root) syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := r.fs.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
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
