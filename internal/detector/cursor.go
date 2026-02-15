package detector

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Cursor Agent session detection via SQLite state.vscdb databases.
//
// Storage architecture:
//   Workspace DB: ~/Library/Application Support/Cursor/User/workspaceStorage/{hash}/state.vscdb
//     → ItemTable key "composer.composerData" → session index with composerIds + timestamps
//   Global DB:    ~/Library/Application Support/Cursor/User/globalStorage/state.vscdb
//     → cursorDiskKV key "composerData:{uuid}" → session metadata
//     → cursorDiskKV key "bubbleId:{composerId}:{bubbleId}" → individual messages with tool calls
//
// File edits appear in bubble toolFormerData with names: edit_file, search_replace, create_file, write_file.
// File paths are in params.relativeWorkspacePath (already relative to workspace root).
//
// We shell out to sqlite3 CLI rather than embedding a Go SQLite driver, to keep the binary lean.

// --- JSON types for Cursor session data ---

type cursorComposerIndex struct {
	AllComposers []cursorComposerHead `json:"allComposers"`
}

type cursorComposerHead struct {
	ComposerID    string `json:"composerId"`
	Name          string `json:"name"`
	CreatedAt     int64  `json:"createdAt"`     // epoch ms
	LastUpdatedAt int64  `json:"lastUpdatedAt"` // epoch ms
	UnifiedMode   string `json:"unifiedMode"`   // "agent", "edit", "chat"
}

type cursorComposerData struct {
	IsAgentic   bool                       `json:"isAgentic"`
	UsageData   map[string]json.RawMessage `json:"usageData"`
	ModelConfig *cursorModelConfig         `json:"modelConfig"`
}

type cursorModelConfig struct {
	ModelName string `json:"modelName"`
}

type cursorBubble struct {
	Type           int               `json:"type"` // 1=user, 2=assistant
	ToolFormerData *cursorToolFormer `json:"toolFormerData"`
	TokenCount     *cursorTokenCount `json:"tokenCount"`
}

type cursorToolFormer struct {
	Name         string `json:"name"`
	Params       string `json:"params"`       // JSON string
	RawArgs      string `json:"rawArgs"`      // JSON string
	Status       string `json:"status"`       // "completed", "cancelled", "error"
	UserDecision string `json:"userDecision"` // "accepted", "rejected", or ""
}

type cursorToolParams struct {
	RelativeWorkspacePath string `json:"relativeWorkspacePath"`
}

type cursorToolRawArgs struct {
	TargetFile string `json:"target_file"`
	FilePath   string `json:"file_path"`
}

type cursorTokenCount struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// cursorWriteTools are the tool names that indicate file writes/edits.
var cursorWriteTools = map[string]bool{
	"edit_file":      true,
	"search_replace": true,
	"create_file":    true,
	"write_file":     true,
	"write":          true,
}

// sqliteQuery runs a SQL query against a SQLite database using the sqlite3 CLI.
// Returns the parsed JSON output as a slice of maps. Returns nil if sqlite3 is
// not available or if the query fails.
func sqliteQuery(dbPath, query string) ([]map[string]json.RawMessage, error) {
	sqlite3Path, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, fmt.Errorf("sqlite3 not found: %w", err)
	}

	cmd := exec.Command(sqlite3Path, "-json", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3 query failed: %w", err)
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, fmt.Errorf("parsing sqlite3 output: %w", err)
	}
	return rows, nil
}

// sqliteQueryValue runs a query that returns a single "value" column and
// returns the raw string values.
func sqliteQueryValues(dbPath, query string) ([]string, error) {
	rows, err := sqliteQuery(dbPath, query)
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	var values []string
	for _, row := range rows {
		raw, ok := row["value"]
		if !ok {
			continue
		}
		// The value may be a JSON string or raw text; unquote if needed.
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			s = strings.Trim(string(raw), "\"")
		}
		values = append(values, s)
	}
	return values, nil
}

// cursorBaseDirs returns the Cursor workspace storage base directories
// for the current OS.
func cursorBaseDirs() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "workspaceStorage"),
		}
	case "linux":
		return []string{
			filepath.Join(homeDir, ".config", "Cursor", "User", "workspaceStorage"),
		}
	}
	return nil
}

