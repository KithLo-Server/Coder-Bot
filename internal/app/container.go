package app

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
)

const (
	RunIDEnv       = "OPENCODE_RUN_ID"
	CancelTokenEnv = "OPENCODE_CANCEL_TOKEN"
)

// ErrRunCancelled is returned when /cancel terminated a container command.
var ErrRunCancelled = errors.New("run cancelled")

const killRunScript = `
pids=""
for _ in $(seq 1 20); do
    pids=""
    for envfile in /proc/[0-9]*/environ; do
        if tr '\000' '\n' < "$envfile" 2>/dev/null | grep -Fqx "$OPENCODE_CANCEL_TOKEN"; then
            pid=${envfile#/proc/}
            pids="$pids ${pid%/environ}"
        fi
    done
    [ -n "$pids" ] && break
    sleep 0.1
done
[ -z "$pids" ] && exit 3
kill -TERM $pids 2>/dev/null || true
for _ in $(seq 1 20); do
    alive=""
    for pid in $pids; do
        kill -0 "$pid" 2>/dev/null && alive="$alive $pid"
    done
    [ -z "$alive" ] && exit 0
    sleep 0.1
done
kill -KILL $pids 2>/dev/null || true
exit 0
`

func (a *App) repoBasePath(name string) string {
	return path.Join(a.Cfg.ReposDir, name)
}

func workerContainerName(name string) string {
	return "opencode_" + name
}

func shellContainerName(name string) string {
	return "opencode_shell_" + name
}

// GetRepoContainer returns the worker of a repository, starting it if stopped.
func (a *App) GetRepoContainer(ctx context.Context, name string) (docker.Container, error) {
	repo, ok := a.Repo(name)
	if !ok {
		return nil, nil
	}
	container, err := a.getRunningContainer(ctx, repo.ContainerID, workerContainerName(name))
	if err != nil {
		return nil, err
	}
	if container == nil {
		container, err = a.createWorkerContainer(ctx, name)
		if err != nil {
			return nil, err
		}
	}
	if repo.ContainerID != container.ID() {
		repo.ContainerID = container.ID()
		a.SetRepo(name, repo)
	}
	return container, nil
}

// GetShellContainer returns a repository's shell container, starting it if stopped.
func (a *App) GetShellContainer(ctx context.Context, name string) (docker.Container, error) {
	repo, ok := a.Repo(name)
	if !ok {
		return nil, nil
	}
	container, err := a.getRunningContainer(ctx, repo.ShellContainerID, shellContainerName(name))
	if err != nil {
		return nil, err
	}
	if container == nil {
		container, err = a.createShellContainer(ctx, name)
		if err != nil {
			return nil, err
		}
	}
	if repo.ShellContainerID != container.ID() {
		repo.ShellContainerID = container.ID()
		a.SetRepo(name, repo)
	}
	return container, nil
}

func (a *App) getRunningContainer(ctx context.Context, id, name string) (docker.Container, error) {
	var container docker.Container
	var err error
	if id != "" {
		container, err = a.Docker.GetContainer(ctx, id)
		if err != nil && !errors.Is(err, docker.ErrNotFound) {
			return nil, err
		}
	}
	if container == nil {
		container, err = a.Docker.GetContainer(ctx, name)
		if err != nil {
			if errors.Is(err, docker.ErrNotFound) {
				return nil, nil
			}
			return nil, err
		}
	}
	status, err := container.Status(ctx)
	if err != nil {
		return nil, err
	}
	if status != "running" {
		if err := container.Start(ctx); err != nil {
			return nil, err
		}
	}
	return container, nil
}

// RemoveContainer stops and removes a container, ignoring already-gone cases.
func (a *App) RemoveContainer(ctx context.Context, container docker.Container) {
	if container == nil {
		return
	}
	if err := container.Stop(ctx, 2); err != nil {
		a.Logger.Printf("Failed to stop container %s: %v", container.Name(), err)
	}
	if err := container.Remove(ctx); err != nil {
		a.Logger.Printf("Failed to remove container %s: %v", container.Name(), err)
	}
}

// CopyToContainer copies an in-memory buffer into a container via a tar stream.
func CopyToContainer(ctx context.Context, container docker.Container, data []byte, filename, targetDir string) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name:    filename,
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}); err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return container.PutArchive(ctx, targetDir, &archive)
}

