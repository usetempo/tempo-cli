package detector

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testCursorWorkspaceStorage returns the platform-correct Cursor workspace
// storage base dir under the given home directory (mirrors cursorBaseDirs).
func testCursorWorkspaceStorage(homeDir string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "workspaceStorage")
	case "linux":
		return filepath.Join(homeDir, ".config", "Cursor", "User", "workspaceStorage")
	default:
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "workspaceStorage")
	}
}

// testCursorGlobalStorage returns the platform-correct Cursor global storage
// dir under the given home directory (mirrors cursorGlobalDBPath).
func testCursorGlobalStorage(homeDir string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "globalStorage")
	case "linux":
		return filepath.Join(homeDir, ".config", "Cursor", "User", "globalStorage")
	default:
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "globalStorage")
	}
}

// skipIfNoSQLite skips the test if sqlite3 CLI is not available.
func skipIfNoSQLite(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not found, skipping Cursor detector test")
	}
}

// createTestDB creates a SQLite database at the given path and executes
// the provided SQL statements.
func createTestDB(t *testing.T, dbPath string, statements []string) {
	t.Helper()
	for _, stmt := range statements {
		cmd := exec.Command("sqlite3", dbPath, stmt)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sqlite3 exec failed: %v\nstatement: %s\noutput: %s", err, stmt, out)
		}
	}
}

func TestFindCursorWorkspace(t *testing.T) {
	skipIfNoSQLite(t)
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	repoRoot := "/Users/jose/projects/myapp"

	// Create a workspace storage dir with matching workspace.json
	wsDir := filepath.Join(testCursorWorkspaceStorage(homeDir), "abc123")
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		t.Fatal(err)
	}
	wsJSON, err := json.Marshal(map[string]string{"folder": "file://" + repoRoot})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "workspace.json"), wsJSON, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a non-matching workspace
	otherDir := filepath.Join(testCursorWorkspaceStorage(homeDir), "def456")
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatal(err)
	}
	otherJSON, err := json.Marshal(map[string]string{"folder": "file:///Users/jose/projects/other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "workspace.json"), otherJSON, 0644); err != nil {
		t.Fatal(err)
	}

	got := findCursorWorkspace(repoRoot)
	if got != wsDir {
		t.Errorf("got %q, want %q", got, wsDir)
	}
}

