// Package app implements the Telegram bot handlers.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"sync"

	"opencodebot/internal/base"
	"opencodebot/internal/config"
	"opencodebot/internal/docker"
	"opencodebot/internal/locks"
	"opencodebot/internal/state"
	"opencodebot/internal/telegram"
)

// HandlerFunc handles one bot command with its split arguments.
type HandlerFunc func(*App, context.Context, *telegram.Update, []string) error

// ModelPrompt is an in-progress interactive /model selection.
type ModelPrompt struct {
	Token         string
	MessageID     int
	ChatID        int64
	Repo          string
	Stage         string
	NameIndex     int
	ProviderIndex int
	ModelIndex    int
	HasName       bool
	HasProvider   bool
	HasModel      bool
}

// PendingCommand is an in-progress /cmd collection.
type PendingCommand struct {
	Name     string
	Commands []string
	Editing  bool
}

// PendingPlan is the newest plan response awaiting execution.
type PendingPlan struct {
	Token     string
	MessageID int
}

type runEntry struct {
	RunID       string
	ChatID      int64
	ContainerID string
	Cancelled   bool
}

// App holds configuration, state, adapters and all in-memory session data.
type App struct {
	Cfg      *config.Config
	State    *state.State
	Telegram telegram.Messenger
	Docker   docker.API
	Locks    *locks.Manager
	Logger   *log.Logger

	baseContainer docker.Container

	mu sync.Mutex

	sessions              map[int64]map[string]string
	modelPrompts          map[int64]*ModelPrompt
	pendingCommandEntries map[int64]*PendingCommand
	commandSetButtons     map[int64]map[int]bool
	pendingPlans          map[int64]*PendingPlan
	runningCommands       map[string]*runEntry
	cancelGenerations     map[string]int
}

// New builds an app with empty in-memory state.
func New(cfg *config.Config, st *state.State, messenger telegram.Messenger, dockerAPI docker.API, logger *log.Logger) *App {
	if logger == nil {
		logger = log.Default()
	}
	return &App{
		Cfg:                   cfg,
		State:                 st,
		Telegram:              messenger,
		Docker:                dockerAPI,
		Locks:                 locks.NewManager(),
		Logger:                logger,
		baseContainer:         base.New(),
		sessions:              make(map[int64]map[string]string),
		modelPrompts:          make(map[int64]*ModelPrompt),
		pendingCommandEntries: make(map[int64]*PendingCommand),
		commandSetButtons:     make(map[int64]map[int]bool),
		pendingPlans:          make(map[int64]*PendingPlan),
		runningCommands:       make(map[string]*runEntry),
		cancelGenerations:     make(map[string]int),
	}
}

// BaseContainer returns the adapter used to run commands in the bot container.
func (a *App) BaseContainer() docker.Container { return a.baseContainer }

// Save persists the state, logging failures without interrupting the handler.
func (a *App) Save() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := state.Save(a.Cfg.StateFile, a.State); err != nil {
		a.Logger.Printf("Failed to save state: %v", err)
	}
}

func token() string {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(buffer)
}

// Repo returns a copy of a configured repository.
func (a *App) Repo(name string) (*state.Repo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	repo, ok := a.State.Repos.Get(name)
	if !ok || repo == nil {
		return nil, false
	}
	return cloneRepo(repo), true
}

func cloneRepo(repo *state.Repo) *state.Repo {
	if repo == nil {
		return nil
	}
	copy := *repo
	copy.Mounts = append([]string(nil), repo.Mounts...)
	if repo.Variant != nil {
		variant := *repo.Variant
		copy.Variant = &variant
	}
	copy.Extra = make(map[string]json.RawMessage, len(repo.Extra))
	for key, value := range repo.Extra {
		copy.Extra[key] = append(json.RawMessage(nil), value...)
	}
	return &copy
}

// RepoExists reports whether a repository is configured.
func (a *App) RepoExists(name string) bool {
	_, ok := a.Repo(name)
	return ok
}

// RepoKeys returns repository names in insertion order.
func (a *App) RepoKeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.State.Repos.Keys()
}

// SetRepo stores a repository.
func (a *App) SetRepo(name string, repo *state.Repo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.State.Repos.Set(name, repo)
}

// DeleteRepo removes a repository.
func (a *App) DeleteRepo(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.State.Repos.Delete(name)
}

