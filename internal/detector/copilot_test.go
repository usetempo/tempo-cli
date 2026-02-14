package detector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUriToPath(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"file:///Users/jose/projects/tempo", "/Users/jose/projects/tempo"},
		{"file:///home/jose/projects/tempo", "/home/jose/projects/tempo"},
		{"/Users/jose/projects/tempo", "/Users/jose/projects/tempo"},
		{"file:///Users/jose/projects/path%20with%20spaces", "/Users/jose/projects/path with spaces"},
	}
	for _, tt := range tests {
		got := uriToPath(tt.uri)
		if got != tt.want {
			t.Errorf("uriToPath(%q) = %q, want %q", tt.uri, got, tt.want)
		}
	}
}

func TestFindCopilotWorkspace(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	repoRoot := "/Users/jose/projects/myapp"

	// Create a workspace storage dir with matching workspace.json
	wsDir := filepath.Join(homeDir, "Library", "Application Support", "Code", "User", "workspaceStorage", "abc123")
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		t.Fatal(err)
	}
	wsJSON := copilotWorkspace{Folder: "file://" + repoRoot}
	data, err := json.Marshal(wsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "workspace.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a non-matching workspace
	otherDir := filepath.Join(homeDir, "Library", "Application Support", "Code", "User", "workspaceStorage", "def456")
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatal(err)
	}
	otherJSON := copilotWorkspace{Folder: "file:///Users/jose/projects/other"}
	otherData, err := json.Marshal(otherJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "workspace.json"), otherData, 0644); err != nil {
		t.Fatal(err)
	}

	got := findCopilotWorkspace(repoRoot)
	if got != wsDir {
		t.Errorf("got %q, want %q", got, wsDir)
	}
}

func TestFindCopilotWorkspace_NotFound(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	got := findCopilotWorkspace("/some/repo")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestFindCopilotSessions(t *testing.T) {
	dir := t.TempDir()
	chatDir := filepath.Join(dir, "chatSessions")
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a recent session
	recentPath := filepath.Join(chatDir, "session-1.json")
	if err := os.WriteFile(recentPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create an old session
	oldPath := filepath.Join(chatDir, "session-2.json")
	if err := os.WriteFile(oldPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-5 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}

	// Create a non-json file (should be skipped)
	if err := os.WriteFile(filepath.Join(chatDir, "notes.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	sessions, err := findCopilotSessions(dir, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d: %v", len(sessions), sessions)
	}
	if sessions[0] != recentPath {
		t.Errorf("got %q, want %q", sessions[0], recentPath)
	}
}

func TestFindCopilotSessions_NoChatDir(t *testing.T) {
	dir := t.TempDir()
	sessions, err := findCopilotSessions(dir, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != nil {
		t.Errorf("expected nil, got %v", sessions)
	}
}

const testCopilotSession = `{
  "version": 3,
  "requests": [
    {
      "requestId": "req-1",
      "timestamp": 1707800000000,
      "modelId": "copilot/auto",
      "agent": {"id": "github.copilot.editsAgent"},
      "response": [
        {"kind": "thinking"},
        {"kind": "toolInvocationSerialized", "toolId": "copilot_readFile"},
        {
          "kind": "textEditGroup",
          "uri": {"path": "/Users/jose/myapp/src/main.go"},
          "edits": [[{"text": "package main"}]]
        }
      ]
    },
    {
      "requestId": "req-2",
      "timestamp": 1707800060000,
      "modelId": "copilot/auto",
      "agent": {"id": "github.copilot.editsAgent"},
      "response": [
        {
          "kind": "textEditGroup",
          "uri": {"path": "/Users/jose/myapp/src/utils.go"},
          "edits": [[{"text": "package main"}]]
        },
        {
          "kind": "textEditGroup",
          "uri": {"path": "/Users/jose/myapp/src/main.go"},
          "edits": [[{"text": "updated content"}]]
        }
      ]
    }
  ],
  "selectedModel": {
    "identifier": "copilot/auto",
    "metadata": {
      "family": "gpt-5-mini",
      "version": "gpt-5-mini"
    }
  }
}`

func TestParseCopilotSession_Basic(t *testing.T) {
	path := writeCopilotTestJSON(t, testCopilotSession)
	info, err := parseCopilotSession(path, "/Users/jose/myapp")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected non-nil session info")
	}

	// Verify files: src/main.go (deduped) and src/utils.go
	wantFiles := []string{"src/main.go", "src/utils.go"}
	gotFiles := sortedKeys(info.FilesWritten)
	if !equal(gotFiles, wantFiles) {
		t.Errorf("files: got %v, want %v", gotFiles, wantFiles)
	}

	if info.Model != "gpt-5-mini" {
		t.Errorf("model: got %q, want %q", info.Model, "gpt-5-mini")
	}

	// Duration: (1707800060000 - 1707800000000) / 1000 = 60 seconds
	if info.SessionDurationSec != 60 {
		t.Errorf("duration: got %d, want 60", info.SessionDurationSec)
	}

	if info.Tool != ToolCopilot {
		t.Errorf("tool: got %q, want %q", info.Tool, ToolCopilot)
	}
}

func TestParseCopilotSession_NoEdits(t *testing.T) {
	content := `{
		"version": 3,
		"requests": [
			{
				"timestamp": 1707800000000,
				"modelId": "copilot/auto",
				"agent": {"id": "github.copilot.workspace"},
				"response": [{"kind": "mcpServersStarting"}]
			}
		]
	}`
	path := writeCopilotTestJSON(t, content)
	info, err := parseCopilotSession(path, "/Users/jose/myapp")
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil for no edits, got %+v", info)
	}
}

func TestParseCopilotSession_FileOutsideRepo(t *testing.T) {
	content := `{
		"version": 3,
		"requests": [{
			"timestamp": 1707800000000,
			"modelId": "copilot/auto",
			"response": [{
				"kind": "textEditGroup",
				"uri": {"path": "/Users/jose/other-project/main.go"},
				"edits": [[{"text": "content"}]]
			}]
		}]
	}`
	path := writeCopilotTestJSON(t, content)
	info, err := parseCopilotSession(path, "/Users/jose/myapp")
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil for file outside repo, got %+v", info)
	}
}