func TestFindCursorWorkspace_NotFound(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	got := findCursorWorkspace("/some/repo")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestFindCursorComposers(t *testing.T) {
	skipIfNoSQLite(t)

	dbPath := filepath.Join(t.TempDir(), "state.vscdb")

	now := time.Now().UnixMilli()
	oldTime := time.Now().Add(-5 * 24 * time.Hour).UnixMilli()

	composerData := cursorComposerIndex{
		AllComposers: []cursorComposerHead{
			{ComposerID: "recent-1", LastUpdatedAt: now, UnifiedMode: "agent"},
			{ComposerID: "old-1", LastUpdatedAt: oldTime, UnifiedMode: "agent"},
			{ComposerID: "recent-2", LastUpdatedAt: now - 1000, UnifiedMode: "edit"},
		},
	}
	data, err := json.Marshal(composerData)
	if err != nil {
		t.Fatal(err)
	}

	createTestDB(t, dbPath, []string{
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
		fmt.Sprintf(`INSERT INTO ItemTable (key, value) VALUES ('composer.composerData', '%s');`,
			escapeSQLString(string(data))),
	})

	composers, err := findCursorComposers(dbPath, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(composers) != 2 {
		t.Fatalf("expected 2 recent composers, got %d", len(composers))
	}

	ids := make(map[string]bool)
	for _, c := range composers {
		ids[c.ComposerID] = true
	}
	if !ids["recent-1"] || !ids["recent-2"] {
		t.Errorf("expected recent-1 and recent-2, got %v", ids)
	}
}

func TestFindCursorComposers_Empty(t *testing.T) {
	skipIfNoSQLite(t)

	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	createTestDB(t, dbPath, []string{
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
	})

	composers, err := findCursorComposers(dbPath, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(composers) != 0 {
		t.Errorf("expected 0 composers, got %d", len(composers))
	}
}

func TestParseCursorBubbles_Basic(t *testing.T) {
	skipIfNoSQLite(t)

	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	composerId := "test-composer-1"

	editBubble := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "edit_file",
			Status:       "completed",
			UserDecision: "accepted",
			Params:       `{"relativeWorkspacePath":"src/main.go","contents":"package main"}`,
		},
		TokenCount: &cursorTokenCount{InputTokens: 100, OutputTokens: 50},
	}

	searchReplaceBubble := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "search_replace",
			Status:       "completed",
			UserDecision: "accepted",
			Params:       `{"relativeWorkspacePath":"src/utils.go","oldString":"old","newString":"new"}`,
		},
		TokenCount: &cursorTokenCount{InputTokens: 80, OutputTokens: 40},
	}

	// Duplicate edit on same file (should be deduped)
	dupBubble := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "edit_file",
			Status:       "completed",
			UserDecision: "accepted",
			Params:       `{"relativeWorkspacePath":"src/main.go","contents":"updated"}`,
		},
		TokenCount: &cursorTokenCount{InputTokens: 60, OutputTokens: 30},
	}

	editData, err := json.Marshal(editBubble)
	if err != nil {
		t.Fatal(err)
	}
	srData, err := json.Marshal(searchReplaceBubble)
	if err != nil {
		t.Fatal(err)
	}
	dupData, err := json.Marshal(dupBubble)
	if err != nil {
		t.Fatal(err)
	}

	createTestDB(t, dbPath, []string{
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-1', '%s');`,
			composerId, escapeSQLString(string(editData))),
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-2', '%s');`,
			composerId, escapeSQLString(string(srData))),
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-3', '%s');`,
			composerId, escapeSQLString(string(dupData))),
	})

	info, err := parseCursorBubbles(dbPath, []string{composerId})
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	wantFiles := []string{"src/main.go", "src/utils.go"}
	gotFiles := sortedKeys(info.FilesWritten)
	if !equal(gotFiles, wantFiles) {
		t.Errorf("files: got %v, want %v", gotFiles, wantFiles)
	}

	// Token sum: (100+50) + (80+40) + (60+30) = 360
	if info.TotalTokens != 360 {
		t.Errorf("tokens: got %d, want 360", info.TotalTokens)
	}
}

func TestParseCursorBubbles_NoEdits(t *testing.T) {
	skipIfNoSQLite(t)

	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	composerId := "test-composer-1"

	readBubble := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:   "read_file",
			Status: "completed",
			Params: `{"relativeWorkspacePath":"src/main.go"}`,
		},
	}
	data, err := json.Marshal(readBubble)
	if err != nil {
		t.Fatal(err)
	}

	createTestDB(t, dbPath, []string{
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-1', '%s');`,
			composerId, escapeSQLString(string(data))),
	})

	info, err := parseCursorBubbles(dbPath, []string{composerId})
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil for no edits, got %+v", info)
	}
}

func TestParseCursorBubbles_MultipleComposers(t *testing.T) {
	skipIfNoSQLite(t)

	dbPath := filepath.Join(t.TempDir(), "state.vscdb")

	bubble1 := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "edit_file",
			Status:       "completed",
			UserDecision: "accepted",
			Params:       `{"relativeWorkspacePath":"a.go"}`,
		},
	}
	bubble2 := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "edit_file",
			Status:       "completed",
			UserDecision: "accepted",
			Params:       `{"relativeWorkspacePath":"b.go"}`,
		},
	}

	data1, err := json.Marshal(bubble1)
	if err != nil {
		t.Fatal(err)
	}
	data2, err := json.Marshal(bubble2)
	if err != nil {
		t.Fatal(err)
	}

	createTestDB(t, dbPath, []string{
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:comp-1:bubble-1', '%s');`,
			escapeSQLString(string(data1))),
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:comp-2:bubble-1', '%s');`,
			escapeSQLString(string(data2))),
	})

	info, err := parseCursorBubbles(dbPath, []string{"comp-1", "comp-2"})
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	wantFiles := []string{"a.go", "b.go"}
	gotFiles := sortedKeys(info.FilesWritten)
	if !equal(gotFiles, wantFiles) {
		t.Errorf("files: got %v, want %v", gotFiles, wantFiles)
	}
}

