package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const startMarker = "# --- TEMPO CLI HOOK ---"
const endMarker = "# --- END TEMPO CLI HOOK ---"

const postCommitHook = `# --- TEMPO CLI HOOK ---
if command -v tempo-cli >/dev/null 2>&1; then
  tempo-cli _detect --hook post-commit
fi
# --- END TEMPO CLI HOOK ---`

const prePushHook = `# --- TEMPO CLI HOOK ---
if command -v tempo-cli >/dev/null 2>&1; then
  tempo-cli _sync
fi
# --- END TEMPO CLI HOOK ---`

// Install installs post-commit and pre-push hooks in the given repo.
func Install(repoRoot string) error {
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return err
	}

	if err := installHook(hooksDir, "post-commit", postCommitHook); err != nil {
		return fmt.Errorf("post-commit: %w", err)
	}
	if err := installHook(hooksDir, "pre-push", prePushHook); err != nil {
		return fmt.Errorf("pre-push: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(repoRoot, ".tempo", "pending"), 0755); err != nil {
		return err
	}

	return ensureGitignore(repoRoot)
}

// Uninstall removes Tempo's hook sections from post-commit and pre-push.
func Uninstall(repoRoot string) error {
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	for _, name := range []string{"post-commit", "pre-push"} {
		if err := removeHookSection(hooksDir, name); err != nil {
			return err
		}
	}
	return nil
}

// IsInstalled checks if Tempo hooks are present.
func IsInstalled(repoRoot string) bool {
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	path := filepath.Join(hooksDir, "post-commit")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), startMarker)
}

func installHook(hooksDir, name, content string) error {
	path := filepath.Join(hooksDir, name)
	existing, err := os.ReadFile(path)

	var newContent string
	if err != nil && os.IsNotExist(err) {
		// New hook file
		newContent = "#!/bin/sh\n" + content + "\n"
	} else if err != nil {
		return err
	} else if strings.Contains(string(existing), startMarker) {
		// Replace existing Tempo section
		newContent = replaceSection(string(existing), content)
	} else {
		// Append to existing hook
		s := string(existing)
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		newContent = s + content + "\n"
	}

	return os.WriteFile(path, []byte(newContent), 0755)
}

func removeHookSection(hooksDir, name string) error {
	path := filepath.Join(hooksDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	content := string(data)
	if !strings.Contains(content, startMarker) {
		return nil
	}

	cleaned := removeSection(content)
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" || cleaned == "#!/bin/sh" {
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(cleaned+"\n"), 0755)
}

func replaceSection(content, newSection string) string {
	lines := strings.Split(content, "\n")
	var result []string
	inSection := false

	for _, line := range lines {
		if strings.TrimSpace(line) == startMarker {
			inSection = true
			continue
		}
		if strings.TrimSpace(line) == endMarker {
			inSection = false
			continue
		}
		if !inSection {
			result = append(result, line)
		}
	}

	// Remove trailing empty lines before appending new section
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}

	out := strings.Join(result, "\n")
	if out != "" {
		out += "\n"
	}
	out += newSection + "\n"
	return out
}

func removeSection(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	inSection := false

	for _, line := range lines {
		if strings.TrimSpace(line) == startMarker {
			inSection = true
			continue
		}
		if strings.TrimSpace(line) == endMarker {
			inSection = false
			continue
		}
		if !inSection {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

func ensureGitignore(repoRoot string) error {
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	content := string(data)
	if strings.Contains(content, ".tempo/") {
		return nil
	}

	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += ".tempo/\n"

	return os.WriteFile(gitignorePath, []byte(content), 0644)
}
