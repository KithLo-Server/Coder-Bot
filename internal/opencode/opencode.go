// Package opencode parses opencode JSONL output and builds run commands.
package opencode

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Events is the parsed form of opencode's streamed JSONL output.
type Events struct {
	Texts     []string
	Errors    []string
	SessionID string
}

// ParseEvents extracts answer text, errors and the first session id.
// Malformed or non-JSON lines are skipped.
func ParseEvents(raw string) Events {
	var events Events
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if events.SessionID == "" {
			if sessionID, ok := event["sessionID"].(string); ok && sessionID != "" {
				events.SessionID = sessionID
			}
		}
		switch event["type"] {
		case "text":
			part, ok := event["part"].(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				events.Texts = append(events.Texts, text)
			}
		case "error":
			errorBody, _ := event["error"].(map[string]any)
			data, _ := errorBody["data"].(map[string]any)
			if message, ok := data["message"].(string); ok && message != "" {
				events.Errors = append(events.Errors, "⚠️ Error: "+message)
			}
		}
	}
	return events
}

// Response returns the final answer text, or the errors, or the raw output.
func Response(raw string) string {
	events := ParseEvents(raw)
	if len(events.Texts) > 0 {
		return strings.TrimSpace(strings.Join(events.Texts, "\n"))
	}
	if len(events.Errors) > 0 {
		return strings.TrimSpace(strings.Join(events.Errors, "\n"))
	}
	return strings.TrimSpace(raw)
}

// SessionID returns the session id carried by the output.
func SessionID(raw string) string {
	return ParseEvents(raw).SessionID
}

// FailedWithoutText reports an error event with no answer text.
func FailedWithoutText(raw string) bool {
	events := ParseEvents(raw)
	return len(events.Errors) > 0 && len(events.Texts) == 0
}

// BuildCommand builds the `opencode run` shell command.
func BuildCommand(prompt, mode, sessionID, variant, model, defaultModel string) string {
	pieces := []string{shellJoin("opencode", "run", "--format", "json")}
	if sessionID != "" {
		pieces = append(pieces, shellJoin("--session", sessionID))
	}
	pieces = append(pieces, shellJoin("--agent", mode))
	modelName := model
	if modelName == "" {
		modelName = defaultModel
	}
	modelName = strings.ReplaceAll(modelName, `\`, `\\`)
	modelName = strings.NewReplacer(`"`, `\"`, `$`, `\$`, "`", "\\`").Replace(modelName)
	pieces = append(pieces, fmt.Sprintf(`--model "%s"`, modelName))
	if variant != "" && variant != "off" {
		pieces = append(pieces, shellJoin("--variant", variant))
	}
	pieces = append(pieces, shellQuote(prompt))
	return strings.Join(pieces, " ")
}

func shellJoin(args ...string) string {
	quoted := make([]string, len(args))
	for index, arg := range args {
		quoted[index] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// shellQuote mimics Python's shlex.quote / shlex.join output.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	safe := true
	for _, character := range value {
		if !isShellSafe(character) {
			safe = false
			break
		}
	}
	if safe {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func isShellSafe(character rune) bool {
	switch {
	case character >= 'a' && character <= 'z':
		return true
	case character >= 'A' && character <= 'Z':
		return true
	case character >= '0' && character <= '9':
		return true
	}
	return strings.ContainsRune("_@%+=:,./-", character)
}

// CallbackPayload returns data without prefix, or false when it does not match.
func CallbackPayload(data, prefix string) (string, bool) {
	if data == "" || !strings.HasPrefix(data, prefix) {
		return "", false
	}
	return data[len(prefix):], true
}

// TelegramUploadName returns the timestamped filename used for unnamed uploads.
func TelegramUploadName(extension string) string {
	return fmt.Sprintf("telegram_upload_%d%s", time.Now().Unix(), extension)
}

var backtickRun = regexp.MustCompile("`+")

// FormatUntrustedOutput wraps text in a code fence longer than any backtick run.
func FormatUntrustedOutput(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "(no output)"
	}
	if maxChars > 0 && len(text) > maxChars {
		text = "...(truncated)...\n" + text[len(text)-maxChars:]
	}
	longest := 0
	for _, run := range backtickRun.FindAllString(text, -1) {
		if len(run) > longest {
			longest = len(run)
		}
	}
	fenceLength := 3
	if longest+1 > fenceLength {
		fenceLength = longest + 1
	}
	fence := strings.Repeat("`", fenceLength)
	return fence + "\n" + text + "\n" + fence
}
