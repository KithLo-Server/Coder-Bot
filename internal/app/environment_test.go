package app

import "testing"

func TestOpencodeAndWorkerEnvironment(t *testing.T) {
	env := newTestEnv(t)
	env.app.Cfg.OpenRouterKey = "openrouter"
	env.app.Cfg.VertexProject = "project"
	env.app.Cfg.VertexLocation = "europe"
	env.app.Cfg.CredentialsJSON = `{"type":"service_account"}`
	env.app.Cfg.SSHKey = "ssh-key"
	env.app.Cfg.GitUserName = "Bot"
	env.app.Cfg.GitUserEmail = "bot@example.com"

	opencodeEnv := env.app.OpencodeEnvironment()
	want := map[string]string{
		"DEEPSEEK_API_KEY":               "key",
		"OPENROUTER_API_KEY":             "openrouter",
		"GOOGLE_VERTEX_PROJECT":          "project",
		"GOOGLE_CLOUD_PROJECT":           "project",
		"GOOGLE_VERTEX_LOCATION":         "europe",
		"GOOGLE_CLOUD_LOCATION":          "europe",
		"VERTEX_LOCATION":                "europe",
		"GOOGLE_APPLICATION_CREDENTIALS": "/root/.gcp-credentials.json",
	}
	for key, value := range want {
		if opencodeEnv[key] != value {
			t.Fatalf("%s = %q, want %q (%+v)", key, opencodeEnv[key], value, opencodeEnv)
		}
	}
	if _, ok := opencodeEnv["OPENCODE_SSH_KEY"]; ok {
		t.Fatal("worker-only secrets must not be exposed to every command")
	}

	workerEnv := env.app.WorkerEnvironment()
	for key, value := range map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS_JSON": `{"type":"service_account"}`,
		"OPENCODE_SSH_KEY":                    "ssh-key",
		"GIT_USER_NAME":                       "Bot",
		"GIT_USER_EMAIL":                      "bot@example.com",
	} {
		if workerEnv[key] != value {
			t.Fatalf("%s = %q, want %q", key, workerEnv[key], value)
		}
	}
}