// cursorGlobalDBPath returns the path to the global Cursor state.vscdb.
func cursorGlobalDBPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	case "linux":
		return filepath.Join(homeDir, ".config", "Cursor", "User", "globalStorage", "state.vscdb")
	}
	return ""
}

// findCursorWorkspace finds the Cursor workspace storage directory
// whose workspace.json maps to the given repo root.
func findCursorWorkspace(repoRoot string) string {
	for _, baseDir := range cursorBaseDirs() {
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			wsPath := filepath.Join(baseDir, entry.Name(), "workspace.json")
			data, err := os.ReadFile(wsPath)
			if err != nil {
				continue
			}
			var ws struct {
				Folder string `json:"folder"`
			}
			if err := json.Unmarshal(data, &ws); err != nil {
				continue
			}
			folder := cursorURIToPath(ws.Folder)
			if folder == repoRoot {
				return filepath.Join(baseDir, entry.Name())
			}
		}
	}
	return ""
}

// cursorURIToPath converts a file:// URI to a local path.
func cursorURIToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	u, err := url.Parse(uri)
	if err != nil {
		return strings.TrimPrefix(uri, "file://")
	}
	return u.Path
}

// findCursorComposers reads the workspace state.vscdb and returns recent
// composer sessions within maxAge.
func findCursorComposers(workspaceDBPath string, maxAge time.Duration) ([]cursorComposerHead, error) {
	if _, err := os.Stat(workspaceDBPath); err != nil {
		return nil, nil
	}

	values, err := sqliteQueryValues(workspaceDBPath,
		`SELECT value FROM ItemTable WHERE key = 'composer.composerData'`)
	if err != nil || len(values) == 0 {
		return nil, err
	}

	// Try modern format: {"allComposers": [...]}
	var index cursorComposerIndex
	if err := json.Unmarshal([]byte(values[0]), &index); err != nil {
		// Try legacy format: direct array [...]
		var composers []cursorComposerHead
		if err := json.Unmarshal([]byte(values[0]), &composers); err != nil {
			return nil, nil
		}
		index.AllComposers = composers
	}

	cutoff := time.Now().Add(-maxAge).UnixMilli()
	var recent []cursorComposerHead
	for _, c := range index.AllComposers {
		if c.LastUpdatedAt > cutoff && c.ComposerID != "" {
			recent = append(recent, c)
		}
	}
	return recent, nil
}