// ActiveName returns a chat's persisted active repository name.
func (a *App) ActiveName(chatID int64) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.State.ActiveContext[chatID]
}

// ActiveRepo returns the chat's active repository if it still exists.
func (a *App) ActiveRepo(chatID int64) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := a.State.ActiveContext[chatID]
	if _, ok := a.State.Repos.Get(name); ok {
		return name
	}
	return ""
}

// SetActiveContext stores a chat's active repository.
func (a *App) SetActiveContext(chatID int64, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.State.ActiveContext[chatID] = name
}

// ClearActiveContext removes the chat's active repository.
func (a *App) ClearActiveContext(chatID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.State.ActiveContext, chatID)
}

// ActiveContextCopy returns a snapshot of the per-chat active repositories.
func (a *App) ActiveContextCopy() map[int64]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make(map[int64]string, len(a.State.ActiveContext))
	for chatID, name := range a.State.ActiveContext {
		result[chatID] = name
	}
	return result
}

// ModelForRepo returns the repository's valid model or the global default.
func (a *App) ModelForRepo(name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	repo, ok := a.State.Repos.Get(name)
	if ok && repo.Model != "" && a.Cfg.Catalog.Entry(repo.Model) != nil {
		return repo.Model
	}
	return a.Cfg.Catalog.DefaultID
}

// VariantForRepo returns the repository's valid variant, if any.
func (a *App) VariantForRepo(name string) *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	repo, ok := a.State.Repos.Get(name)
	if !ok {
		return a.Cfg.Catalog.DefaultVariant
	}
	modelID := repo.Model
	if modelID == "" || a.Cfg.Catalog.Entry(modelID) == nil {
		modelID = a.Cfg.Catalog.DefaultID
	}
	if repo.VariantSet {
		if repo.Variant == nil {
			return nil
		}
		variant := *repo.Variant
		for _, configured := range a.Cfg.Catalog.Variants(modelID) {
			if configured == variant {
				return &variant
			}
		}
		if a.Cfg.Catalog.DefaultVariant != nil && variant == *a.Cfg.Catalog.DefaultVariant {
			return &variant
		}
	}
	if modelID == a.Cfg.Catalog.DefaultID {
		return a.Cfg.Catalog.DefaultVariant
	}
	return nil
}

// SetRepoSelection stores a validated model/variant selection.
func (a *App) SetRepoSelection(name, modelID string, variant *string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	repo, ok := a.State.Repos.Get(name)
	if !ok {
		repo = &state.Repo{}
		a.State.Repos.Set(name, repo)
	}
	repo.Model = modelID
	repo.Variant = variant
	repo.VariantSet = true
}

// ResolveVariantChoice resolves a 1-based index, variant text, or "off".
func (a *App) ResolveVariantChoice(modelID, value string) (bool, *string) {
	choice := value
	for len(choice) > 0 && (choice[0] == ' ' || choice[0] == '\t' || choice[0] == '\n') {
		choice = choice[1:]
	}
	for len(choice) > 0 && (choice[len(choice)-1] == ' ' || choice[len(choice)-1] == '\t' || choice[len(choice)-1] == '\n') {
		choice = choice[:len(choice)-1]
	}
	if choice == "" {
		return false, nil
	}
	if isDigits(choice) {
		index, _ := parsePositive(choice)
		index--
		variants := a.Cfg.Catalog.Variants(modelID)
		if index >= 0 && index < len(variants) {
			variant := variants[index]
			return true, &variant
		}
		return false, nil
	}
	lower := lowerString(choice)
	if lower == "off" || lower == "none" {
		return true, nil
	}
	for _, variant := range a.Cfg.Catalog.Variants(modelID) {
		if lowerString(variant) == lower {
			value := variant
			return true, &value
		}
	}
	if a.Cfg.Catalog.DefaultVariant != nil && lower == lowerString(*a.Cfg.Catalog.DefaultVariant) {
		return true, a.Cfg.Catalog.DefaultVariant
	}
	return false, nil
}

func isDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}

func parsePositive(value string) (int, bool) {
	result := 0
	for _, character := range value {
		result = result*10 + int(character-'0')
	}
	return result, true
}

func lowerString(value string) string {
	result := []byte(value)
	for i, character := range result {
		if character >= 'A' && character <= 'Z' {
			result[i] = character + ('a' - 'A')
		}
	}
	return string(result)
}

