package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// gitOutput runs `git -C dir args…` and returns trimmed stdout. Errors
// carry git's own message so the CLI can show what went wrong.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := gitCmd(ctx, dir, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", gitError(args, err, errb.String())
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// gitOK runs git and reports whether it exited 0; for yes/no queries
// like `show-ref --verify --quiet`.
func gitOK(ctx context.Context, dir string, args ...string) bool {
	cmd := gitCmd(ctx, dir, args...)
	return cmd.Run() == nil
}

// gitStream runs git with stdout to w (so diffs keep git's colouring when
// w is a terminal). okCodes lists non-zero exit codes that are not errors
// (`diff --no-index` exits 1 when files differ).
func gitStream(ctx context.Context, dir string, w io.Writer, args []string, okCodes ...int) error {
	var errb bytes.Buffer
	cmd := gitCmd(ctx, dir, args...)
	cmd.Stdout, cmd.Stderr = w, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		for _, c := range okCodes {
			if ee.ExitCode() == c {
				return nil
			}
		}
	}
	if err != nil {
		return gitError(args, err, errb.String())
	}
	return nil
}

// gitInteractive runs git with this process's stdin and the given output,
// for commands that may prompt for credentials (push).
func gitInteractive(ctx context.Context, dir string, stdout, stderr io.Writer, args ...string) error {
	cmd := gitCmd(ctx, dir, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Never let a hook-less query open an editor or a pager.
	cmd.Env = append(os.Environ(), "GIT_PAGER=cat", "PAGER=cat", "GIT_EDITOR=true")
	return cmd
}

func gitError(args []string, err error, stderr string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("git not found — install git and make sure it is on your PATH")
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("git %s: %s", firstWords(args, 2), msg)
}

func firstWords(args []string, n int) string {
	if len(args) > n {
		args = args[:n]
	}
	return strings.Join(args, " ")
}