// parseCursorBubbles queries the global state.vscdb for file-writing tool calls
// across the given composer sessions.
func parseCursorBubbles(globalDBPath string, composerIds []string) (*SessionInfo, error) {
	if _, err := os.Stat(globalDBPath); err != nil {
		return nil, nil
	}

	// Check if cursorDiskKV table exists
	rows, err := sqliteQuery(globalDBPath,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='cursorDiskKV'`)
	if err != nil || len(rows) == 0 {
		return nil, nil
	}

	info := &SessionInfo{
		Tool:         ToolCursor,
		FilesWritten: make(map[string]struct{}),
	}

	for _, composerId := range composerIds {
		// Use range-based prefix search for index efficiency (LIKE causes full table scan)
		// ';' is the ASCII character after ':', so key < 'bubbleId:xxx;' covers all 'bubbleId:xxx:*' keys
		query := fmt.Sprintf(
			`SELECT value FROM cursorDiskKV WHERE key >= 'bubbleId:%s:' AND key < 'bubbleId:%s;' `+
				`AND (value LIKE '%%"edit_file"%%' OR value LIKE '%%"search_replace"%%' `+
				`OR value LIKE '%%"create_file"%%' OR value LIKE '%%"write_file"%%')`,
			composerId, composerId)

		values, err := sqliteQueryValues(globalDBPath, query)
		if err != nil {
			continue
		}

		for _, val := range values {
			var bubble cursorBubble
			if err := json.Unmarshal([]byte(val), &bubble); err != nil {
				continue
			}

			// Sum token counts
			if bubble.TokenCount != nil {
				info.TotalTokens += bubble.TokenCount.InputTokens + bubble.TokenCount.OutputTokens
			}

			if bubble.ToolFormerData == nil {
				continue
			}
			tf := bubble.ToolFormerData

			// Only count file-writing tools
			if !cursorWriteTools[tf.Name] {
				continue
			}

			// Filter: must be completed and not rejected
			if tf.Status != "completed" {
				continue
			}
			if tf.UserDecision == "rejected" {
				continue
			}

			// Extract file path
			filePath := extractCursorFilePath(tf)
			if filePath != "" {
				info.FilesWritten[filePath] = struct{}{}
			}
		}
	}

	if len(info.FilesWritten) == 0 {
		return nil, nil
	}
	return info, nil
}

// extractCursorFilePath extracts the relative file path from a tool call's
// params or rawArgs.
func extractCursorFilePath(tf *cursorToolFormer) string {
	// Priority 1: params.relativeWorkspacePath
	if tf.Params != "" {
		var params cursorToolParams
		if err := json.Unmarshal([]byte(tf.Params), &params); err == nil {
			if params.RelativeWorkspacePath != "" {
				return params.RelativeWorkspacePath
			}
		}
	}

	// Priority 2: rawArgs.target_file or rawArgs.file_path
	if tf.RawArgs != "" {
		var args cursorToolRawArgs
		if err := json.Unmarshal([]byte(tf.RawArgs), &args); err == nil {
			if args.TargetFile != "" {
				return args.TargetFile
			}
			if args.FilePath != "" {
				return args.FilePath
			}
		}
	}

	return ""
}

// parseCursorComposerModel extracts the model name from a composer's metadata.
func parseCursorComposerModel(globalDBPath string, composerId string) string {
	query := fmt.Sprintf(
		`SELECT value FROM cursorDiskKV WHERE key = 'composerData:%s'`, composerId)
	values, err := sqliteQueryValues(globalDBPath, query)
	if err != nil || len(values) == 0 {
		return ""
	}

	var data cursorComposerData
	if err := json.Unmarshal([]byte(values[0]), &data); err != nil {
		return ""
	}

	// Priority 1: usageData keys (e.g. "claude-4-sonnet-thinking")
	if len(data.UsageData) > 0 {
		for model := range data.UsageData {
			if model != "" {
				return model
			}
		}
	}

	// Priority 2: modelConfig.modelName
	if data.ModelConfig != nil && data.ModelConfig.ModelName != "" && data.ModelConfig.ModelName != "default" {
		return data.ModelConfig.ModelName
	}

	return ""
}

// detectCursor finds recent Cursor Agent/Composer sessions for the repo
// and extracts file-level edit information.
func detectCursor(repoRoot string, maxAge time.Duration) (*SessionInfo, error) {
	// Check sqlite3 availability
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	workspaceDir := findCursorWorkspace(repoRoot)
	if workspaceDir == "" {
		return nil, nil
	}

	workspaceDBPath := filepath.Join(workspaceDir, "state.vscdb")
	composers, err := findCursorComposers(workspaceDBPath, maxAge)
	if err != nil || len(composers) == 0 {
		return nil, nil
	}

	globalDBPath := cursorGlobalDBPath()
	if globalDBPath == "" {
		return nil, nil
	}

	var composerIds []string
	var latestComposerId string
	var latestTimestamp int64
	for _, c := range composers {
		composerIds = append(composerIds, c.ComposerID)
		if c.LastUpdatedAt > latestTimestamp {
			latestTimestamp = c.LastUpdatedAt
			latestComposerId = c.ComposerID
		}
	}

	info, err := parseCursorBubbles(globalDBPath, composerIds)
	if err != nil || info == nil {
		return nil, nil
	}

	// Extract model from the most recent composer
	if latestComposerId != "" {
		info.Model = parseCursorComposerModel(globalDBPath, latestComposerId)
	}

	// Session duration: earliest createdAt to latest lastUpdatedAt
	var earliest, latest int64
	for _, c := range composers {
		if earliest == 0 || c.CreatedAt < earliest {
			earliest = c.CreatedAt
		}
		if c.LastUpdatedAt > latest {
			latest = c.LastUpdatedAt
		}
	}
	if earliest > 0 && latest > earliest {
		info.SessionDurationSec = (latest - earliest) / 1000
	}

	return info, nil
}