// NewModelToken mints a token for one /model prompt.
func (a *App) NewModelToken() string { return token() }

// NewPlanToken mints a token carried by the plan button.
func (a *App) NewPlanToken() string { return token() }

// Session returns a chat's OpenCode session for one repository.
func (a *App) Session(chatID int64, name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[chatID][name]
}

// SetSession stores an OpenCode session id.
func (a *App) SetSession(chatID int64, name, sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[chatID] == nil {
		a.sessions[chatID] = make(map[string]string)
	}
	a.sessions[chatID][name] = sessionID
}

// ForgetSession drops a chat's session for one repository.
func (a *App) ForgetSession(chatID int64, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions[chatID], name)
	if len(a.sessions[chatID]) == 0 {
		delete(a.sessions, chatID)
	}
}

// ForgetSessionsForRepo drops a repository's sessions for every chat.
func (a *App) ForgetSessionsForRepo(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for chatID, sessions := range a.sessions {
		delete(sessions, name)
		if len(sessions) == 0 {
			delete(a.sessions, chatID)
		}
	}
}

// ChatHasSessions reports whether a chat has any stored sessions.
func (a *App) ChatHasSessions(chatID int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sessions[chatID]) > 0
}

// ForgetChatSessions drops every stored session of a chat.
func (a *App) ForgetChatSessions(chatID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, chatID)
}

// GetModelPrompt returns a copy of an in-progress /model prompt.
func (a *App) GetModelPrompt(chatID int64) *ModelPrompt {
	a.mu.Lock()
	defer a.mu.Unlock()
	prompt := a.modelPrompts[chatID]
	if prompt == nil {
		return nil
	}
	copy := *prompt
	return &copy
}

// SetModelPrompt stores an in-progress /model prompt.
func (a *App) SetModelPrompt(chatID int64, prompt *ModelPrompt) {
	a.mu.Lock()
	defer a.mu.Unlock()
	copy := *prompt
	a.modelPrompts[chatID] = &copy
}

// ReplaceModelPrompt updates a prompt only if its token still matches.
func (a *App) ReplaceModelPrompt(chatID int64, token string, prompt *ModelPrompt) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	stored := a.modelPrompts[chatID]
	if stored == nil || stored.Token != token {
		return false
	}
	copy := *prompt
	a.modelPrompts[chatID] = &copy
	return true
}

// PopModelPrompt removes and returns an in-progress /model prompt.
func (a *App) PopModelPrompt(chatID int64) *ModelPrompt {
	a.mu.Lock()
	defer a.mu.Unlock()
	prompt := a.modelPrompts[chatID]
	delete(a.modelPrompts, chatID)
	return prompt
}

// HasModelPrompt reports whether a /model prompt is open.
func (a *App) HasModelPrompt(chatID int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.modelPrompts[chatID] != nil
}

// GetPendingCommand returns a copy of an in-progress /cmd collection.
func (a *App) GetPendingCommand(chatID int64) *PendingCommand {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := a.pendingCommandEntries[chatID]
	if pending == nil {
		return nil
	}
	copy := *pending
	copy.Commands = append([]string(nil), pending.Commands...)
	return &copy
}

// AppendPendingCommand adds a line to an in-progress collection.
func (a *App) AppendPendingCommand(chatID int64, command string) *PendingCommand {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := a.pendingCommandEntries[chatID]
	if pending == nil {
		return nil
	}
	pending.Commands = append(pending.Commands, command)
	copy := *pending
	copy.Commands = append([]string(nil), pending.Commands...)
	return &copy
}

// SetPendingCommand stores an in-progress /cmd collection.
func (a *App) SetPendingCommand(chatID int64, pending *PendingCommand) {
	a.mu.Lock()
	defer a.mu.Unlock()
	copy := *pending
	copy.Commands = append([]string(nil), pending.Commands...)
	a.pendingCommandEntries[chatID] = &copy
}

// DeletePendingCommand drops an in-progress /cmd collection.
func (a *App) DeletePendingCommand(chatID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pendingCommandEntries, chatID)
}

// HasPendingCommand reports whether a /cmd collection is in progress.
func (a *App) HasPendingCommand(chatID int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pendingCommandEntries[chatID] != nil
}

