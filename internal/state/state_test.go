package state

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSaveLoadRoundTripPreservesOrderAndVariant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := New()
	m1 := "high"
	s.Repos.Set("zeta", &Repo{URL: "u1", ContainerID: "c1", ShellContainerID: "s1", Model: "m1", Variant: &m1, VariantSet: true})
	s.Repos.Set("alpha", &Repo{URL: "u2", ContainerID: "c2"})
	s.Repos.Get("alpha")
	alpha, _ := s.Repos.Get("alpha")
	alpha.Mounts = []string{"zeta"}
	s.ActiveContext[7] = "zeta"
	s.ChatSets(7).Set("deploy", &CommandSet{Name: "deploy", Commands: []string{"/sh make", "git status"}})

	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(loaded.Repos.Keys(), ","); got != "zeta,alpha" {
		t.Fatalf("repo order = %s", got)
	}
	zeta, _ := loaded.Repos.Get("zeta")
	if !zeta.VariantSet || zeta.Variant == nil || *zeta.Variant != "high" {
		t.Fatalf("variant lost: %+v", zeta)
	}
	if zeta.ContainerID != "c1" || zeta.ShellContainerID != "s1" {
		t.Fatalf("container ids lost: %+v", zeta)
	}
	if loaded.ActiveContext[7] != "zeta" {
		t.Fatalf("active = %+v", loaded.ActiveContext)
	}
	set, _ := loaded.CommandSets[7].Get("deploy")
	if set == nil || len(set.Commands) != 2 {
		t.Fatalf("sets = %+v", loaded.CommandSets[7])
	}
}

func TestLoadVersion2ClearsModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"version":2,"repos":{"r":{"url":"u","container_id":"c","model":"m1","variant":"high"}},"command_sets":{},"active_context":{}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := loaded.Repos.Get("r")
	if repo.Model != "" || repo.VariantSet {
		t.Fatalf("model not cleared: %+v", repo)
	}
	if repo.ContainerID != "" || repo.ShellContainerID != "" {
		t.Fatalf("stale container ids kept: %+v", repo)
	}
}

func TestLoadNormalizesModelMountsAndContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"version":3,"repos":{
		"a":{"url":"u","container_id":"c","model":"gone","variant":"high","mounts":["b","b","a","missing"]},
		"b":{"url":"u","container_id":"c","model":"m1","variant":"stale"}},
		"command_sets":{"bad":{},"9":{"x":{"name":"x","commands":["ls"]}}},
		"active_context":{"9":"b","10":"missing"}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := loaded.Repos.Get("a")
	if a.Model != "" || a.VariantSet || len(a.Mounts) != 1 || a.Mounts[0] != "b" {
		t.Fatalf("a = %+v", a)
	}
	if a.ContainerID != "" || a.ShellContainerID != "" {
		t.Fatalf("v3 container ids should be migrated away: %+v", a)
	}
	b, _ := loaded.Repos.Get("b")
	if b.VariantSet || b.Variant != nil {
		t.Fatalf("stale variant not dropped: %+v", b)
	}
	if loaded.ActiveContext[9] != "b" || len(loaded.ActiveContext) != 1 {
		t.Fatalf("active = %+v", loaded.ActiveContext)
	}
	if _, ok := loaded.CommandSets[9]; !ok {
		t.Fatal("valid command set missing")
	}
	if _, ok := loaded.CommandSets[0]; ok {
		t.Fatal("invalid chat key accepted")
	}
}

func TestLoadVersion3PreservesModelAndClearsContainerIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"version":3,"repos":{"r":{"url":"u","container_id":"c","shell_container_id":"s","model":"m1","variant":"high"}},"command_sets":{},"active_context":{}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := loaded.Repos.Get("r")
	if repo.Model != "m1" || repo.Variant == nil || *repo.Variant != "high" {
		t.Fatalf("model selection not preserved: %+v", repo)
	}
	if repo.ContainerID != "" || repo.ShellContainerID != "" {
		t.Fatalf("container ids not cleared: %+v", repo)
	}
}