func TestParseCursorBubbles_FallbackToRawArgs(t *testing.T) {
	skipIfNoSQLite(t)

	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	composerId := "test-composer-1"

	// No params.relativeWorkspacePath, should fall back to rawArgs.target_file
	bubble := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "edit_file",
			Status:       "completed",
			UserDecision: "accepted",
			RawArgs:      `{"target_file":"src/fallback.go","instructions":"update"}`,
		},
	}
	data, err := json.Marshal(bubble)
	if err != nil {
		t.Fatal(err)
	}

	createTestDB(t, dbPath, []string{
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-1', '%s');`,
			composerId, escapeSQLString(string(data))),
	})

	info, err := parseCursorBubbles(dbPath, []string{composerId})
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	wantFiles := []string{"src/fallback.go"}
	gotFiles := sortedKeys(info.FilesWritten)
	if !equal(gotFiles, wantFiles) {
		t.Errorf("files: got %v, want %v", gotFiles, wantFiles)
	}
}

func TestParseCursorBubbles_RejectedEdits(t *testing.T) {
	skipIfNoSQLite(t)

	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	composerId := "test-composer-1"

	// Accepted edit
	accepted := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "edit_file",
			Status:       "completed",
			UserDecision: "accepted",
			Params:       `{"relativeWorkspacePath":"accepted.go"}`,
		},
	}
	// Rejected edit — should NOT count
	rejected := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "edit_file",
			Status:       "completed",
			UserDecision: "rejected",
			Params:       `{"relativeWorkspacePath":"rejected.go"}`,
		},
	}
	// No userDecision (older schema) — should count
	noDecision := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:   "edit_file",
			Status: "completed",
			Params: `{"relativeWorkspacePath":"nodecision.go"}`,
		},
	}

	acceptedData, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	rejectedData, err := json.Marshal(rejected)
	if err != nil {
		t.Fatal(err)
	}
	noDecisionData, err := json.Marshal(noDecision)
	if err != nil {
		t.Fatal(err)
	}

	createTestDB(t, dbPath, []string{
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-1', '%s');`,
			composerId, escapeSQLString(string(acceptedData))),
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-2', '%s');`,
			composerId, escapeSQLString(string(rejectedData))),
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-3', '%s');`,
			composerId, escapeSQLString(string(noDecisionData))),
	})

	info, err := parseCursorBubbles(dbPath, []string{composerId})
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	wantFiles := []string{"accepted.go", "nodecision.go"}
	gotFiles := sortedKeys(info.FilesWritten)
	if !equal(gotFiles, wantFiles) {
		t.Errorf("files: got %v, want %v", gotFiles, wantFiles)
	}
}

func TestParseCursorComposerModel(t *testing.T) {
	skipIfNoSQLite(t)

	t.Run("from usageData", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.vscdb")
		composerId := "model-test-1"

		data := `{"isAgentic":true,"usageData":{"claude-4-sonnet-thinking":{"costInCents":0.5}},"modelConfig":{"modelName":"default"}}`

		createTestDB(t, dbPath, []string{
			`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
			fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('composerData:%s', '%s');`,
				composerId, escapeSQLString(data)),
		})

		got := parseCursorComposerModel(dbPath, composerId)
		if got != "claude-4-sonnet-thinking" {
			t.Errorf("model: got %q, want %q", got, "claude-4-sonnet-thinking")
		}
	})

	t.Run("from modelConfig", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.vscdb")
		composerId := "model-test-2"

		data := `{"isAgentic":true,"usageData":{},"modelConfig":{"modelName":"gpt-4o"}}`

		createTestDB(t, dbPath, []string{
			`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
			fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('composerData:%s', '%s');`,
				composerId, escapeSQLString(data)),
		})

		got := parseCursorComposerModel(dbPath, composerId)
		if got != "gpt-4o" {
			t.Errorf("model: got %q, want %q", got, "gpt-4o")
		}
	})

	t.Run("default modelName ignored", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.vscdb")
		composerId := "model-test-3"

		data := `{"isAgentic":true,"usageData":{},"modelConfig":{"modelName":"default"}}`

		createTestDB(t, dbPath, []string{
			`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
			fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('composerData:%s', '%s');`,
				composerId, escapeSQLString(data)),
		})

		got := parseCursorComposerModel(dbPath, composerId)
		if got != "" {
			t.Errorf("model: got %q, want empty string", got)
		}
	})
}

