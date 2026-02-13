package detector

import (
	"os/exec"
	"runtime"
)

// processNames maps executable names to AI tools for process detection.
var processNames = map[string]Tool{
	"claude":         ToolClaudeCode,
	"Cursor":         ToolCursor,
	"cursor":         ToolCursor,
	"copilot-agent":  ToolCopilot,
	"github-copilot": ToolCopilot,
	"aider":          ToolAider,
	"codex":          ToolCodex,
}

// detectProcesses checks for running AI tool processes.
// Only works on macOS/Linux. Returns nil on Windows.
func detectProcesses() []Tool {
	if runtime.GOOS == "windows" {
		return nil
	}

	var detected []Tool
	seen := make(map[Tool]bool)

	for name, tool := range processNames {
		if seen[tool] {
			continue
		}
		cmd := exec.Command("pgrep", "-x", name)
		if err := cmd.Run(); err == nil {
			seen[tool] = true
			detected = append(detected, tool)
		}
	}
	return detected
}
