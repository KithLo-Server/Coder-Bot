package app

import (
	"testing"

	"opencodebot/internal/state"
)

func TestAppReadsRepositoryStateFromStore(t *testing.T) {
	env := newTestEnv(t)
	if err := env.app.SetRepo("repo", &state.Repo{URL: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := env.app.Store.UpsertRepo("repo", &state.Repo{URL: "second"}); err != nil {
		t.Fatal(err)
	}
	repo, ok := env.app.Repo("repo")
	if !ok || repo.URL != "second" {
		t.Fatalf("repo = %+v ok=%v", repo, ok)
	}
}
