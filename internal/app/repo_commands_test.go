package app

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
	"opencodebot/internal/state"
	"opencodebot/internal/telegram"
)

func inspectScript(options docker.ExecOptions) bool {
	return len(options.Cmd) == 3 && strings.Contains(options.Cmd[2], "echo missing")
}

func describeScript(options docker.ExecOptions) bool {
	return len(options.Cmd) == 3 && strings.HasPrefix(options.Cmd[2], "if [ -f")
}

func gitScript(options docker.ExecOptions) bool {
	return len(options.Cmd) == 3 && strings.Contains(options.Cmd[2], "git -c")
}

func tarFile(name string, data []byte) []byte {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})
	writer.Write(data)
	writer.Close()
	return buffer.Bytes()
}

func TestCreateClonesWithoutContainers(t *testing.T) {
	env := newTestEnv(t)
	var clones int
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		switch {
		case inspectScript(options):
			return docker.ExecResult{Output: []byte("missing\n")}, nil
		case gitScript(options) && strings.Contains(options.Cmd[2], "clone"):
			clones++
			return docker.ExecResult{ExitCode: 0}, nil
		}
		return docker.ExecResult{}, nil
	}
	if err := env.app.CmdCreate(env.ctx, env.message("/create repo https://example.com/repo", 1), []string{"repo", "https://example.com/repo"}); err != nil {
		t.Fatal(err)
	}
	if clones != 1 || !env.app.RepoExists("repo") {
		t.Fatalf("clones=%d repo=%v", clones, env.app.RepoExists("repo"))
	}
	if env.app.ActiveRepo(1) != "repo" {
		t.Fatal("repo should be active")
	}
	if len(env.docker.runCalls) != 0 {
		t.Fatalf("run calls = %d", len(env.docker.runCalls))
	}
	repo, _ := env.app.Repo("repo")
	if repo.ContainerID != "" || repo.ShellContainerID != "" {
		t.Fatalf("container ids = %q/%q", repo.ContainerID, repo.ShellContainerID)
	}
	if _, err := os.Stat(env.app.Cfg.StateFile); err != nil {
		t.Fatalf("state not saved: %v", err)
	}
	if !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "initialized") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
}

func TestCreateRefusesNonGitWithoutContainers(t *testing.T) {
	env := newTestEnv(t)
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if inspectScript(options) {
			return docker.ExecResult{Output: []byte("non-git\n")}, nil
		}
		return docker.ExecResult{}, nil
	}
	env.app.CmdCreate(env.ctx, env.message("/create repo url", 1), []string{"repo", "url"})
	if env.app.RepoExists("repo") {
		t.Fatal("non-git directory must be refused")
	}
	if len(env.docker.runCalls) != 0 {
		t.Fatalf("run calls = %d", len(env.docker.runCalls))
	}
	if _, exists := env.docker.containers[workerContainerName("repo")]; exists {
		t.Fatal("worker should not be created")
	}
	if !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "not a git repository") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
}

func TestSetInfoList(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.addRepo("other")
	if err := env.app.CmdSet(env.ctx, env.message("/set repo", 1), []string{"repo"}); err != nil {
		t.Fatal(err)
	}
	if env.app.ActiveRepo(1) != "repo" {
		t.Fatal("not active")
	}
	env.app.CmdInfo(env.ctx, env.message("/info", 1), nil)
	info := env.tg.messages[len(env.tg.messages)-1].Text
	if !strings.Contains(info, "**Active Context:** `repo`") || !strings.Contains(info, "M1") || !strings.Contains(info, "running") {
		t.Fatalf("info = %q", info)
	}
	env.app.CmdList(env.ctx, env.message("/list", 1), nil)
	list := env.tg.messages[len(env.tg.messages)-1].Text
	if !strings.Contains(list, "👉 `repo`") || !strings.Contains(list, "• `other`") {
		t.Fatalf("list = %q", list)
	}
	env.app.CmdSet(env.ctx, env.message("/set missing", 1), []string{"missing"})
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "not found") {
		t.Fatal("missing repo should be reported")
	}
}

