// Package state persists repositories, per-chat active repos and command sets in SQLite.
package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"opencodebot/internal/config"

	_ "modernc.org/sqlite"
)

const schemaVersion = 2

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

// CommandSet is a named list of saved command lines.
type CommandSet struct {
	Name     string
	Commands []string
}

// Store is the SQLite-backed source of truth for persistent bot state.
type Store struct {
	db *sql.DB
}

// Open opens the state database, creates its schema when needed and validates
// persisted selections against the configured model catalog.
func Open(path string, catalog *config.Catalog) (*Store, error) {
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.Normalize(catalog); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	statements := []string{
		`CREATE TABLE IF NOT EXISTS repos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			url TEXT NOT NULL DEFAULT '',
			container_id TEXT NOT NULL DEFAULT '',
			shell_container_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			variant TEXT,
			variant_set INTEGER NOT NULL DEFAULT 0,
			extra TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE TABLE IF NOT EXISTS repo_mounts (
			repo_name TEXT NOT NULL REFERENCES repos(name) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			mount TEXT NOT NULL,
			PRIMARY KEY (repo_name, position)
		)`,
		`CREATE TABLE IF NOT EXISTS active_context (
			chat_id INTEGER PRIMARY KEY,
			repo_name TEXT NOT NULL REFERENCES repos(name) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			chat_id INTEGER NOT NULL,
			repo_name TEXT NOT NULL REFERENCES repos(name) ON DELETE CASCADE,
			session_id TEXT NOT NULL,
			PRIMARY KEY (chat_id, repo_name)
		)`,
		`CREATE TABLE IF NOT EXISTS command_sets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			UNIQUE (chat_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS command_set_commands (
			chat_id INTEGER NOT NULL,
			set_name TEXT NOT NULL,
			position INTEGER NOT NULL,
			command TEXT NOT NULL,
			PRIMARY KEY (chat_id, set_name, position),
			FOREIGN KEY (chat_id, set_name) REFERENCES command_sets(chat_id, name) ON DELETE CASCADE
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}

	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch {
	case version > schemaVersion:
		return fmt.Errorf("unsupported state database version %d", version)
	case version < schemaVersion:
		// Schema changes so far are additive and handled by the idempotent
		// CREATE TABLE statements above.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Normalize validates stored model selections, mounts and active contexts.
// It does not keep any state in memory after it returns.
func (s *Store) Normalize(catalog *config.Catalog) error {
	keys, err := s.RepoKeys()
	if err != nil {
		return err
	}
	names := make(map[string]bool, len(keys))
	for _, name := range keys {
		names[name] = true
	}
	for _, name := range keys {
		repo, ok, err := s.Repo(name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		changed := false
		if catalog != nil {
			model, variant, variantSet := repo.Model, repo.Variant, repo.VariantSet
			normalizeModelSelection(repo, catalog)
			if repo.Model != model || !sameVariant(repo.Variant, variant) || repo.VariantSet != variantSet {
				changed = true
			}
		}
		mounts := normalizeMounts(name, repo.Mounts, names)
		if !sameStrings(repo.Mounts, mounts) {
			repo.Mounts = mounts
			changed = true
		}
		if _, ok := repo.Extra["commands"]; ok {
			delete(repo.Extra, "commands")
			changed = true
		}
		if changed {
			if err := s.UpsertRepo(name, repo); err != nil {
				return err
			}
		}
	}
	_, err = s.db.Exec(`DELETE FROM active_context WHERE repo_name NOT IN (SELECT name FROM repos)`)
	return err
}

func sameVariant(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func normalizeModelSelection(repo *Repo, catalog *config.Catalog) {
	if repo.Model == "" || catalog.Entry(repo.Model) == nil {
		repo.Model = ""
		repo.Variant = nil
		repo.VariantSet = false
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

func normalizeMounts(name string, mounts []string, repos map[string]bool) []string {
	if mounts == nil {
		return nil
	}
	seen := make(map[string]bool)
	result := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		if mount == name || !repos[mount] || seen[mount] {
			continue
		}
		seen[mount] = true
		result = append(result, mount)
	}
	return result
}

// Repo returns a repository by name.
func (s *Store) Repo(name string) (*Repo, bool, error) {
	repo := &Repo{Extra: make(map[string]json.RawMessage)}
	var variant sql.NullString
	var variantSet int
	var extra string
	err := s.db.QueryRow(`SELECT url, container_id, shell_container_id, model, variant, variant_set, extra
		FROM repos WHERE name = ?`, name).
		Scan(&repo.URL, &repo.ContainerID, &repo.ShellContainerID, &repo.Model, &variant, &variantSet, &extra)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	if variant.Valid {
		value := variant.String
		repo.Variant = &value
	}
	repo.VariantSet = variantSet != 0
	if err := json.Unmarshal([]byte(extra), &repo.Extra); err != nil {
		return nil, false, fmt.Errorf("decode repo %q extra: %w", name, err)
	}
	if repo.Extra == nil {
		repo.Extra = make(map[string]json.RawMessage)
	}
	mounts, err := s.repoMounts(name)
	if err != nil {
		return nil, false, err
	}
	repo.Mounts = mounts
	return repo, true, nil
}

func (s *Store) repoMounts(name string) ([]string, error) {
	rows, err := s.db.Query(`SELECT mount FROM repo_mounts WHERE repo_name = ? ORDER BY position`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mounts []string
	for rows.Next() {
		var mount string
		if err := rows.Scan(&mount); err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	return mounts, rows.Err()
}

// RepoExists reports whether a repository exists.
func (s *Store) RepoExists(name string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM repos WHERE name = ?`, name).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// RepoKeys returns repository names in insertion order.
func (s *Store) RepoKeys() ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM repos ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// UpsertRepo stores a repository and replaces its mounts atomically.
func (s *Store) UpsertRepo(name string, repo *Repo) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	extra, err := encodeExtra(repo.Extra)
	if err != nil {
		return err
	}
	var variant any
	if repo.Variant != nil {
		variant = *repo.Variant
	}
	variantSet := 0
	if repo.VariantSet {
		variantSet = 1
	}
	if _, err := tx.Exec(`INSERT INTO repos
		(name, url, container_id, shell_container_id, model, variant, variant_set, extra)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			url = excluded.url,
			container_id = excluded.container_id,
			shell_container_id = excluded.shell_container_id,
			model = excluded.model,
			variant = excluded.variant,
			variant_set = excluded.variant_set,
			extra = excluded.extra`,
		name, repo.URL, repo.ContainerID, repo.ShellContainerID, repo.Model, variant, variantSet, extra); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM repo_mounts WHERE repo_name = ?`, name); err != nil {
		return err
	}
	for position, mount := range repo.Mounts {
		if _, err := tx.Exec(`INSERT INTO repo_mounts (repo_name, position, mount) VALUES (?, ?, ?)`,
			name, position, mount); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func encodeExtra(extra map[string]json.RawMessage) (string, error) {
	if len(extra) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(extra)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// DeleteRepo removes a repository and its related mounts and active contexts.
func (s *Store) DeleteRepo(name string) error {
	_, err := s.db.Exec(`DELETE FROM repos WHERE name = ?`, name)
	return err
}

// SetRepoSelection stores a validated model/variant selection.
func (s *Store) SetRepoSelection(name, modelID string, variant *string) error {
	var value any
	if variant != nil {
		value = *variant
	}
	_, err := s.db.Exec(`INSERT INTO repos (name, model, variant, variant_set)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(name) DO UPDATE SET
			model = excluded.model,
			variant = excluded.variant,
			variant_set = 1`,
		name, modelID, value)
	return err
}

// UpdateRepoContainerIDs stores the container ids of a repository.
func (s *Store) UpdateRepoContainerIDs(name, workerID, shellID string) error {
	_, err := s.db.Exec(`UPDATE repos SET container_id = ?, shell_container_id = ? WHERE name = ?`,
		workerID, shellID, name)
	return err
}

// ActiveName returns a chat's persisted active repository name.
func (s *Store) ActiveName(chatID int64) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT repo_name FROM active_context WHERE chat_id = ?`, chatID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}

