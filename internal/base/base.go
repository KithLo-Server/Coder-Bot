// Package base exposes the bot's own container as a Docker-like executor.
package base

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"opencodebot/internal/docker"
)

const (
	// ContainerID is the synthetic id used for runs in the base container.
	ContainerID = "base"
	// ContainerName is the display name of the base container.
	ContainerName = "base"
)

// Container implements docker.Container against the bot's own filesystem and
// processes.
type Container struct{}

// New returns a base container adapter.
func New() *Container { return &Container{} }

func (c *Container) ID() string   { return ContainerID }
func (c *Container) Name() string { return ContainerName }

func (c *Container) ImageID(context.Context) (string, error) { return ContainerID, nil }

func (c *Container) Status(context.Context) (string, error) { return "running", nil }

func (c *Container) Start(context.Context) error { return nil }

func (c *Container) Stop(context.Context, int) error {
	return errors.New("the base container cannot be stopped")
}

func (c *Container) Remove(context.Context) error {
	return errors.New("the base container cannot be removed")
}

func (c *Container) Restart(context.Context, int) error {
	return errors.New("the base container cannot be restarted")
}

func (c *Container) Exec(ctx context.Context, options docker.ExecOptions) (docker.ExecResult, error) {
	if len(options.Cmd) == 0 {
		return docker.ExecResult{}, errors.New("empty command")
	}
	command := exec.CommandContext(ctx, options.Cmd[0], options.Cmd[1:]...)
	command.Dir = options.Workdir
	command.Env = mergeEnv(options.Env)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return docker.ExecResult{ExitCode: exitError.ExitCode(), Output: output.Bytes()}, nil
		}
		return docker.ExecResult{ExitCode: -1, Output: output.Bytes()}, err
	}
	return docker.ExecResult{ExitCode: 0, Output: output.Bytes()}, nil
}

func (c *Container) ExecStream(ctx context.Context, options docker.ExecOptions) (io.ReadCloser, error) {
	if len(options.Cmd) == 0 {
		return nil, errors.New("empty command")
	}
	command := exec.CommandContext(ctx, options.Cmd[0], options.Cmd[1:]...)
	command.Dir = options.Workdir
	command.Env = mergeEnv(options.Env)
	reader, writer := io.Pipe()
	command.Stdout = writer
	command.Stderr = writer
	if err := command.Start(); err != nil {
		_ = writer.CloseWithError(err)
		return nil, err
	}
	go func() {
		_ = writer.CloseWithError(command.Wait())
	}()
	return reader, nil
}

func (c *Container) PutArchive(_ context.Context, targetDir string, content io.Reader) error {
	reader := tar.NewReader(content)
	cleanTarget := filepath.Clean(targetDir)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
			return fmt.Errorf("invalid archive entry %q", header.Name)
		}
		target := filepath.Join(cleanTarget, name)
		if target != cleanTarget && !strings.HasPrefix(target, cleanTarget+string(filepath.Separator)) {
			return fmt.Errorf("archive entry %q escapes the target directory", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func (c *Container) GetArchive(_ context.Context, target string) (io.ReadCloser, error) {
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	go func() {
		_ = writer.CloseWithError(writeArchive(writer, target, info))
	}()
	return reader, nil
}

func writeArchive(writer io.Writer, target string, info os.FileInfo) error {
	tarWriter := tar.NewWriter(writer)
	baseDir := filepath.Dir(target)
	if !info.IsDir() {
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.Base(target)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(target)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return tarWriter.Close()
	}
	err := filepath.Walk(target, func(current string, currentInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(baseDir, current)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(currentInfo, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if currentInfo.IsDir() {
			header.Name += "/"
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if currentInfo.Mode().IsRegular() {
			file, err := os.Open(current)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tarWriter, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tarWriter.Close()
}

func mergeEnv(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return os.Environ()
	}
	env := append([]string(nil), os.Environ()...)
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

var _ docker.Container = (*Container)(nil)
