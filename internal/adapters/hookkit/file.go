package hookkit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// File is a settings file as read before an edit.
type File struct {
	Path   string      // the path agents read (may be a symlink)
	Real   string      // where the bytes live: symlinks resolved
	Data   []byte      // nil when the file does not exist
	Mode   fs.FileMode // permission bits to keep
	Exists bool
}

// ReadFile reads path, following symlinks so a dotfiles-managed settings
// file is edited in place instead of being replaced by a regular file.
func ReadFile(path string) (File, error) {
	f := File{Path: path, Real: path, Mode: 0o600}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		f.Real = real
	} else if !errors.Is(err, fs.ErrNotExist) {
		return f, err
	}
	st, err := os.Stat(f.Real)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if !st.Mode().IsRegular() {
		return f, fmt.Errorf("%s is not a regular file", path)
	}
	if f.Data, err = os.ReadFile(f.Real); err != nil {
		return f, err
	}
	f.Mode = st.Mode().Perm()
	f.Exists = true
	return f, nil
}

// Commit backs up the current file (when it exists) next to f.Path as
// <name>.askgate-bak-<unix seconds>, then atomically replaces the content:
// a crash leaves either the old or the new file, never half of one.
func Commit(f File, data []byte, now time.Time) (backup string, err error) {
	if f.Exists {
		if backup, err = writeBackup(f, now); err != nil {
			return "", fmt.Errorf("backup %s: %w", f.Path, err)
		}
	}
	if err := atomicWrite(f.Real, data, f.Mode); err != nil {
		return backup, err
	}
	return backup, nil
}

func writeBackup(f File, now time.Time) (string, error) {
	base := f.Path + ".askgate-bak-" + strconv.FormatInt(now.Unix(), 10)
	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name += "-" + strconv.Itoa(i)
		}
		out, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, f.Mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := out.Write(f.Data); err != nil {
			out.Close()
			return "", err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return "", err
		}
		return name, out.Close()
	}
	return "", errors.New("too many backups with the same timestamp")
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".askgate-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // persist the rename; best effort on filesystems that refuse
		d.Close()
	}
	return nil
}