// ActiveRepo returns the chat's active repository if it still exists.
func (s *Store) ActiveRepo(chatID int64) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT ac.repo_name FROM active_context ac
		JOIN repos r ON r.name = ac.repo_name WHERE ac.chat_id = ?`, chatID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}

// SetActiveContext stores a chat's active repository.
func (s *Store) SetActiveContext(chatID int64, name string) error {
	_, err := s.db.Exec(`INSERT INTO active_context (chat_id, repo_name) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET repo_name = excluded.repo_name`, chatID, name)
	return err
}

// ClearActiveContext removes a chat's active repository.
func (s *Store) ClearActiveContext(chatID int64) error {
	_, err := s.db.Exec(`DELETE FROM active_context WHERE chat_id = ?`, chatID)
	return err
}

// ActiveContexts returns a snapshot of the per-chat active repositories.
func (s *Store) ActiveContexts() (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT chat_id, repo_name FROM active_context`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]string)
	for rows.Next() {
		var chatID int64
		var name string
		if err := rows.Scan(&chatID, &name); err != nil {
			return nil, err
		}
		result[chatID] = name
	}
	return result, rows.Err()
}

// Session returns a chat's OpenCode session for a repository.
func (s *Store) Session(chatID int64, repoName string) (string, error) {
	var sessionID string
	err := s.db.QueryRow(`SELECT session_id FROM sessions WHERE chat_id = ? AND repo_name = ?`,
		chatID, repoName).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return sessionID, err
}

