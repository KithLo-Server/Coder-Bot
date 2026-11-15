package docker

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

// SDK is the production Docker adapter.
type SDK struct {
	client *client.Client
}

// NewSDK creates a Docker client from the environment.
func NewSDK() (*SDK, error) {
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &SDK{client: api}, nil
}

// GetContainer looks up a container by id or name.
func (sdk *SDK) GetContainer(ctx context.Context, ref string) (Container, error) {
	info, err := sdk.client.ContainerInspect(ctx, ref)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sdkContainer{client: sdk.client, id: info.ID, name: strings.TrimPrefix(info.Name, "/")}, nil
}

// RunContainer creates and starts a detached container.
func (sdk *SDK) RunContainer(ctx context.Context, options RunOptions) (Container, error) {
	env := make([]string, 0, len(options.Environment))
	for key, value := range options.Environment {
		env = append(env, key+"="+value)
	}
	binds := make([]string, 0, len(options.Volumes))
	for _, volume := range options.Volumes {
		mode := volume.Mode
		if mode == "" {
			mode = "rw"
		}
		binds = append(binds, volume.Source+":"+volume.Target+":"+mode)
	}
	if _, _, err := sdk.client.ImageInspectWithRaw(ctx, options.Image); err != nil {
		if !errdefs.IsNotFound(err) {
			return nil, err
		}
		pull, err := sdk.client.ImagePull(ctx, options.Image, image.PullOptions{})
		if err != nil {
			return nil, err
		}
		defer pull.Close()
		if _, err := io.Copy(io.Discard, pull); err != nil {
			return nil, err
		}
	}
	created, err := sdk.client.ContainerCreate(ctx,
		&container.Config{
			Image:      options.Image,
			Env:        env,
			WorkingDir: options.WorkingDir,
		},
		&container.HostConfig{
			Binds:         binds,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(options.RestartPolicy)},
		},
		nil, nil, options.Name,
	)
	if err != nil {
		return nil, err
	}
	if err := sdk.client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return nil, err
	}
	return &sdkContainer{client: sdk.client, id: created.ID, name: options.Name}, nil
}

type sdkContainer struct {
	client *client.Client
	id     string
	name   string
}

func (c *sdkContainer) ID() string   { return c.id }
func (c *sdkContainer) Name() string { return c.name }

func (c *sdkContainer) Status(ctx context.Context) (string, error) {
	info, err := c.client.ContainerInspect(ctx, c.id)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	return info.State.Status, nil
}

func (c *sdkContainer) Start(ctx context.Context) error {
	return c.client.ContainerStart(ctx, c.id, container.StartOptions{})
}

func (c *sdkContainer) Stop(ctx context.Context, timeoutSeconds int) error {
	timeout := timeoutSeconds
	return c.client.ContainerStop(ctx, c.id, container.StopOptions{Timeout: &timeout})
}

func (c *sdkContainer) Remove(ctx context.Context) error {
	return c.client.ContainerRemove(ctx, c.id, container.RemoveOptions{})
}

func (c *sdkContainer) Restart(ctx context.Context, timeoutSeconds int) error {
	timeout := timeoutSeconds
	return c.client.ContainerRestart(ctx, c.id, container.StopOptions{Timeout: &timeout})
}

func (c *sdkContainer) Exec(ctx context.Context, options ExecOptions) (ExecResult, error) {
	created, err := c.client.ContainerExecCreate(ctx, c.id, container.ExecOptions{
		Cmd:        options.Cmd,
		WorkingDir: options.Workdir,
		Env:        envSlice(options.Env),
	})
	if err != nil {
		return ExecResult{}, err
	}
	attached, err := c.client.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, err
	}
	defer attached.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		// A raw (non-multiplexed) stream is returned as-is.
		return ExecResult{Output: append(stdout.Bytes(), stderr.Bytes()...)}, nil
	}
	inspect, err := c.client.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{ExitCode: inspect.ExitCode, Output: append(stdout.Bytes(), stderr.Bytes()...)}, nil
}

func (c *sdkContainer) ExecStream(ctx context.Context, options ExecOptions) (io.ReadCloser, error) {
	created, err := c.client.ContainerExecCreate(ctx, c.id, container.ExecOptions{
		Cmd:        options.Cmd,
		WorkingDir: options.Workdir,
		Env:        envSlice(options.Env),
	})
	if err != nil {
		return nil, err
	}
	attached, err := c.client.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	return &hijackedReader{response: attached}, nil
}

func (c *sdkContainer) PutArchive(ctx context.Context, targetDir string, content io.Reader) error {
	return c.client.CopyToContainer(ctx, c.id, targetDir, content, container.CopyToContainerOptions{})
}

func (c *sdkContainer) GetArchive(ctx context.Context, path string) (io.ReadCloser, error) {
	reader, _, err := c.client.CopyFromContainer(ctx, c.id, path)
	return reader, err
}

func envSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	return result
}

type hijackedReader struct {
	response types.HijackedResponse
}

func (r *hijackedReader) Read(p []byte) (int, error) { return r.response.Reader.Read(p) }
func (r *hijackedReader) Close() error {
	r.response.Close()
	return nil
}

var _ API = (*SDK)(nil)
var _ Container = (*sdkContainer)(nil)
