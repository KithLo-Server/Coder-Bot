// Package docker defines the Docker surface used by the bot and a real adapter.
package docker

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned when a container does not exist.
var ErrNotFound = errors.New("container not found")

// Volume is one bind mount.
type Volume struct {
	Source string
	Target string
	Mode   string
}

// RunOptions creates a detached container.
type RunOptions struct {
	Image         string
	Name          string
	Environment   map[string]string
	Volumes       []Volume
	WorkingDir    string
	RestartPolicy string
}

// ExecOptions runs a command in a container.
type ExecOptions struct {
	Cmd     []string
	Workdir string
	Env     map[string]string
}

// ExecResult is the result of a finished exec.
type ExecResult struct {
	ExitCode int
	Output   []byte
}

// PruneReport summarizes what a system prune removed.
type PruneReport struct {
	ContainersDeleted int
	ImagesDeleted     int
	NetworksDeleted   int
	SpaceReclaimed    uint64
}

// Container is the subset of a Docker container the bot uses.
type Container interface {
	ID() string
	Name() string
	ImageID(ctx context.Context) (string, error)
	Status(ctx context.Context) (string, error)
	Start(ctx context.Context) error
	Stop(ctx context.Context, timeoutSeconds int) error
	Remove(ctx context.Context) error
	Restart(ctx context.Context, timeoutSeconds int) error
	Exec(ctx context.Context, options ExecOptions) (ExecResult, error)
	ExecStream(ctx context.Context, options ExecOptions) (io.ReadCloser, error)
	PutArchive(ctx context.Context, targetDir string, content io.Reader) error
	GetArchive(ctx context.Context, path string) (io.ReadCloser, error)
}

// API creates and looks up containers.
type API interface {
	GetContainer(ctx context.Context, ref string) (Container, error)
	RunContainer(ctx context.Context, options RunOptions) (Container, error)
	ImageID(ctx context.Context, ref string) (string, error)
	PullImage(ctx context.Context, ref string) (string, error)
	PruneSystem(ctx context.Context) (PruneReport, error)
}