// SetSession stores a chat's OpenCode session for a repository.
func (s *Store) SetSession(chatID int64, repoName, sessionID string) error {
	_, err := s.db.Exec(`INSERT INTO sessions (chat_id, repo_name, session_id) VALUES (?, ?, ?)
		ON CONFLICT(chat_id, repo_name) DO UPDATE SET session_id = excluded.session_id`,
		chatID, repoName, sessionID)
	return err
}

// ForgetSession removes a chat's OpenCode session for a repository.
func (s *Store) ForgetSession(chatID int64, repoName string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE chat_id = ? AND repo_name = ?`, chatID, repoName)
	return err
}

// ForgetRepoSessions removes every OpenCode session for a repository.
func (s *Store) ForgetRepoSessions(repoName string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE repo_name = ?`, repoName)
	return err
}

// ChatHasSessions reports whether a chat has any stored OpenCode sessions.
func (s *Store) ChatHasSessions(chatID int64) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM sessions WHERE chat_id = ? LIMIT 1`, chatID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ForgetChatSessions removes every OpenCode session for a chat.
func (s *Store) ForgetChatSessions(chatID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE chat_id = ?`, chatID)
	return err
}

// CommandSet returns a chat's saved command set.
func (s *Store) CommandSet(chatID int64, name string) (*CommandSet, bool, error) {
	var displayName string
	err := s.db.QueryRow(`SELECT display_name FROM command_sets WHERE chat_id = ? AND name = ?`, chatID, name).
		Scan(&displayName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	commands, err := s.commandSetCommands(chatID, name)
	if err != nil {
		return nil, false, err
	}
	return &CommandSet{Name: displayName, Commands: commands}, true, nil
}

func (s *Store) commandSetCommands(chatID int64, name string) ([]string, error) {
	rows, err := s.db.Query(`SELECT command FROM command_set_commands
		WHERE chat_id = ? AND set_name = ? ORDER BY position`, chatID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var commands []string
	for rows.Next() {
		var command string
		if err := rows.Scan(&command); err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

// CommandSetNames returns a chat's saved command set names in insertion order.
func (s *Store) CommandSetNames(chatID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM command_sets WHERE chat_id = ? ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// SaveCommandSet stores a command set and replaces its commands atomically.
func (s *Store) SaveCommandSet(chatID int64, set *CommandSet) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO command_sets (chat_id, name, display_name) VALUES (?, ?, ?)
		ON CONFLICT(chat_id, name) DO UPDATE SET display_name = excluded.display_name`,
		chatID, set.Name, set.Name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM command_set_commands WHERE chat_id = ? AND set_name = ?`,
		chatID, set.Name); err != nil {
		return err
	}
	for position, command := range set.Commands {
		if _, err := tx.Exec(`INSERT INTO command_set_commands (chat_id, set_name, position, command)
			VALUES (?, ?, ?, ?)`, chatID, set.Name, position, command); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteCommandSet removes and returns a chat's command set.
func (s *Store) DeleteCommandSet(chatID int64, name string) (*CommandSet, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var displayName string
	err = tx.QueryRow(`SELECT display_name FROM command_sets WHERE chat_id = ? AND name = ?`, chatID, name).
		Scan(&displayName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	rows, err := tx.Query(`SELECT command FROM command_set_commands
		WHERE chat_id = ? AND set_name = ? ORDER BY position`, chatID, name)
	if err != nil {
		return nil, false, err
	}
	var commands []string
	for rows.Next() {
		var command string
		if err := rows.Scan(&command); err != nil {
			rows.Close()
			return nil, false, err
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	if _, err := tx.Exec(`DELETE FROM command_sets WHERE chat_id = ? AND name = ?`, chatID, name); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &CommandSet{Name: displayName, Commands: commands}, true, nil
}

// CommandSetsCount returns how many sets a chat has.
func (s *Store) CommandSetsCount(chatID int64) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM command_sets WHERE chat_id = ?`, chatID).Scan(&count)
	return count, err
}

// CommandSets returns a snapshot of a chat's sets.
func (s *Store) CommandSets(chatID int64) (map[string]*CommandSet, error) {
	names, err := s.CommandSetNames(chatID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*CommandSet, len(names))
	for _, name := range names {
		set, ok, err := s.CommandSet(chatID, name)
		if err != nil {
			return nil, err
		}
		if ok {
			result[name] = set
		}
	}
	return result, nil
}