// EnsureContainerDirectory creates a directory and its parents inside a container.
func EnsureContainerDirectory(ctx context.Context, container docker.Container, directory string) error {
	result, err := container.Exec(ctx, docker.ExecOptions{
		Cmd:     []string{"/bin/bash", "-c", "mkdir -p -- " + shellQuote(directory)},
		Workdir: "/",
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		output := strings.TrimSpace(string(result.Output))
		if output == "" {
			output = "unknown error"
		}
		return fmt.Errorf("could not create directory '%s': %s", directory, output)
	}
	return nil
}

// NormalizeRepoURL makes equivalent remote URLs compare equal.
func NormalizeRepoURL(raw string) string {
	normalized := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasPrefix(normalized, "git@") && strings.Contains(normalized, ":") {
		rest := normalized[len("git@"):]
		host, pathPart, _ := strings.Cut(rest, ":")
		normalized = "https://" + host + "/" + pathPart
	}
	normalized = strings.TrimSuffix(normalized, ".git")
	return strings.TrimRight(normalized, "/")
}

// GitConfigArgs builds the -c flags that mark a checkout safe plus extra config.
func GitConfigArgs(workspacePath string, extra []string) string {
	configs := append([]string{"safe.directory=" + workspacePath}, extra...)
	parts := make([]string, len(configs))
	for index, item := range configs {
		parts[index] = "-c " + shellQuote(item)
	}
	return strings.Join(parts, " ")
}

// RunGit runs git in a checkout and returns the raw exec result.
func RunGit(ctx context.Context, container docker.Container, workspacePath string, args []string, extra []string) (docker.ExecResult, error) {
	quoted := make([]string, len(args))
	for index, part := range args {
		quoted[index] = shellQuote(part)
	}
	command := strings.Join(quoted, " ")
	flags := GitConfigArgs(workspacePath, extra)
	result, err := container.Exec(ctx, docker.ExecOptions{
		Cmd:     []string{"/bin/bash", "-c", "git " + flags + " " + command},
		Workdir: workspacePath,
	})
	if err != nil {
		return docker.ExecResult{ExitCode: -1}, err
	}
	return result, nil
}

// InspectWorkspace classifies a checkout directory from inside a container.
func InspectWorkspace(ctx context.Context, container docker.Container, workspacePath string) (string, error) {
	p := shellQuote(workspacePath)
	script := fmt.Sprintf(
		"if [ ! -e %s ]; then echo missing; "+
			"elif [ ! -d %s ]; then echo not-dir; "+
			"elif [ -z \"$(ls -A %s)\" ]; then echo empty; "+
			"elif [ ! -e %s/.git ]; then echo non-git; "+
			"elif [ -e %s/.git/index.lock ]; then echo git-locked; "+
			"else echo git; fi",
		p, p, p, p, p,
	)
	result, err := container.Exec(ctx, docker.ExecOptions{Cmd: []string{"/bin/bash", "-c", script}, Workdir: "/"})
	if err != nil {
		return "missing", err
	}
	status := strings.TrimSpace(string(result.Output))
	switch status {
	case "not-dir", "missing", "empty", "non-git", "git-locked", "git":
		return status, nil
	default:
		return "missing", nil
	}
}

// ExecuteInContainer runs a shell command, exposing runID to the whole process tree.
func (a *App) ExecuteInContainer(ctx context.Context, container docker.Container, command, workdir, runID string) (string, error) {
	env := map[string]string{}
	if runID != "" {
		env[RunIDEnv] = runID
	}
	result, err := container.Exec(ctx, docker.ExecOptions{
		Cmd:     []string{"/bin/bash", "-c", command},
		Workdir: workdir,
		Env:     env,
	})
	if err != nil {
		return "", err
	}
	return string(result.Output), nil
}

// KillRunProcesses terminates every process carrying the run's marker.
func KillRunProcesses(ctx context.Context, container docker.Container, runID string) (bool, error) {
	result, err := container.Exec(ctx, docker.ExecOptions{
		Cmd:     []string{"/bin/bash", "-c", killRunScript},
		Workdir: "/",
		Env:     map[string]string{CancelTokenEnv: RunIDEnv + "=" + runID},
	})
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

// ExecuteCancellable runs a command and reports cancellation when /cancel hit it.
func (a *App) ExecuteCancellable(ctx context.Context, container docker.Container, command string, chatID int64, repoName, workdir string) (string, error) {
	runID := a.startRun(container.ID(), chatID, repoName)
	if a.runCancelled(repoName, runID) {
		a.finishRun(repoName, runID)
		return "", ErrRunCancelled
	}
	output, err := a.ExecuteInContainer(ctx, container, command, workdir, runID)
	if err != nil {
		if a.finishRun(repoName, runID) {
			return "", ErrRunCancelled
		}
		return "", err
	}
	if a.finishRun(repoName, runID) {
		return "", ErrRunCancelled
	}
	return output, nil
}

// RestartContainer restarts a worker whose command could not be signalled.
func RestartContainer(ctx context.Context, container docker.Container) error {
	return container.Restart(ctx, 1)
}

// ResolveBasePath resolves a user path against the active repository on the base
// container.
func (a *App) ResolveBasePath(rawPath, repoName string) string {
	rawPath = strings.TrimSpace(rawPath)
	if !strings.HasPrefix(rawPath, "/") {
		base := a.Cfg.ReposDir
		if repoName != "" {
			base = a.repoBasePath(repoName)
		}
		rawPath = base + "/" + rawPath
	}
	return path.Clean(strings.ReplaceAll(rawPath, "\\", "/"))
}

// DescribeContainerPath returns "file", "dir" or "".
func DescribeContainerPath(ctx context.Context, container docker.Container, target string) (string, error) {
	quoted := shellQuote(target)
	script := fmt.Sprintf("if [ -f %s ]; then echo file; elif [ -d %s ]; then echo dir; else exit 1; fi", quoted, quoted)
	result, err := container.Exec(ctx, docker.ExecOptions{Cmd: []string{"/bin/bash", "-c", script}, Workdir: "/"})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", nil
	}
	kind := strings.TrimSpace(string(result.Output))
	if kind == "file" || kind == "dir" {
		return kind, nil
	}
	return "", nil
}

// ReadFileFromContainer reads one regular file out of a container.
func ReadFileFromContainer(ctx context.Context, container docker.Container, target string) ([]byte, error) {
	reader, err := container.GetArchive(ctx, target)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			return io.ReadAll(tarReader)
		}
	}
	return nil, fmt.Errorf("'%s' is not a regular file", target)
}

