package state

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"opencodebot/internal/config"
)

func catalog(t *testing.T) *config.Catalog {
	t.Helper()
	c, err := config.LoadCatalog(`{"version":1,"models":{"A":{"P":{"m1":{"name":"M1","variants":["low","high"]},"m2":{"name":"M2"}}}},"default_model":{"id":"m1","variant":"low"}}`)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestStoreRoundTripPreservesOrderAndValues(t *testing.T) {
	store, _ := openStore(t)
	m1 := "high"
	extra := map[string]json.RawMessage{"custom": json.RawMessage(`{"nested":true}`)}
	repos := []struct {
		name string
		repo *Repo
	}{
		{"zeta", &Repo{URL: "u1", ContainerID: "c1", ShellContainerID: "s1", Model: "m1", Variant: &m1, VariantSet: true}},
		{"alpha", &Repo{URL: "u2", ContainerID: "c2", Extra: extra}},
	}
	for _, entry := range repos {
		if err := store.UpsertRepo(entry.name, entry.repo); err != nil {
			t.Fatal(err)
		}
	}
	alpha, _, _ := store.Repo("alpha")
	alpha.Mounts = []string{"zeta"}
	if err := store.UpsertRepo("alpha", alpha); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveContext(7, "zeta"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCommandSet(7, &CommandSet{Name: "deploy", Commands: []string{"/sh make", "git status"}}); err != nil {
		t.Fatal(err)
	}

	keys, err := store.RepoKeys()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keys, ","); got != "zeta,alpha" {
		t.Fatalf("repo order = %s", got)
	}
	zeta, ok, err := store.Repo("zeta")
	if err != nil || !ok {
		t.Fatalf("zeta = %v ok=%v err=%v", zeta, ok, err)
	}
	if !zeta.VariantSet || zeta.Variant == nil || *zeta.Variant != "high" {
		t.Fatalf("variant lost: %+v", zeta)
	}
	if zeta.ContainerID != "c1" || zeta.ShellContainerID != "s1" {
		t.Fatalf("container ids lost: %+v", zeta)
	}
	alpha, _, _ = store.Repo("alpha")
	if len(alpha.Mounts) != 1 || alpha.Mounts[0] != "zeta" {
		t.Fatalf("mounts = %+v", alpha.Mounts)
	}
	if string(alpha.Extra["custom"]) != `{"nested":true}` {
		t.Fatalf("extra = %+v", alpha.Extra)
	}
	if name, err := store.ActiveName(7); err != nil || name != "zeta" {
		t.Fatalf("active = %q err=%v", name, err)
	}
	set, ok, err := store.CommandSet(7, "deploy")
	if err != nil || !ok || set == nil || len(set.Commands) != 2 {
		t.Fatalf("set = %+v ok=%v err=%v", set, ok, err)
	}
}

func TestStoreStartsEmptyAndUpdatesInPlace(t *testing.T) {
	store, _ := openStore(t)
	keys, err := store.RepoKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys = %v", keys)
	}
	if err := store.UpsertRepo("r", &Repo{URL: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepo("r", &Repo{URL: "two"}); err != nil {
		t.Fatal(err)
	}
	keys, _ = store.RepoKeys()
	if len(keys) != 1 || keys[0] != "r" {
		t.Fatalf("keys = %v", keys)
	}
	repo, _, _ := store.Repo("r")
	if repo.URL != "two" {
		t.Fatalf("repo = %+v", repo)
	}
}

func TestNormalizeInvalidValuesOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepo("a", &Repo{
		URL:        "u",
		Model:      "gone",
		VariantSet: true,
		Mounts:     []string{"b", "b", "a", "missing"},
		Extra:      map[string]json.RawMessage{"commands": json.RawMessage(`[]`), "keep": json.RawMessage(`1`)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepo("b", &Repo{
		URL:        "u",
		Model:      "m1",
		Variant:    stringPointer("stale"),
		VariantSet: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	a, _, _ := store.Repo("a")
	if a.Model != "" || a.VariantSet || a.Variant != nil {
		t.Fatalf("a model not cleared: %+v", a)
	}
	if len(a.Mounts) != 1 || a.Mounts[0] != "b" {
		t.Fatalf("a mounts = %+v", a.Mounts)
	}
	if _, ok := a.Extra["commands"]; ok {
		t.Fatalf("legacy commands kept: %+v", a.Extra)
	}
	if string(a.Extra["keep"]) != "1" {
		t.Fatalf("extra lost: %+v", a.Extra)
	}
	b, _, _ := store.Repo("b")
	if b.VariantSet || b.Variant != nil || b.Model != "m1" {
		t.Fatalf("b = %+v", b)
	}
}

func stringPointer(value string) *string { return &value }

func TestDeleteRepoCascades(t *testing.T) {
	store, _ := openStore(t)
	if err := store.UpsertRepo("a", &Repo{URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepo("b", &Repo{URL: "u", Mounts: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveContext(1, "a"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRepo("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Repo("a"); ok {
		t.Fatal("repo still present")
	}
	if name, err := store.ActiveName(1); err != nil || name != "" {
		t.Fatalf("active = %q err=%v", name, err)
	}
}

func TestCommandSetsReplaceAndDelete(t *testing.T) {
	store, _ := openStore(t)
	if err := store.SaveCommandSet(1, &CommandSet{Name: "one", Commands: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCommandSet(1, &CommandSet{Name: "two", Commands: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCommandSet(1, &CommandSet{Name: "one", Commands: []string{"c", "d"}}); err != nil {
		t.Fatal(err)
	}
	names, err := store.CommandSetNames(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "one,two" {
		t.Fatalf("names = %s", got)
	}
	set, _, _ := store.CommandSet(1, "one")
	if got := strings.Join(set.Commands, ","); got != "c,d" {
		t.Fatalf("commands = %s", got)
	}
	removed, ok, err := store.DeleteCommandSet(1, "one")
	if err != nil || !ok || removed == nil || len(removed.Commands) != 2 {
		t.Fatalf("removed = %+v ok=%v err=%v", removed, ok, err)
	}
	if _, ok, _ := store.CommandSet(1, "one"); ok {
		t.Fatal("set still present")
	}
	if count, err := store.CommandSetsCount(1); err != nil || count != 1 {
		t.Fatalf("count = %d err=%v", count, err)
	}
}

func TestConcurrentStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	var wait sync.WaitGroup
	errs := make(chan error, 20)
	for index := 0; index < 10; index++ {
		store := first
		if index%2 == 1 {
			store = second
		}
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			name := "repo-" + string(rune('a'+index))
			if err := store.UpsertRepo(name, &Repo{URL: "u"}); err != nil {
				errs <- err
				return
			}
			if err := store.SaveCommandSet(1, &CommandSet{Name: name, Commands: []string{"cmd"}}); err != nil {
				errs <- err
			}
		}(index, store)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	keys, err := first.RepoKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 10 {
		t.Fatalf("keys = %v", keys)
	}
	if count, err := first.CommandSetsCount(1); err != nil || count != 10 {
		t.Fatalf("count = %d err=%v", count, err)
	}
}
