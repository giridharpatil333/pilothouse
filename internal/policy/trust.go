package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// TrustedRepo is one entry of trusted-repos.yaml. A trusted repo's
// .pilothouse/policy.yaml may add allow rules; others may only add deny and
// ask rules.
type TrustedRepo struct {
	Path      string    `yaml:"path" json:"path"`
	TrustedAt time.Time `yaml:"trusted_at,omitempty" json:"trusted_at,omitempty"`
}

type trustFile struct {
	Version int           `yaml:"version"`
	Repos   []TrustedRepo `yaml:"repos"`
}

const trustHeader = `# Repositories whose .pilothouse/policy.yaml may add *allow* rules.
# Managed by ` + "`pilothouse policy trust` / `pilothouse policy untrust`" + `.
# Untrusted repos can still add deny and ask rules.
`

// ReadTrustedRepos returns the trusted repos. A missing file is an empty
// list.
func ReadTrustedRepos(file string) ([]TrustedRepo, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseTrustedRepos(file, data)
}

func parseTrustedRepos(file string, data []byte) ([]TrustedRepo, error) {
	var tf trustFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&tf); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // empty file
		}
		return nil, FileErrors{yamlError(file, err)}
	}
	if tf.Version != 0 && tf.Version != 1 {
		return nil, FileErrors{{File: file, Msg: fmt.Sprintf("unsupported version %d", tf.Version)}}
	}
	for i, r := range tf.Repos {
		if !filepath.IsAbs(r.Path) {
			return nil, FileErrors{{File: file, Msg: fmt.Sprintf("repos[%d]: path %q must be absolute", i, r.Path)}}
		}
	}
	return tf.Repos, nil
}

// CanonicalRepo returns the absolute, cleaned, symlink-resolved form of a
// repo root, which is how trusted-repos.yaml stores it.
func CanonicalRepo(repo string) (string, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	return filepath.Clean(abs), nil
}

// TrustRepo adds repo to the trust list. It reports false if it was
// already trusted.
func TrustRepo(file, repo string) (bool, error) {
	canon, err := CanonicalRepo(repo)
	if err != nil {
		return false, err
	}
	repos, err := ReadTrustedRepos(file)
	if err != nil {
		return false, fmt.Errorf("%w\n(fix or delete %s and try again)", err, file)
	}
	for _, r := range repos {
		if r.Path == canon {
			return false, nil
		}
	}
	repos = append(repos, TrustedRepo{Path: canon, TrustedAt: time.Now().UTC().Truncate(time.Second)})
	return true, writeTrustedRepos(file, repos)
}

// UntrustRepo removes repo from the trust list. It reports false if it
// was not trusted.
func UntrustRepo(file, repo string) (bool, error) {
	canon, err := CanonicalRepo(repo)
	if err != nil {
		return false, err
	}
	repos, err := ReadTrustedRepos(file)
	if err != nil {
		return false, fmt.Errorf("%w\n(fix or delete %s and try again)", err, file)
	}
	clean := filepath.Clean(repo)
	out := repos[:0]
	removed := false
	for _, r := range repos {
		if r.Path == canon || r.Path == clean {
			removed = true
			continue
		}
		out = append(out, r)
	}
	if !removed {
		return false, nil
	}
	return true, writeTrustedRepos(file, out)
}

// writeTrustedRepos replaces the file atomically with private permissions.
func writeTrustedRepos(file string, repos []TrustedRepo) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	body, err := yaml.Marshal(trustFile{Version: 1, Repos: repos})
	if err != nil {
		return err
	}
	return writeFileAtomic(file, append([]byte(trustHeader), body...), 0o600)
}

// writeFileAtomic writes via a temp file in the same directory and a
// rename, so the daemon never reads a half-written file.
func writeFileAtomic(file string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, file)
}

// WriteFileAtomic is exported for the CLI (`policy init`).
func WriteFileAtomic(file string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(file, data, perm)
}
