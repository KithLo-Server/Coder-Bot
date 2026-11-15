// Package state persists repositories, per-chat active repos and command sets.
package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"

	"opencodebot/internal/config"
	"opencodebot/internal/ordered"
)

// Repo is one configured repository.
type Repo struct {
	URL              string
	ContainerID      string
	ShellContainerID string
	Model            string
	Variant          *string
	VariantSet       bool
	Mounts           []string
	Extra            map[string]json.RawMessage
}

// UnmarshalJSON reads a repository while keeping unknown keys.
func (r *Repo) UnmarshalJSON(data []byte) error {
	_, fields, err := config.DecodeObject(data)
	if err != nil {
		return err
	}
	*r = Repo{Extra: make(map[string]json.RawMessage)}
	for key, raw := range fields {
		switch key {
		case "url":
			if err := json.Unmarshal(raw, &r.URL); err != nil {
				return err
			}
		case "container_id":
			if err := json.Unmarshal(raw, &r.ContainerID); err != nil {
				return err
			}
		case "shell_container_id":
			if err := json.Unmarshal(raw, &r.ShellContainerID); err != nil {
				return err
			}
		case "model":
			if string(raw) != "null" {
				if err := json.Unmarshal(raw, &r.Model); err != nil {
					return err
				}
			}
		case "variant":
			r.VariantSet = true
			if string(raw) != "null" {
				var variant string
				if err := json.Unmarshal(raw, &variant); err != nil {
					return err
				}
				r.Variant = &variant
			}
		case "mounts":
			if string(raw) != "null" {
				var mounts []string
				if err := json.Unmarshal(raw, &mounts); err == nil {
					r.Mounts = mounts
				}
			}
		default:
			r.Extra[key] = append(json.RawMessage(nil), raw...)
		}
	}
	return nil
}

// MarshalJSON writes known fields and keeps unknown keys.
func (r Repo) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	write := func(key string, value any) error {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		encodedKey, _ := json.Marshal(key)
		buf.Write(encodedKey)
		buf.WriteByte(':')
		encodedValue, err := json.Marshal(value)
		if err != nil {
			return err
		}
		buf.Write(encodedValue)
		return nil
	}
	if err := write("url", r.URL); err != nil {
		return nil, err
	}
	if err := write("container_id", r.ContainerID); err != nil {
		return nil, err
	}
	if err := write("shell_container_id", r.ShellContainerID); err != nil {
		return nil, err
	}
	if len(r.Mounts) > 0 {
		if err := write("mounts", r.Mounts); err != nil {
			return nil, err
		}
	}
	if r.Model != "" {
		if err := write("model", r.Model); err != nil {
			return nil, err
		}
	}
	if r.VariantSet {
		if err := write("variant", r.Variant); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(r.Extra))
	for key := range r.Extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		encodedKey, _ := json.Marshal(key)
		buf.Write(encodedKey)
		buf.WriteByte(':')
		buf.Write(r.Extra[key])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// CommandSet is a named list of saved command lines.
type CommandSet struct {
	Name     string
	Commands []string
}

// State is the persistent part of the bot's state.
type State struct {
	Repos         *ordered.Map[string, *Repo]
	ActiveContext map[int64]string
	CommandSets   map[int64]*ordered.Map[string, *CommandSet]
}

// New returns empty state.
func New() *State {
	return &State{
		Repos:         ordered.New[string, *Repo](),
		ActiveContext: make(map[int64]string),
		CommandSets:   make(map[int64]*ordered.Map[string, *CommandSet]),
	}
}

// ChatSets returns the command sets of a chat, creating the mapping if needed.
func (s *State) ChatSets(chatID int64) *ordered.Map[string, *CommandSet] {
	sets, ok := s.CommandSets[chatID]
	if !ok {
		sets = ordered.New[string, *CommandSet]()
		s.CommandSets[chatID] = sets
	}
	return sets
}

// Load reads the state file. A missing file leaves the state empty.
func Load(path string, catalog *config.Catalog) (*State, error) {
	result := New()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}

	rootKeys, root, err := config.DecodeObject(raw)
	if err != nil {
		return nil, fmt.Errorf("expected a JSON object: %w", err)
	}
	_ = rootKeys

	version := 0
	if rawVersion, ok := root["version"]; ok && string(rawVersion) != "null" {
		_ = json.Unmarshal(rawVersion, &version)
	}

	reposRaw, hasRepos := root["repos"]
	if version >= 2 && hasRepos && string(reposRaw) != "null" {
		if err := loadRepos(result, reposRaw); err != nil {
			return nil, err
		}
		loadCommandSets(result, root["command_sets"])
		loadActiveContext(result, root["active_context"])
	} else {
		// Legacy state stored repositories directly and kept /cmd sets inside each repo.
		if err := loadRepos(result, raw); err != nil {
			return nil, err
		}
	}

	normalize(result, catalog, version)
	return result, nil
}

func loadRepos(result *State, raw json.RawMessage) error {
	keys, values, err := config.DecodeObject(raw)
	if err != nil {
		return fmt.Errorf("repos must be an object: %w", err)
	}
	for _, name := range keys {
		repo := &Repo{}
		if err := json.Unmarshal(values[name], repo); err != nil {
			// Match the Python loader, which ignores entries that are not objects.
			continue
		}
		result.Repos.Set(name, repo)
	}
	return nil
}

