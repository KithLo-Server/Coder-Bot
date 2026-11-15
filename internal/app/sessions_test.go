package app

import (
	"strings"
	"testing"

	"opencodebot/internal/docker"
	"opencodebot/internal/telegram"
)

func opencodeJSON(text, session string) []byte {
	return []byte(`{"type":"text","part":{"text":"` + text + `"},"sessionID":"` + session + `"}` + "\n")
}

func TestAskStoresSessionAndAddsPlanButton(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	var commands []string
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		commands = append(commands, options.Cmd[len(options.Cmd)-1])
		return docker.ExecResult{Output: opencodeJSON("the plan", "ses_1")}, nil
	}
	if err := env.app.CmdAsk(env.ctx, env.message("/ask fix it", 1), []string{"fix", "it"}); err != nil {
		t.Fatal(err)
	}
	if env.app.Session(1, "repo") != "ses_1" {
		t.Fatalf("session = %q", env.app.Session(1, "repo"))
	}
	if len(commands) != 1 || !strings.Contains(commands[0], "--agent plan") || !strings.Contains(commands[0], `--model "m1"`) || !strings.Contains(commands[0], "--variant low") {
		t.Fatalf("command = %v", commands)
	}
	last := env.tg.messages[len(env.tg.messages)-1]
	if last.Text != "the plan" || last.Options.Keyboard == nil || last.Options.Keyboard[0][0].Text != planExecuteButton {
		t.Fatalf("plan response = %+v", last)
	}
	if _, ok := env.app.TakePendingPlanMessage(1); !ok {
		t.Fatal("pending plan not registered")
	}

	if err := env.app.CmdCode(env.ctx, env.message("/code build", 1), []string{"build"}); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || !strings.Contains(commands[1], "--agent build") || !strings.Contains(commands[1], "--session ses_1") {
		t.Fatalf("command = %v", commands)
	}
	last = env.tg.messages[len(env.tg.messages)-1]
	if last.Options.Keyboard != nil {
		t.Fatal("build responses must not carry the plan button")
	}
}

func TestOpenCodeRetriesAndResetsSession(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.app.SetSession(1, "repo", "old")
	oldDelay := opencodeRetryDelay
	opencodeRetryDelay = 0
	defer func() { opencodeRetryDelay = oldDelay }()
	calls := 0
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		calls++
		if calls < 3 {
			return docker.ExecResult{Output: []byte(`{"type":"error","error":{"data":{"message":"bad json"}}}`)}, nil
		}
		return docker.ExecResult{Output: opencodeJSON("recovered", "ses_new")}, nil
	}
	if err := env.app.CmdAsk(env.ctx, env.message("/ask x", 1), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || env.app.Session(1, "repo") != "ses_new" {
		t.Fatalf("calls=%d session=%q", calls, env.app.Session(1, "repo"))
	}

	calls = 0
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		calls++
		return docker.ExecResult{Output: []byte(`{"type":"error","error":{"data":{"message":"always"}}}`)}, nil
	}
	if err := env.app.CmdAsk(env.ctx, env.message("/ask y", 1), []string{"y"}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || env.app.Session(1, "repo") != "" {
		t.Fatalf("calls=%d session=%q", calls, env.app.Session(1, "repo"))
	}
	last := env.tg.messages[len(env.tg.messages)-1]
	if !strings.Contains(last.Text, "session was reset") {
		t.Fatalf("failure response = %q", last.Text)
	}
}

func TestThinkPersistsVariantAndOff(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	var commands []string
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		commands = append(commands, options.Cmd[len(options.Cmd)-1])
		return docker.ExecResult{Output: opencodeJSON("ok", "s")}, nil
	}
	env.app.SetRepoSelection("repo", "m1", env.app.Cfg.Catalog.DefaultVariant)
	if err := env.app.CmdThink(env.ctx, env.message("/think 2", 1), []string{"2"}); err != nil {
		t.Fatal(err)
	}
	repo, _ := env.app.Repo("repo")
	if repo.Variant == nil || *repo.Variant != "high" {
		t.Fatalf("variant = %+v", repo.Variant)
	}
	env.app.CmdAsk(env.ctx, env.message("/ask x", 1), []string{"x"})
	if !strings.Contains(commands[0], "--variant high") {
		t.Fatalf("command = %s", commands[0])
	}
	if err := env.app.CmdThink(env.ctx, env.message("/think off", 1), []string{"off"}); err != nil {
		t.Fatal(err)
	}
	env.app.CmdAsk(env.ctx, env.message("/ask y", 1), []string{"y"})
	if strings.Contains(commands[1], "--variant") {
		t.Fatalf("off command = %s", commands[1])
	}
}

func TestModelMenuFlow(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	if err := env.app.CmdModel(env.ctx, env.message("/model", 1), nil); err != nil {
		t.Fatal(err)
	}
	pending := env.app.GetModelPrompt(1)
	if pending == nil || pending.Stage != stageName {
		t.Fatalf("pending = %+v", pending)
	}
	token := pending.Token
	env.app.HandleModelCallback(env.ctx, env.callback("modelmenu:"+token+":name:0", 1))
	pending = env.app.GetModelPrompt(1)
	if pending == nil || pending.Stage != stageModel {
		t.Fatalf("after name = %+v", pending)
	}
	env.app.HandleModelCallback(env.ctx, env.callback("modelmenu:"+token+":model:0", 1))
	pending = env.app.GetModelPrompt(1)
	if pending == nil || pending.Stage != stageVariant {
		t.Fatalf("after model = %+v", pending)
	}
	env.app.HandleModelCallback(env.ctx, env.callback("modelmenu:"+token+":variant:1", 1))
	repo, _ := env.app.Repo("repo")
	if repo.Model != "m1" || repo.Variant == nil || *repo.Variant != "high" {
		t.Fatalf("selection = %+v", repo)
	}
	if env.app.GetModelPrompt(1) != nil {
		t.Fatal("prompt should be finished")
	}
}

