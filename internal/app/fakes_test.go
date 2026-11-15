package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
	"opencodebot/internal/state"
	"opencodebot/internal/telegram"
)

type sentMessage struct {
	ChatID  int64
	Text    string
	Options telegram.SendOptions
}

type fakeMessenger struct {
	mu          sync.Mutex
	nextID      int
	messages    []sentMessage
	documents   []sentMessage
	documentRaw []byte
	edits       []sentMessage
	markups     []struct {
		ChatID    int64
		MessageID int
		Keyboard  telegram.Keyboard
	}
	deleted     []int
	chatAction  []string
	answered    []string
	files       map[string][]byte
	markdownErr bool
}

func newFakeMessenger() *fakeMessenger {
	return &fakeMessenger{nextID: 100, files: make(map[string][]byte)}
}

func (f *fakeMessenger) SendMessage(_ context.Context, chatID int64, text string, options telegram.SendOptions) (*telegram.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if options.Markdown && f.markdownErr {
		f.markdownErr = false
		return nil, fmt.Errorf("markdown parse: %w", errBadRequest{})
	}
	f.nextID++
	f.messages = append(f.messages, sentMessage{ChatID: chatID, Text: text, Options: options})
	return &telegram.Message{ID: f.nextID, ChatID: chatID, Text: text}, nil
}

func (f *fakeMessenger) SendDocument(_ context.Context, chatID int64, filename string, data []byte, caption string, options telegram.SendOptions) (*telegram.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.documentRaw = append([]byte(nil), data...)
	f.documents = append(f.documents, sentMessage{ChatID: chatID, Text: caption, Options: options})
	return &telegram.Message{ID: f.nextID, ChatID: chatID}, nil
}

func (f *fakeMessenger) EditMessageText(_ context.Context, chatID int64, messageID int, text string, options telegram.SendOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, sentMessage{ChatID: chatID, Text: text, Options: options})
	return nil
}

func (f *fakeMessenger) EditReplyMarkup(_ context.Context, chatID int64, messageID int, keyboard telegram.Keyboard) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markups = append(f.markups, struct {
		ChatID    int64
		MessageID int
		Keyboard  telegram.Keyboard
	}{chatID, messageID, keyboard})
	return nil
}

func (f *fakeMessenger) DeleteMessage(_ context.Context, chatID int64, messageID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, messageID)
	return nil
}

func (f *fakeMessenger) SendChatAction(_ context.Context, chatID int64, action string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatAction = append(f.chatAction, action)
	return nil
}

func (f *fakeMessenger) AnswerCallback(_ context.Context, callbackID, text string, showAlert bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, text)
	return nil
}

func (f *fakeMessenger) GetFileBytes(_ context.Context, fileID string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[fileID]
	if !ok {
		return nil, "", errors.New("file not found")
	}
	return append([]byte(nil), data...), "file", nil
}

type errBadRequest struct{}

func (errBadRequest) Error() string { return "bad request" }

type fakeDocker struct {
	mu         sync.Mutex
	nextID     int
	containers map[string]*fakeContainer
	runCalls   []docker.RunOptions
	runErr     error
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{nextID: 0, containers: make(map[string]*fakeContainer)}
}

func (f *fakeDocker) add(container *fakeContainer) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[container.id] = container
	f.containers[container.name] = container
	return container
}

func (f *fakeDocker) GetContainer(_ context.Context, ref string) (docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if container, ok := f.containers[ref]; ok {
		container.mu.Lock()
		removed := container.removed > 0
		container.mu.Unlock()
		if !removed {
			return container, nil
		}
	}
	return nil, docker.ErrNotFound
}

func (f *fakeDocker) RunContainer(_ context.Context, options docker.RunOptions) (docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCalls = append(f.runCalls, options)
	if f.runErr != nil {
		return nil, f.runErr
	}
	f.nextID++
	container := &fakeContainer{id: fmt.Sprintf("new-%d", f.nextID), name: options.Name, status: "running", archives: make(map[string][]byte)}
	f.containers[container.id] = container
	f.containers[container.name] = container
	return container, nil
}