func TestParseCopilotSession_ModelFallback(t *testing.T) {
	content := `{
		"version": 3,
		"requests": [{
			"timestamp": 1707800000000,
			"modelId": "copilot/claude-sonnet-4",
			"response": [{
				"kind": "textEditGroup",
				"uri": {"path": "/Users/jose/myapp/a.go"},
				"edits": [[{"text": "content"}]]
			}]
		}]
	}`
	path := writeCopilotTestJSON(t, content)
	info, err := parseCopilotSession(path, "/Users/jose/myapp")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	// No selectedModel, should fall back to request modelId
	if info.Model != "copilot/claude-sonnet-4" {
		t.Errorf("model: got %q, want %q", info.Model, "copilot/claude-sonnet-4")
	}
}

func TestParseCopilotSession_EmptyRequests(t *testing.T) {
	content := `{"version": 3, "requests": []}`
	path := writeCopilotTestJSON(t, content)
	info, err := parseCopilotSession(path, "/Users/jose/myapp")
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil for empty requests, got %+v", info)
	}
}

func TestParseCopilotSession_MalformedJSON(t *testing.T) {
	path := writeCopilotTestJSON(t, "not valid json at all")
	info, err := parseCopilotSession(path, "/Users/jose/myapp")
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil for malformed JSON, got %+v", info)
	}
}

func TestDetectCopilot_Integration(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	repoRoot := "/Users/jose/myapp"

	// Set up workspace storage
	wsDir := filepath.Join(homeDir, "Library", "Application Support", "Code", "User", "workspaceStorage", "abc123")
	chatDir := filepath.Join(wsDir, "chatSessions")
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write workspace.json
	wsJSON := copilotWorkspace{Folder: "file://" + repoRoot}
	data, err := json.Marshal(wsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "workspace.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Write session with edits
	if err := os.WriteFile(filepath.Join(chatDir, "session-1.json"), []byte(testCopilotSession), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := detectCopilot(repoRoot, 72*time.Hour)
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
	if info.Model != "gpt-5-mini" {
		t.Errorf("model: got %q, want %q", info.Model, "gpt-5-mini")
	}
}

func TestDetectCopilot_NoWorkspace(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	info, err := detectCopilot("/some/repo", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil, got %+v", info)
	}
}

func TestDetectCopilot_MergesSessions(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	repoRoot := "/Users/jose/myapp"

	wsDir := filepath.Join(homeDir, "Library", "Application Support", "Code", "User", "workspaceStorage", "abc123")
	chatDir := filepath.Join(wsDir, "chatSessions")
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatal(err)
	}

	wsJSON := copilotWorkspace{Folder: "file://" + repoRoot}
	data, err := json.Marshal(wsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "workspace.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Session 1: edits a.go
	session1 := `{
		"version": 3,
		"requests": [{
			"timestamp": 1707800000000,
			"modelId": "copilot/auto",
			"response": [{
				"kind": "textEditGroup",
				"uri": {"path": "/Users/jose/myapp/a.go"},
				"edits": [[{"text": "content"}]]
			}]
		}],
		"selectedModel": {"identifier": "copilot/auto", "metadata": {"family": "gpt-4o"}}
	}`
	if err := os.WriteFile(filepath.Join(chatDir, "s1.json"), []byte(session1), 0644); err != nil {
		t.Fatal(err)
	}

	// Session 2: edits b.go with newer model
	session2 := `{
		"version": 3,
		"requests": [{
			"timestamp": 1707800000000,
			"modelId": "copilot/auto",
			"response": [{
				"kind": "textEditGroup",
				"uri": {"path": "/Users/jose/myapp/b.go"},
				"edits": [[{"text": "content"}]]
			}]
		}],
		"selectedModel": {"identifier": "copilot/auto", "metadata": {"family": "gpt-5-mini"}}
	}`
	if err := os.WriteFile(filepath.Join(chatDir, "s2.json"), []byte(session2), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := detectCopilot(repoRoot, 72*time.Hour)
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

func writeCopilotTestJSON(t *testing.T, content string) string {
	t.Helper()
	tmpFile := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return tmpFile
}
