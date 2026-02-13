package detector

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Minimal types for JSONL parsing — only unmarshal what we need.
type jsonlLine struct {
	Type      string   `json:"type"`
	Timestamp string   `json:"timestamp"`
	Message   jsonlMsg `json:"message"`
}

type jsonlMsg struct {
	Model   string         `json:"model"`
	Content []jsonlContent `json:"content"`
	Usage   jsonlUsage     `json:"usage"`
}

type jsonlContent struct {
	Type  string     `json:"type"`
	Name  string     `json:"name"`
	Input jsonlInput `json:"input"`
}

type jsonlInput struct {
	FilePath string `json:"file_path"`
}

type jsonlUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// claudeSessionDir returns the Claude Code projects directory for a given repo root.
// e.g. /Users/jose/projects/tempo → ~/.claude/projects/-Users-jose-projects-tempo
// Returns empty string if the home directory cannot be determined.
func claudeSessionDir(repoRoot string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	encoded := strings.ReplaceAll(repoRoot, string(filepath.Separator), "-")
	return filepath.Join(homeDir, ".claude", "projects", encoded)
}

// findLatestSession finds the most recently modified .jsonl file in the session dir,
// excluding agent-*.jsonl files. Only considers files modified within maxAge.
func findLatestSession(sessionDir string, maxAge time.Duration) (string, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return "", err
	}

	var bestPath string
	var bestModTime time.Time
	cutoff := time.Now().Add(-maxAge)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if strings.HasPrefix(name, "agent-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			continue
		}
		if info.ModTime().After(bestModTime) {
			bestModTime = info.ModTime()
			bestPath = filepath.Join(sessionDir, name)
		}
	}

	if bestPath == "" {
		return "", fmt.Errorf("no recent Claude Code sessions found in %s", sessionDir)
	}
	return bestPath, nil
}

// parseClaudeSession streams a JSONL file and extracts session info.
// Only Edit and Write tool_use calls are extracted for file paths.
func parseClaudeSession(jsonlPath string, repoRoot string) (*SessionInfo, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	info := &SessionInfo{
		Tool:         ToolClaudeCode,
		FilesWritten: make(map[string]struct{}),
	}

	assistantKey := []byte(`"assistant"`)
	var firstTimestamp, lastTimestamp time.Time

	for scanner.Scan() {
		line := scanner.Bytes()

		// Pre-filter: skip lines that can't be assistant messages
		if !bytes.Contains(line, assistantKey) {
			continue
		}

		var msg jsonlLine
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		if msg.Type != "assistant" {
			continue
		}

		// Parse timestamp
		if msg.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
				if firstTimestamp.IsZero() || t.Before(firstTimestamp) {
					firstTimestamp = t
				}
				if t.After(lastTimestamp) {
					lastTimestamp = t
				}
			}
		}

		// Extract model (use last non-empty)
		if msg.Message.Model != "" {
			info.Model = msg.Message.Model
		}

		// Sum token usage across the entire session. Note: this is session-level
		// totals, not commit-level. A long-running session may accumulate very
		// large token counts (100M+) that span many commits.
		u := msg.Message.Usage
		info.TotalTokens += u.InputTokens + u.OutputTokens +
			u.CacheCreationInputTokens + u.CacheReadInputTokens

		// Extract file paths from Edit/Write tool_use calls
		for _, c := range msg.Message.Content {
			if c.Type != "tool_use" {
				continue
			}
			if c.Name != "Edit" && c.Name != "Write" {
				continue
			}
			fp := c.Input.FilePath
			if fp == "" {
				continue
			}
			relPath := strings.TrimPrefix(fp, repoRoot+"/")
			if relPath != fp {
				info.FilesWritten[relPath] = struct{}{}
			}
		}
	}

	if len(info.FilesWritten) == 0 {
		return nil, nil
	}

	if !firstTimestamp.IsZero() && !lastTimestamp.IsZero() {
		info.SessionDurationSec = int64(lastTimestamp.Sub(firstTimestamp).Seconds())
	}

	return info, scanner.Err()
}