func TestDetectCursor_Integration(t *testing.T) {
	skipIfNoSQLite(t)
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	repoRoot := "/Users/jose/projects/myapp"

	// Set up workspace storage
	wsDir := filepath.Join(testCursorWorkspaceStorage(homeDir), "abc123")
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write workspace.json
	wsJSON, err := json.Marshal(map[string]string{"folder": "file://" + repoRoot})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "workspace.json"), wsJSON, 0644); err != nil {
		t.Fatal(err)
	}

	// Create workspace state.vscdb with composer index
	now := time.Now().UnixMilli()
	composerId := "integration-composer-1"
	composerIndex := cursorComposerIndex{
		AllComposers: []cursorComposerHead{
			{ComposerID: composerId, LastUpdatedAt: now, CreatedAt: now - 60000, UnifiedMode: "agent"},
		},
	}
	indexData, err := json.Marshal(composerIndex)
	if err != nil {
		t.Fatal(err)
	}

	createTestDB(t, filepath.Join(wsDir, "state.vscdb"), []string{
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
		fmt.Sprintf(`INSERT INTO ItemTable (key, value) VALUES ('composer.composerData', '%s');`,
			escapeSQLString(string(indexData))),
	})

	// Create global state.vscdb with bubbles and composer data
	globalDir := testCursorGlobalStorage(homeDir)
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}

	bubble := cursorBubble{
		Type: 2,
		ToolFormerData: &cursorToolFormer{
			Name:         "edit_file",
			Status:       "completed",
			UserDecision: "accepted",
			Params:       `{"relativeWorkspacePath":"src/main.go"}`,
		},
		TokenCount: &cursorTokenCount{InputTokens: 500, OutputTokens: 200},
	}
	bubbleData, err := json.Marshal(bubble)
	if err != nil {
		t.Fatal(err)
	}

	composerMeta := `{"isAgentic":true,"usageData":{"claude-4-sonnet":{"costInCents":1}},"modelConfig":{"modelName":"default"}}`

	createTestDB(t, filepath.Join(globalDir, "state.vscdb"), []string{
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);`,
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('bubbleId:%s:bubble-1', '%s');`,
			composerId, escapeSQLString(string(bubbleData))),
		fmt.Sprintf(`INSERT INTO cursorDiskKV (key, value) VALUES ('composerData:%s', '%s');`,
			composerId, escapeSQLString(composerMeta)),
	})

	info, err := detectCursor(repoRoot, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	wantFiles := []string{"src/main.go"}
	gotFiles := sortedKeys(info.FilesWritten)
	if !equal(gotFiles, wantFiles) {
		t.Errorf("files: got %v, want %v", gotFiles, wantFiles)
	}
	if info.Model != "claude-4-sonnet" {
		t.Errorf("model: got %q, want %q", info.Model, "claude-4-sonnet")
	}
	if info.TotalTokens != 700 {
		t.Errorf("tokens: got %d, want 700", info.TotalTokens)
	}
	if info.Tool != ToolCursor {
		t.Errorf("tool: got %q, want %q", info.Tool, ToolCursor)
	}
}

func TestDetectCursor_NoWorkspace(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	info, err := detectCursor("/some/repo", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil, got %+v", info)
	}
}

func TestDetectCursor_NoSQLite(t *testing.T) {
	// Test graceful degradation when sqlite3 is not available
	t.Setenv("PATH", "/nonexistent")

	info, err := detectCursor("/some/repo", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil when sqlite3 not available, got %+v", info)
	}
}

// escapeSQLString escapes single quotes in a string for use in SQL INSERT statements.
func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