func TestModelMenuAutoFinalizeAndCancel(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.app.CmdModel(env.ctx, env.message("/model", 1), nil)
	token := env.app.GetModelPrompt(1).Token
	env.app.HandleModelCallback(env.ctx, env.callback("modelmenu:"+token+":name:2", 1))
	repo, _ := env.app.Repo("repo")
	if repo.Model != "m4" {
		t.Fatalf("auto-finalized model = %q", repo.Model)
	}

	env.app.CmdModel(env.ctx, env.message("/model", 1), nil)
	env.app.CancelPendingModelPrompt(env.ctx, env.message("hello", 1))
	if env.app.GetModelPrompt(1) != nil {
		t.Fatal("new message should cancel the prompt")
	}
	if len(env.tg.edits) == 0 || !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "cancelled") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
}

func TestPlanExecuteCallback(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	var command string
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		command = options.Cmd[len(options.Cmd)-1]
		return docker.ExecResult{Output: opencodeJSON("done", "ses")}, nil
	}
	env.app.RegisterPendingPlan(1, 42, "tok")
	if err := env.app.HandlePlanExecuteCallback(env.ctx, env.callback("planexec:tok", 1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "--agent build") || !strings.Contains(command, planExecutePrompt) {
		t.Fatalf("command = %s", command)
	}
	if _, ok := env.app.TakePendingPlanMessage(1); ok {
		t.Fatal("plan should be consumed")
	}
	if len(env.tg.markups) == 0 || env.tg.markups[0].Keyboard != nil {
		t.Fatalf("plan button not removed: %+v", env.tg.markups)
	}

	if err := env.app.HandlePlanExecuteCallback(env.ctx, env.callback("planexec:stale", 1)); err != nil {
		t.Fatal(err)
	}
	if len(env.tg.answered) == 0 || !strings.Contains(env.tg.answered[len(env.tg.answered)-1], "no longer pending") {
		t.Fatalf("answers = %+v", env.tg.answered)
	}
}

func TestPhotoUploadsAndRunsBuild(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.tg.files["photo-1"] = []byte("image")
	var command string
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		command = options.Cmd[len(options.Cmd)-1]
		return docker.ExecResult{Output: opencodeJSON("seen", "s")}, nil
	}
	update := &telegram.Update{ChatID: 1, UserID: 1, Message: &telegram.Message{
		ID: 7, ChatID: 1,
		Caption: "/code fix this",
		Photo:   []telegram.PhotoSize{{FileID: "small"}, {FileID: "photo-1"}},
	}}
	if err := env.app.HandlePhoto(env.ctx, update, nil); err != nil {
		t.Fatal(err)
	}
	if len(env.shell.putPaths) != 1 || env.shell.putPaths[0] != "/workspace/repo" {
		t.Fatalf("put paths = %+v", env.shell.putPaths)
	}
	if !strings.Contains(command, "--agent build") || !strings.Contains(command, "Inspect the uploaded image") || !strings.Contains(command, "fix this") {
		t.Fatalf("command = %s", command)
	}
}

func TestDefaultMessageModesAndClear(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	var modes []string
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		modes = append(modes, options.Cmd[len(options.Cmd)-1])
		return docker.ExecResult{Output: opencodeJSON("ok", "s")}, nil
	}
	env.app.HandleDefaultMessage(env.ctx, env.message("plain question", 1), nil)
	env.app.HandleDefaultMessage(env.ctx, env.message("# build request", 1), nil)
	if len(modes) != 2 || !strings.Contains(modes[0], "--agent plan") || !strings.Contains(modes[1], "--agent build") {
		t.Fatalf("modes = %v", modes)
	}
	env.app.CmdClear(env.ctx, env.message("/clear", 1), nil)
	if env.app.Session(1, "repo") != "" {
		t.Fatal("session should be cleared")
	}
}

func TestOversizedPlanUsesFollowupButton(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		return docker.ExecResult{Output: opencodeJSON(strings.Repeat("p", 4001), "s")}, nil
	}
	if err := env.app.CmdAsk(env.ctx, env.message("/ask big", 1), []string{"big"}); err != nil {
		t.Fatal(err)
	}
	if len(env.tg.documents) != 1 {
		t.Fatalf("documents = %+v", env.tg.documents)
	}
	last := env.tg.messages[len(env.tg.messages)-1]
	if !strings.Contains(last.Text, "attached above") || last.Options.Keyboard == nil {
		t.Fatalf("followup = %+v", last)
	}
	if id, ok := env.app.TakePendingPlanMessage(1); !ok || id != env.tg.nextID {
		t.Fatalf("pending plan id = %d ok=%v next=%d", id, ok, env.tg.nextID)
	}
}
