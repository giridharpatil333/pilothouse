package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/paths"
)

// Tasks are recorded locally in paths.Home()/tasks.json, always, and
// mirrored to the daemon (POST /v1/tasks) whenever it is running. The
// local file is the source of truth for `askgate task`: tasks keep
// working with no daemon, and a daemon that was down when a task changed
// is brought up to date the next time the task is touched.

const storeVersion = 1

type storeFile struct {
	Version int        `json:"version"`
	Tasks   []api.Task `json:"tasks"`
}

// StorePath is the local task file.
func StorePath() string { return filepath.Join(paths.Home(), "tasks.json") }

// loadTasks reads the task file; a missing file means no tasks.
func loadTasks() ([]api.Task, error) {
	b, err := os.ReadFile(StorePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (%v) — fix or remove it", StorePath(), err)
	}
	if f.Version > storeVersion {
		return nil, fmt.Errorf("%s was written by a newer askgate (format %d) — upgrade askgate", StorePath(), f.Version)
	}
	return f.Tasks, nil
}

// updateTasks applies fn to the task list under an exclusive lock, so
// concurrent `askgate task` invocations don't lose each other's writes,
// and saves atomically.
func updateTasks(fn func([]api.Task) ([]api.Task, error)) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	lock, err := os.OpenFile(StorePath()+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", StorePath(), err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	tasks, err := loadTasks()
	if err != nil {
		return err
	}
	tasks, err = fn(tasks)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(storeFile{Version: storeVersion, Tasks: tasks}, "", "  ")
	if err != nil {
		return err
	}
	tmp := StorePath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, StorePath())
}

// saveTask inserts or replaces one task.
func saveTask(t api.Task) error {
	return updateTasks(func(ts []api.Task) ([]api.Task, error) {
		for i := range ts {
			if ts[i].ID == t.ID {
				ts[i] = t
				return ts, nil
			}
		}
		return append(ts, t), nil
	})
}

// deleteTask removes one task (used to roll back a failed `task new`).
func deleteTask(id string) error {
	return updateTasks(func(ts []api.Task) ([]api.Task, error) {
		out := ts[:0]
		for _, t := range ts {
			if t.ID != id {
				out = append(out, t)
			}
		}
		return out, nil
	})
}