type fakeContainer struct {
	mu        sync.Mutex
	id        string
	name      string
	status    string
	execFunc  func(docker.ExecOptions) (docker.ExecResult, error)
	stream    io.ReadCloser
	archives  map[string][]byte
	putPaths  []string
	putData   [][]byte
	started   int
	stopped   int
	removed   int
	restarted int
}

func (c *fakeContainer) ID() string   { return c.id }
func (c *fakeContainer) Name() string { return c.name }

func (c *fakeContainer) Status(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status, nil
}

func (c *fakeContainer) Start(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started++
	c.status = "running"
	return nil
}

func (c *fakeContainer) Stop(context.Context, int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped++
	c.status = "exited"
	return nil
}

func (c *fakeContainer) Remove(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed++
	return nil
}

func (c *fakeContainer) Restart(context.Context, int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restarted++
	return nil
}

func (c *fakeContainer) Exec(_ context.Context, options docker.ExecOptions) (docker.ExecResult, error) {
	c.mu.Lock()
	execFunc := c.execFunc
	c.mu.Unlock()
	if execFunc != nil {
		return execFunc(options)
	}
	return docker.ExecResult{}, nil
}

func (c *fakeContainer) ExecStream(_ context.Context, options docker.ExecOptions) (io.ReadCloser, error) {
	if c.stream != nil {
		return c.stream, nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (c *fakeContainer) PutArchive(_ context.Context, targetDir string, content io.Reader) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putPaths = append(c.putPaths, targetDir)
	c.putData = append(c.putData, data)
	return nil
}

func (c *fakeContainer) GetArchive(_ context.Context, path string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.archives[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func testCatalog(t *testing.T) *config.Catalog {
	t.Helper()
	catalog, err := config.LoadCatalog(`{
		"version":1,
		"models":{
			"A":{"P":{"m1":{"name":"M1","variants":["low","high"]},"m2":{"name":"M2"}}},
			"B":{"Q":{"m3":{"name":"M3","variants":["low","high"]}}},
			"C":{"R":{"m4":{"name":"M4"}}}
		},
		"default_model":{"id":"m1","variant":"low"}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

type testEnv struct {
	app    *App
	tg     *fakeMessenger
	docker *fakeDocker
	shell  *fakeContainer
	ctx    context.Context
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	tg := newFakeMessenger()
	fake := newFakeDocker()
	shell := fake.add(&fakeContainer{id: "shell-id", name: config.ShellContainerName, status: "running", archives: make(map[string][]byte)})
	cfg := &config.Config{
		DeepseekAPIKey: "key",
		HostReposDir:   "/repos",
		DataDir:        t.TempDir(),
		StateFile:      filepath.Join(t.TempDir(), "state.json"),
		AllowedUserIDs: map[string]bool{},
		Catalog:        testCatalog(t),
	}
	application := New(cfg, state.New(), tg, fake, log.New(io.Discard, "", 0))
	return &testEnv{app: application, tg: tg, docker: fake, shell: shell, ctx: context.Background()}
}

func (e *testEnv) addRepo(name string) *fakeContainer {
	container := e.docker.add(&fakeContainer{id: "worker-" + name, name: "opencode_" + name, status: "running", archives: make(map[string][]byte)})
	e.app.SetRepo(name, &state.Repo{URL: "https://example.com/" + name, ContainerID: container.id, Model: "m1", Variant: e.app.Cfg.Catalog.DefaultVariant, VariantSet: true})
	return container
}

func (e *testEnv) message(text string, chatID int64) *telegram.Update {
	return &telegram.Update{ChatID: chatID, UserID: 1, Message: &telegram.Message{ID: 7, ChatID: chatID, Text: text}}
}

func (e *testEnv) callback(data string, chatID int64) *telegram.Update {
	return &telegram.Update{ChatID: chatID, UserID: 1, Callback: &telegram.Callback{ID: "cb", Data: data, Message: &telegram.Message{ID: 42, ChatID: chatID}}}
}