// TrackCommandSetMessage records a message showing a /cmd keyboard.
func (a *App) TrackCommandSetMessage(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.commandSetButtons[chatID] == nil {
		a.commandSetButtons[chatID] = make(map[int]bool)
	}
	a.commandSetButtons[chatID][messageID] = true
}

// PopCommandSetMessages removes and returns every tracked /cmd keyboard message.
func (a *App) PopCommandSetMessages(chatID int64) []int {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]int, 0, len(a.commandSetButtons[chatID]))
	for id := range a.commandSetButtons[chatID] {
		ids = append(ids, id)
	}
	delete(a.commandSetButtons, chatID)
	return ids
}

// RegisterPendingPlan stores the chat's pending plan.
func (a *App) RegisterPendingPlan(chatID int64, messageID int, planToken string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingPlans[chatID] = &PendingPlan{Token: planToken, MessageID: messageID}
}

// PopPendingPlan consumes the pending plan and reports whether the token matched.
func (a *App) PopPendingPlan(chatID int64, planToken string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	plan := a.pendingPlans[chatID]
	delete(a.pendingPlans, chatID)
	return plan != nil && plan.Token == planToken
}

// TakePendingPlanMessage removes and returns the live plan button message id.
func (a *App) TakePendingPlanMessage(chatID int64) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	plan := a.pendingPlans[chatID]
	delete(a.pendingPlans, chatID)
	if plan == nil {
		return 0, false
	}
	return plan.MessageID, true
}

// RemovePendingPlan stores nothing; callers use TakePendingPlanMessage.
func (a *App) DropPendingPlan(chatID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pendingPlans, chatID)
}

// CommandSet returns a chat's saved command set.
func (a *App) CommandSet(chatID int64, name string) (*state.CommandSet, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sets, ok := a.State.CommandSets[chatID]
	if !ok {
		return nil, false
	}
	return sets.Get(name)
}

// CommandSetNames returns a chat's saved command set names in insertion order.
func (a *App) CommandSetNames(chatID int64) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	sets, ok := a.State.CommandSets[chatID]
	if !ok {
		return nil
	}
	return sets.Keys()
}

// SaveCommandSet stores a command set for a chat.
func (a *App) SaveCommandSet(chatID int64, set *state.CommandSet) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.State.ChatSets(chatID).Set(set.Name, set)
}

// DeleteCommandSet removes a command set from a chat.
func (a *App) DeleteCommandSet(chatID int64, name string) (*state.CommandSet, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sets, ok := a.State.CommandSets[chatID]
	if !ok {
		return nil, false
	}
	set, ok := sets.Get(name)
	if !ok {
		return nil, false
	}
	sets.Delete(name)
	return set, true
}

// CommandSetsCount returns how many sets a chat has.
func (a *App) CommandSetsCount(chatID int64) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	sets, ok := a.State.CommandSets[chatID]
	if !ok {
		return 0
	}
	return sets.Len()
}

// CopyCommandSets returns a snapshot of a chat's sets.
func (a *App) CopyCommandSets(chatID int64) map[string]*state.CommandSet {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make(map[string]*state.CommandSet)
	sets, ok := a.State.CommandSets[chatID]
	if !ok {
		return result
	}
	sets.Range(func(name string, set *state.CommandSet) bool {
		result[name] = &state.CommandSet{Name: set.Name, Commands: append([]string(nil), set.Commands...)}
		return true
	})
	return result
}

// Generation returns the current cancel generation of a repository.
func (a *App) Generation(name string) int {
	if name == "" {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancelGenerations[name]
}

func (a *App) startRun(containerID string, chatID int64, name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	runID := token() + token()
	a.runningCommands[name] = &runEntry{RunID: runID, ChatID: chatID, ContainerID: containerID}
	return runID
}

func (a *App) runCancelled(name, runID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.runningCommands[name]
	return entry != nil && entry.RunID == runID && entry.Cancelled
}

func (a *App) runStillActive(name, runID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.runningCommands[name]
	return entry != nil && entry.RunID == runID
}

func (a *App) finishRun(name, runID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.runningCommands[name]
	if entry == nil || entry.RunID != runID {
		return false
	}
	delete(a.runningCommands, name)
	return entry.Cancelled
}

func (a *App) requestCancel(name string) (*runEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.runningCommands[name]
	a.cancelGenerations[name]++
	if entry == nil {
		return nil, false
	}
	entry.Cancelled = true
	copy := *entry
	return &copy, true
}
