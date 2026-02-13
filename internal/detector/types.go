package detector

// Confidence levels for AI tool detection.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
)

// Method describes how the AI tool was detected.
type Method string

const (
	MethodFileMatch       Method = "file-match"
	MethodProcess         Method = "process"
	MethodCoAuthorTrailer Method = "co-author-trailer"
)

// Tool identifies an AI coding tool.
type Tool string

const (
	ToolClaudeCode Tool = "claude-code"
	ToolAider      Tool = "aider"
	ToolCursor     Tool = "cursor"
	ToolCopilot    Tool = "copilot"
	ToolCodex      Tool = "codex"
)

// Detection represents a single AI tool detection for a commit.
type Detection struct {
	Tool               Tool       `json:"tool"`
	Confidence         Confidence `json:"confidence"`
	Method             Method     `json:"method"`
	FilesMatched       []string   `json:"files_matched,omitempty"`
	FilesCommitted     int        `json:"files_committed"`
	AIFiles            int        `json:"ai_files"`
	Model              string     `json:"model,omitempty"`
	TokenUsage         int64      `json:"token_usage,omitempty"`
	SessionDurationSec int64      `json:"session_duration_sec,omitempty"`
}

// Attribution is the full payload for one commit.
type Attribution struct {
	CommitSHA    string      `json:"commit_sha"`
	CommitAuthor string      `json:"commit_author"`
	Repo         string      `json:"repo"`
	Timestamp    string      `json:"timestamp"`
	Detections   []Detection `json:"detections"`
}

// SessionInfo holds metadata extracted from an AI tool session.
type SessionInfo struct {
	Tool               Tool
	FilesWritten       map[string]struct{}
	Model              string
	TotalTokens        int64
	SessionDurationSec int64
}
