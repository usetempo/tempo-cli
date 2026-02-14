package detector

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Copilot Agent session JSON schema (VS Code chat sessions).
//
// Sessions are stored at:
//   macOS:  ~/Library/Application Support/Code/User/workspaceStorage/{hash}/chatSessions/{uuid}.json
//   Linux:  ~/.config/Code/User/workspaceStorage/{hash}/chatSessions/{uuid}.json
//
// Workspace-to-repo mapping:
//   ~/Library/Application Support/Code/User/workspaceStorage/{hash}/workspace.json
//   → {"folder": "file:///path/to/repo"}
//
// File edits appear as response parts with kind "textEditGroup":
//   {"kind": "textEditGroup", "uri": {"path": "/abs/path/to/file"}, "edits": [...]}
//
// Agent mode is identified by requests[].agent.id containing "editsAgent" or "workspace".

type copilotSession struct {
	Requests      []copilotRequest      `json:"requests"`
	SelectedModel *copilotSelectedModel `json:"selectedModel"`
}

type copilotRequest struct {
	Timestamp int64              `json:"timestamp"` // unix ms
	ModelID   string             `json:"modelId"`
	Agent     *copilotAgent      `json:"agent"`
	Response  []copilotRespPart  `json:"response"`
}

type copilotAgent struct {
	ID string `json:"id"`
}

type copilotRespPart struct {
	Kind string      `json:"kind"`
	URI  *copilotURI `json:"uri,omitempty"`
}

type copilotURI struct {
	Path string `json:"path"`
}

type copilotSelectedModel struct {
	Identifier string                `json:"identifier"`
	Metadata   *copilotModelMetadata `json:"metadata"`
}

type copilotModelMetadata struct {
	Family  string `json:"family"`
	Version string `json:"version"`
}

type copilotWorkspace struct {
	Folder string `json:"folder"` // "file:///path/to/repo"
}

// vscodeBaseDirs returns the VS Code workspace storage base directories
// for the current OS. Checks both VS Code and VS Code Insiders.
func vscodeBaseDirs() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var bases []string
	switch runtime.GOOS {
	case "darwin":
		appSupport := filepath.Join(homeDir, "Library", "Application Support")
		bases = []string{
			filepath.Join(appSupport, "Code", "User", "workspaceStorage"),
			filepath.Join(appSupport, "Code - Insiders", "User", "workspaceStorage"),
		}
	case "linux":
		configDir := filepath.Join(homeDir, ".config")
		bases = []string{
			filepath.Join(configDir, "Code", "User", "workspaceStorage"),
			filepath.Join(configDir, "Code - Insiders", "User", "workspaceStorage"),
		}
	}
	return bases
}

// findCopilotWorkspace finds the VS Code workspace storage directory
// whose workspace.json maps to the given repo root.
func findCopilotWorkspace(repoRoot string) string {
	for _, baseDir := range vscodeBaseDirs() {
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
			var ws copilotWorkspace
			if err := json.Unmarshal(data, &ws); err != nil {
				continue
			}
			folder := uriToPath(ws.Folder)
			if folder == repoRoot {
				return filepath.Join(baseDir, entry.Name())
			}
		}
	}
	return ""
}

// uriToPath converts a file:// URI to a local path.
func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	u, err := url.Parse(uri)
	if err != nil {
		return strings.TrimPrefix(uri, "file://")
	}
	return u.Path
}

// findCopilotSessions finds recent chat session JSON files in the workspace dir.
func findCopilotSessions(workspaceDir string, maxAge time.Duration) ([]string, error) {
	chatDir := filepath.Join(workspaceDir, "chatSessions")
	entries, err := os.ReadDir(chatDir)
	if err != nil {
		return nil, nil
	}

	cutoff := time.Now().Add(-maxAge)
	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			continue
		}
		sessions = append(sessions, filepath.Join(chatDir, entry.Name()))
	}
	return sessions, nil
}

// parseCopilotSession reads a Copilot chat session JSON file and extracts
// file-level edit information. Only returns data if the session contains
// textEditGroup entries (Agent mode file edits).
func parseCopilotSession(jsonPath string, repoRoot string) (*SessionInfo, error) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, err
	}

	var session copilotSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, nil
	}

	info := &SessionInfo{
		Tool:         ToolCopilot,
		FilesWritten: make(map[string]struct{}),
	}

	var firstTimestamp, lastTimestamp int64

	// Extract model from selectedModel metadata
	if session.SelectedModel != nil && session.SelectedModel.Metadata != nil {
		if family := session.SelectedModel.Metadata.Family; family != "" {
			info.Model = family
		}
	}

	for _, req := range session.Requests {
		// Track timestamps for session duration
		if req.Timestamp > 0 {
			if firstTimestamp == 0 || req.Timestamp < firstTimestamp {
				firstTimestamp = req.Timestamp
			}
			if req.Timestamp > lastTimestamp {
				lastTimestamp = req.Timestamp
			}
		}

		// Fall back to per-request modelId if no selectedModel
		if info.Model == "" && req.ModelID != "" {
			info.Model = req.ModelID
		}

		// Extract edited files from textEditGroup response parts
		for _, part := range req.Response {
			if part.Kind != "textEditGroup" {
				continue
			}
			if part.URI == nil || part.URI.Path == "" {
				continue
			}
			absPath := part.URI.Path
			relPath := strings.TrimPrefix(absPath, repoRoot+"/")
			if relPath != absPath {
				info.FilesWritten[relPath] = struct{}{}
			}
		}
	}

	if len(info.FilesWritten) == 0 {
		return nil, nil
	}

	if firstTimestamp > 0 && lastTimestamp > 0 {
		info.SessionDurationSec = (lastTimestamp - firstTimestamp) / 1000
	}

	return info, nil
}

// detectCopilot finds recent Copilot Agent sessions for the repo
// and merges their file sets.
func detectCopilot(repoRoot string, maxAge time.Duration) (*SessionInfo, error) {
	workspaceDir := findCopilotWorkspace(repoRoot)
	if workspaceDir == "" {
		return nil, nil
	}

	sessions, err := findCopilotSessions(workspaceDir, maxAge)
	if err != nil || len(sessions) == 0 {
		return nil, nil
	}

	merged := &SessionInfo{
		Tool:         ToolCopilot,
		FilesWritten: make(map[string]struct{}),
	}

	for _, path := range sessions {
		session, err := parseCopilotSession(path, repoRoot)
		if err != nil || session == nil {
			continue
		}
		for f := range session.FilesWritten {
			merged.FilesWritten[f] = struct{}{}
		}
		if session.Model != "" {
			merged.Model = session.Model
		}
		if session.SessionDurationSec > merged.SessionDurationSec {
			merged.SessionDurationSec = session.SessionDurationSec
		}
	}

	if len(merged.FilesWritten) == 0 {
		return nil, nil
	}
	return merged, nil
}