func loadCommandSets(result *State, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	chatKeys, chats, err := config.DecodeObject(raw)
	if err != nil {
		return
	}
	for _, chatKey := range chatKeys {
		chatID, err := strconv.ParseInt(chatKey, 10, 64)
		if err != nil {
			continue
		}
		setKeys, setValues, err := config.DecodeObject(chats[chatKey])
		if err != nil {
			continue
		}
		sets := result.ChatSets(chatID)
		for _, name := range setKeys {
			var decoded struct {
				Name     string   `json:"name"`
				Commands []string `json:"commands"`
			}
			if err := json.Unmarshal(setValues[name], &decoded); err != nil {
				continue
			}
			if decoded.Name == "" {
				decoded.Name = name
			}
			sets.Set(name, &CommandSet{Name: decoded.Name, Commands: decoded.Commands})
		}
	}
}

func loadActiveContext(result *State, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	chatKeys, chats, err := config.DecodeObject(raw)
	if err != nil {
		return
	}
	for _, chatKey := range chatKeys {
		chatID, err := strconv.ParseInt(chatKey, 10, 64)
		if err != nil {
			continue
		}
		var repoName string
		if err := json.Unmarshal(chats[chatKey], &repoName); err != nil {
			continue
		}
		if _, ok := result.Repos.Get(repoName); ok {
			result.ActiveContext[chatID] = repoName
		}
	}
}

func normalize(s *State, catalog *config.Catalog, version int) {
	s.Repos.Range(func(name string, repo *Repo) bool {
		if version < 3 {
			clearModelSelection(repo)
		} else {
			normalizeModelSelection(repo, catalog)
		}
		if version < 4 {
			repo.ContainerID = ""
			repo.ShellContainerID = ""
		}
		normalizeMounts(name, repo, s.Repos)
		delete(repo.Extra, "commands")
		return true
	})
}

func clearModelSelection(repo *Repo) {
	repo.Model = ""
	repo.Variant = nil
	repo.VariantSet = false
}

func normalizeModelSelection(repo *Repo, catalog *config.Catalog) {
	if repo.Model == "" || catalog.Entry(repo.Model) == nil {
		clearModelSelection(repo)
		return
	}
	if !repo.VariantSet || repo.Variant == nil {
		return
	}
	variant := *repo.Variant
	for _, configured := range catalog.Variants(repo.Model) {
		if configured == variant {
			return
		}
	}
	if catalog.DefaultVariant != nil && variant == *catalog.DefaultVariant {
		return
	}
	repo.Variant = nil
	repo.VariantSet = false
}

func normalizeMounts(name string, repo *Repo, repos *ordered.Map[string, *Repo]) {
	if repo.Mounts == nil {
		delete(repo.Extra, "mounts")
		return
	}
	seen := make(map[string]bool)
	var mounts []string
	for _, mount := range repo.Mounts {
		if mount == name {
			continue
		}
		if _, ok := repos.Get(mount); !ok {
			continue
		}
		if !seen[mount] {
			seen[mount] = true
			mounts = append(mounts, mount)
		}
	}
	repo.Mounts = mounts
}

// Save writes the state file in the version-3 format.
func Save(path string, s *State) error {
	compact, err := marshalState(s)
	if err != nil {
		return err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact, "", "  "); err != nil {
		return err
	}
	return os.WriteFile(path, indented.Bytes(), 0o644)
}

func marshalState(s *State) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`{"version":4,"repos":{`)
	firstRepo := true
	s.Repos.Range(func(name string, repo *Repo) bool {
		if !firstRepo {
			buf.WriteByte(',')
		}
		firstRepo = false
		writeJSON(&buf, name)
		buf.WriteByte(':')
		encoded, err := repo.MarshalJSON()
		if err != nil {
			buf.WriteString("null")
			return false
		}
		buf.Write(encoded)
		return true
	})
	buf.WriteString(`},"command_sets":{`)
	chatIDs := make([]int64, 0, len(s.CommandSets))
	for chatID := range s.CommandSets {
		chatIDs = append(chatIDs, chatID)
	}
	sort.Slice(chatIDs, func(i, j int) bool { return chatIDs[i] < chatIDs[j] })
	for index, chatID := range chatIDs {
		if index > 0 {
			buf.WriteByte(',')
		}
		writeJSON(&buf, strconv.FormatInt(chatID, 10))
		buf.WriteByte(':')
		buf.WriteByte('{')
		firstSet := true
		s.CommandSets[chatID].Range(func(name string, set *CommandSet) bool {
			if !firstSet {
				buf.WriteByte(',')
			}
			firstSet = false
			writeJSON(&buf, name)
			buf.WriteByte(':')
			encoded, _ := json.Marshal(struct {
				Name     string   `json:"name"`
				Commands []string `json:"commands"`
			}{Name: set.Name, Commands: set.Commands})
			buf.Write(encoded)
			return true
		})
		buf.WriteByte('}')
	}
	buf.WriteString(`},"active_context":{`)
	activeIDs := make([]int64, 0, len(s.ActiveContext))
	for chatID := range s.ActiveContext {
		activeIDs = append(activeIDs, chatID)
	}
	sort.Slice(activeIDs, func(i, j int) bool { return activeIDs[i] < activeIDs[j] })
	for index, chatID := range activeIDs {
		if index > 0 {
			buf.WriteByte(',')
		}
		writeJSON(&buf, strconv.FormatInt(chatID, 10))
		buf.WriteByte(':')
		writeJSON(&buf, s.ActiveContext[chatID])
	}
	buf.WriteString("}}")
	return buf.Bytes(), nil
}

func writeJSON(buf *bytes.Buffer, value any) {
	encoded, _ := json.Marshal(value)
	buf.Write(encoded)
}