func TestMountAndUnmountRecreateWorker(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.addRepo("other")
	env.app.SetActiveContext(1, "repo")
	env.app.SetSession(1, "repo", "session-1")
	if err := env.app.CmdMount(env.ctx, env.message("/mount other", 1), []string{"other"}); err != nil {
		t.Fatal(err)
	}
	repo, _ := env.app.Repo("repo")
	if len(repo.Mounts) != 1 || repo.Mounts[0] != "other" {
		t.Fatalf("mounts = %+v", repo.Mounts)
	}
	if len(env.docker.runCalls) != 2 {
		t.Fatalf("run calls = %d", len(env.docker.runCalls))
	}
	volumes := env.docker.runCalls[0].Volumes
	found := false
	for _, volume := range volumes {
		if volume.Source == "/repos/other" && volume.Target == "/opt/other" {
			found = true
		}
	}
	if !found {
		t.Fatalf("volumes = %+v", volumes)
	}
	if err := env.app.CmdUnmount(env.ctx, env.message("/unmount other", 1), []string{"other"}); err != nil {
		t.Fatal(err)
	}
	repo, _ = env.app.Repo("repo")
	if len(repo.Mounts) != 0 {
		t.Fatalf("mounts = %+v", repo.Mounts)
	}
	if got := env.app.Session(1, "repo"); got != "session-1" {
		t.Fatalf("session = %q", got)
	}

	env.docker.runErr = errors.New("boom")
	env.app.CmdMount(env.ctx, env.message("/mount other", 1), []string{"other"})
	repo, _ = env.app.Repo("repo")
	if len(repo.Mounts) != 0 {
		t.Fatalf("failed mount should be rolled back: %+v", repo.Mounts)
	}
}

func TestReloadStopsContainersAndSyncs(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetSession(1, "repo", "session-1")
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if inspectScript(options) {
			return docker.ExecResult{Output: []byte("git\n")}, nil
		}
		if gitScript(options) {
			if strings.Contains(options.Cmd[2], "remote.origin.url") {
				return docker.ExecResult{Output: []byte("https://example.com/repo\n")}, nil
			}
			return docker.ExecResult{ExitCode: 0}, nil
		}
		return docker.ExecResult{}, nil
	}
	if err := env.app.CmdReload(env.ctx, env.message("/reload repo", 1), []string{"repo"}); err != nil {
		t.Fatal(err)
	}
	if worker.removed == 0 {
		t.Fatal("old worker should be removed")
	}
	if len(env.docker.runCalls) != 0 {
		t.Fatalf("run calls = %d", len(env.docker.runCalls))
	}
	repo, _ := env.app.Repo("repo")
	if repo.ContainerID != "" || repo.ShellContainerID != "" {
		t.Fatalf("container ids = %q/%q", repo.ContainerID, repo.ShellContainerID)
	}
	if env.app.ActiveRepo(1) != "repo" {
		t.Fatal("reload should set active")
	}
	if got := env.app.Session(1, "repo"); got != "session-1" {
		t.Fatalf("session = %q", got)
	}
}

func TestReloadAllSummarizes(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("one")
	env.addRepo("two")
	env.app.SetSession(1, "one", "session-one")
	env.app.SetSession(1, "two", "session-two")
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if inspectScript(options) {
			return docker.ExecResult{Output: []byte("git\n")}, nil
		}
		if gitScript(options) {
			if strings.Contains(options.Cmd[2], "remote.origin.url") {
				if strings.Contains(options.Cmd[2], "/two") {
					return docker.ExecResult{Output: []byte("https://example.com/two\n")}, nil
				}
				return docker.ExecResult{Output: []byte("https://example.com/one\n")}, nil
			}
			return docker.ExecResult{ExitCode: 0}, nil
		}
		return docker.ExecResult{}, nil
	}
	env.app.CmdReloadAll(env.ctx, env.message("/reloadall", 1), nil)
	if len(env.docker.runCalls) != 0 {
		t.Fatalf("run calls = %d", len(env.docker.runCalls))
	}
	last := env.tg.edits[len(env.tg.edits)-1].Text
	if !strings.Contains(last, "Reloaded 2/2") {
		t.Fatalf("summary = %q", last)
	}
	if env.app.Session(1, "one") != "session-one" || env.app.Session(1, "two") != "session-two" {
		t.Fatalf("sessions = %q/%q", env.app.Session(1, "one"), env.app.Session(1, "two"))
	}
}

func TestStopRemovesContainersAndKeepsState(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.app.SetSession(1, "repo", "session-1")
	if err := env.app.CmdStop(env.ctx, env.message("/stop repo", 1), []string{"repo"}); err != nil {
		t.Fatal(err)
	}
	if worker.removed == 0 || !env.app.RepoExists("repo") || env.app.ActiveRepo(1) != "repo" {
		t.Fatalf("stop incomplete: removed=%d repo=%v active=%q", worker.removed, env.app.RepoExists("repo"), env.app.ActiveRepo(1))
	}
	repo, _ := env.app.Repo("repo")
	if repo.ContainerID != "" || repo.ShellContainerID != "" {
		t.Fatalf("container ids = %q/%q", repo.ContainerID, repo.ShellContainerID)
	}
	if got := env.app.Session(1, "repo"); got != "session-1" {
		t.Fatalf("session = %q", got)
	}
}