// TarDirectoryInContainer streams a .tar.gz of a directory, enforcing the upload limit.
func TarDirectoryInContainer(ctx context.Context, container docker.Container, target string) ([]byte, error) {
	cleaned := strings.TrimRight(target, "/")
	parent := path.Dir(cleaned)
	base := path.Base(cleaned)
	if parent == "" {
		parent = "/"
	}
	command := "tar -czf - -C " + shellQuote(parent) + " " + shellQuote(base)
	stream, err := container.ExecStream(ctx, docker.ExecOptions{
		Cmd:     []string{"/bin/bash", "-c", command},
		Workdir: "/",
	})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return readDemuxed(stream, config.MaxUploadBytes)
}

// readDemuxed reads a Docker multiplexed (or raw) stream up to maxBytes.
func readDemuxed(stream io.Reader, maxBytes int64) ([]byte, error) {
	var output bytes.Buffer
	var buffer []byte
	chunk := make([]byte, 64*1024)
	decided, framed := false, false

	appendOutput := func(data []byte) error {
		if len(data) == 0 {
			return nil
		}
		if int64(output.Len()+len(data)) > maxBytes {
			return fmt.Errorf("compressed directory exceeds the %d MB Telegram limit", maxBytes/(1024*1024))
		}
		output.Write(data)
		return nil
	}

	for {
		n, err := stream.Read(chunk)
		if n > 0 {
			buffer = append(buffer, chunk[:n]...)
			if !decided && len(buffer) >= 8 {
				framed = buffer[0] <= 2 && buffer[1] == 0 && buffer[2] == 0 && buffer[3] == 0
				decided = true
				if !framed {
					if writeErr := appendOutput(buffer); writeErr != nil {
						return nil, writeErr
					}
					buffer = buffer[:0]
				}
			}
			if decided && framed {
				for len(buffer) >= 8 {
					size := int(uint32(buffer[4])<<24 | uint32(buffer[5])<<16 | uint32(buffer[6])<<8 | uint32(buffer[7]))
					if len(buffer) < 8+size {
						break
					}
					if writeErr := appendOutput(buffer[8 : 8+size]); writeErr != nil {
						return nil, writeErr
					}
					buffer = buffer[8+size:]
				}
			} else if decided && !framed {
				if writeErr := appendOutput(buffer); writeErr != nil {
					return nil, writeErr
				}
				buffer = buffer[:0]
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(buffer) > 0 {
		if writeErr := appendOutput(buffer); writeErr != nil {
			return nil, writeErr
		}
	}
	return output.Bytes(), nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	safe := true
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("_@%+=:,./-", character)) {
			safe = false
			break
		}
	}
	if safe {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
