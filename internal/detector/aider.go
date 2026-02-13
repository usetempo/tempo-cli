package detector

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// detectAider parses .aider.chat.history.md in the repo root and extracts
// file paths from #### headers.
func detectAider(repoRoot string, maxAge time.Duration) (*SessionInfo, error) {
	historyPath := filepath.Join(repoRoot, ".aider.chat.history.md")
	f, err := os.Open(historyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if time.Since(stat.ModTime()) > maxAge {
		return nil, nil
	}

	info := &SessionInfo{
		Tool:         ToolAider,
		FilesWritten: make(map[string]struct{}),
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#### ") {
			filePath := strings.TrimSpace(strings.TrimPrefix(line, "#### "))
			if filePath != "" && !strings.Contains(filePath, " ") {
				info.FilesWritten[filePath] = struct{}{}
			}
		}
	}

	if len(info.FilesWritten) == 0 {
		return nil, nil
	}
	return info, scanner.Err()
}