func TestPurgeSafetyAndSuccess(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	statusOutput := ""
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		switch {
		case inspectScript(options):
			return docker.ExecResult{Output: []byte("git\n")}, nil
		case gitScript(options):
			script := options.Cmd[2]
			switch {
			case strings.Contains(script, "status --porcelain"):
				return docker.ExecResult{Output: []byte(statusOutput)}, nil
			case strings.Contains(script, "remote") && !strings.Contains(script, "rev-list"):
				return docker.ExecResult{Output: []byte("origin\n")}, nil
			case strings.Contains(script, "rev-list"):
				return docker.ExecResult{Output: []byte("0\n")}, nil
			}
		case strings.Contains(options.Cmd[2], "rm -rf"):
			return docker.ExecResult{ExitCode: 0}, nil
		}
		return docker.ExecResult{}, nil
	}
	statusOutput = " M file\n"
	env.app.CmdPurge(env.ctx, env.message("/purge repo", 1), []string{"repo"})
	if !env.app.RepoExists("repo") {
		t.Fatal("dirty checkout must not be purged")
	}
	if !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "uncommitted") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
	statusOutput = ""
	env.app.CmdPurge(env.ctx, env.message("/purge repo", 1), []string{"repo"})
	if env.app.RepoExists("repo") {
		t.Fatal("clean checkout should be purged")
	}

	env.app.CmdPurge(env.ctx, env.message("/purge ../bad", 1), []string{"../bad"})
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "Refusing") {
		t.Fatal("unsafe name must be refused")
	}
}

func TestDownloadFileAndDirectory(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	shell := env.repoShell("repo")
	shell.archives["/workspace/README.md"] = tarFile("README.md", []byte("hello"))
	var kind = "file"
	shell.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if describeScript(options) {
			return docker.ExecResult{Output: []byte(kind + "\n")}, nil
		}
		return docker.ExecResult{}, nil
	}
	env.app.CmdDL(env.ctx, env.message("/dl README.md", 1), []string{"README.md"})
	if len(env.tg.documents) != 1 || string(env.tg.documentRaw) != "hello" || !strings.Contains(env.tg.documents[0].Text, "README.md") {
		t.Fatalf("documents = %+v raw=%q", env.tg.documents, env.tg.documentRaw)
	}

	kind = "dir"
	shell.stream = ioNopCloser("tarball")
	env.app.CmdDL(env.ctx, env.message("/dl .", 1), []string{"."})
	if len(env.tg.documents) != 2 || string(env.tg.documentRaw) != "tarball" || !strings.Contains(env.tg.documents[1].Text, "compressed") {
		t.Fatalf("directory = %+v", env.tg.documents)
	}

	kind = ""
	env.app.CmdDL(env.ctx, env.message("/dl nope", 1), []string{"nope"})
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "does not exist") {
		t.Fatal("missing path should be reported")
	}
}

func TestUploadAttachment(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.tg.files["doc"] = []byte("payload")
	shell := env.repoShell("repo")
	shell.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if describeScript(options) {
			return docker.ExecResult{ExitCode: 1}, nil
		}
		return docker.ExecResult{}, nil
	}
	update := &telegram.Update{ChatID: 1, UserID: 1, Message: &telegram.Message{
		ID: 7, ChatID: 1, Caption: "/ul docs/file.txt",
		Document: &telegram.Attachment{FileID: "doc", FileName: "file.txt"},
	}}
	if err := env.app.CmdUL(env.ctx, update, []string{"docs/file.txt"}); err != nil {
		t.Fatal(err)
	}
	if len(shell.putPaths) != 1 || shell.putPaths[0] != "/workspace/docs" {
		t.Fatalf("put paths = %+v", shell.putPaths)
	}
	if !strings.Contains(string(shell.putData[0]), "payload") {
		t.Fatal("archive missing payload")
	}
	if len(env.base.putPaths) != 0 {
		t.Fatalf("base container should not receive uploads: %+v", env.base.putPaths)
	}
	if !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "Uploaded `file.txt`") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
}

func ioNopCloser(text string) *stringCloser {
	return &stringCloser{Reader: strings.NewReader(text)}
}

type stringCloser struct {
	*strings.Reader
}

func (s *stringCloser) Close() error { return nil }

func TestCreateRefusesRemoteMismatchAndWorkspaceKinds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		remote  string
		message string
	}{
		{"mismatch", "git", "https://example.com/other\n", "Remote URL mismatch"},
		{"notdir", "not-dir", "", "not a directory"},
		{"locked", "git-locked", "", "index.lock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
				if inspectScript(options) {
					return docker.ExecResult{Output: []byte(tc.status + "\n")}, nil
				}
				if gitScript(options) && strings.Contains(options.Cmd[2], "remote.origin.url") {
					return docker.ExecResult{ExitCode: 0, Output: []byte(tc.remote)}, nil
				}
				return docker.ExecResult{}, nil
			}
			env.app.CmdCreate(env.ctx, env.message("/create repo https://example.com/repo", 1), []string{"repo", "https://example.com/repo"})
			if env.app.RepoExists("repo") {
				t.Fatal("setup should have been refused")
			}
			last := env.tg.edits[len(env.tg.edits)-1].Text
			if !strings.Contains(last, tc.message) {
				t.Fatalf("message = %q", last)
			}
		})
	}
}

func TestCreateUpdatesExistingMatchingClone(t *testing.T) {
	env := newTestEnv(t)
	var pull bool
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		script := options.Cmd[len(options.Cmd)-1]
		if inspectScript(options) {
			return docker.ExecResult{Output: []byte("git\n")}, nil
		}
		if gitScript(options) {
			if strings.Contains(script, "remote.origin.url") {
				return docker.ExecResult{ExitCode: 0, Output: []byte("https://example.com/repo\n")}, nil
			}
			if strings.Contains(script, "pull --ff-only") {
				pull = true
				return docker.ExecResult{ExitCode: 0}, nil
			}
		}
		return docker.ExecResult{}, nil
	}
	env.app.CmdCreate(env.ctx, env.message("/create repo https://example.com/repo", 1), []string{"repo", "https://example.com/repo"})
	if !pull || env.app.ActiveRepo(1) != "repo" {
		t.Fatalf("pull=%v active=%q", pull, env.app.ActiveRepo(1))
	}
	if !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "updated") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
}

func TestPurgeMissingCheckoutRemovesState(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	dataPath, err := env.app.prepareOpencodeData("repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath+"/opencode.db", []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if inspectScript(options) {
			return docker.ExecResult{Output: []byte("missing\n")}, nil
		}
		return docker.ExecResult{}, nil
	}
	env.app.CmdPurge(env.ctx, env.message("/purge repo", 1), []string{"repo"})
	if env.app.RepoExists("repo") {
		t.Fatal("state should be removed")
	}
	if !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "no checkout left") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Fatalf("opencode data directory still exists: %v", err)
	}
}

func TestUploadTrailingSlashOnFileIsRejected(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.tg.files["doc"] = []byte("payload")
	env.repoShell("repo").execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if describeScript(options) {
			return docker.ExecResult{Output: []byte("file\n")}, nil
		}
		return docker.ExecResult{}, nil
	}
	update := &telegram.Update{ChatID: 1, UserID: 1, Message: &telegram.Message{
		ID: 7, ChatID: 1, Caption: "/ul existing/",
		Document: &telegram.Attachment{FileID: "doc", FileName: "file.txt"},
	}}
	env.app.CmdUL(env.ctx, update, []string{"existing/"})
	last := env.tg.edits[len(env.tg.edits)-1].Text
	if !strings.Contains(last, "not a directory") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
}

func TestGetContainersCreatesMissingOnDemand(t *testing.T) {
	env := newTestEnv(t)
	env.app.SetRepo("repo", &state.Repo{URL: "https://example.com/repo"})
	worker, err := env.app.GetRepoContainer(env.ctx, "repo")
	if err != nil || worker == nil {
		t.Fatalf("worker = %v err = %v", worker, err)
	}
	shell, err := env.app.GetShellContainer(env.ctx, "repo")
	if err != nil || shell == nil {
		t.Fatalf("shell = %v err = %v", shell, err)
	}
	repo, _ := env.app.Repo("repo")
	if repo.ContainerID != worker.ID() || repo.ShellContainerID != shell.ID() {
		t.Fatalf("container ids not stored: %+v", repo)
	}
	if len(env.docker.runCalls) != 2 {
		t.Fatalf("run calls = %d", len(env.docker.runCalls))
	}
	workerRun := env.docker.runCalls[0]
	if workerRun.Environment["OPENCODE_DB"] != config.OpenCodeDBPath {
		t.Fatalf("opencode db env = %q", workerRun.Environment["OPENCODE_DB"])
	}
	foundVolume := false
	for _, volume := range workerRun.Volumes {
		if volume.Source == env.app.opencodeDataPath("repo") && volume.Target == config.OpenCodeDataDir {
			foundVolume = true
		}
	}
	if !foundVolume {
		t.Fatalf("worker volumes = %+v", workerRun.Volumes)
	}
}
