package detector

import (
	"testing"
)

func TestIntersect(t *testing.T) {
	tests := []struct {
		name      string
		aiFiles   map[string]struct{}
		committed map[string]struct{}
		want      []string
	}{
		{
			name:      "overlap",
			aiFiles:   map[string]struct{}{"a.go": {}, "b.go": {}},
			committed: map[string]struct{}{"b.go": {}, "c.go": {}},
			want:      []string{"b.go"},
		},
		{
			name:      "full overlap",
			aiFiles:   map[string]struct{}{"a.go": {}, "b.go": {}},
			committed: map[string]struct{}{"a.go": {}, "b.go": {}},
			want:      []string{"a.go", "b.go"},
		},
		{
			name:      "no overlap",
			aiFiles:   map[string]struct{}{"a.go": {}},
			committed: map[string]struct{}{"b.go": {}},
			want:      nil,
		},
		{
			name:      "empty ai",
			aiFiles:   map[string]struct{}{},
			committed: map[string]struct{}{"a.go": {}},
			want:      nil,
		},
		{
			name:      "empty committed",
			aiFiles:   map[string]struct{}{"a.go": {}},
			committed: map[string]struct{}{},
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intersect(tt.aiFiles, tt.committed)
			if !equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseRepoFromRemote_SSH(t *testing.T) {
	// Can't test with real git, but test the parsing logic directly
	// by checking known patterns
	tests := []struct {
		name   string
		remote string
		want   string
	}{
		{"ssh", "git@github.com:tempo-metrics/tempo.git", "tempo-metrics/tempo"},
		{"https", "https://github.com/tempo-metrics/tempo.git", "tempo-metrics/tempo"},
		{"https no .git", "https://github.com/tempo-metrics/tempo", "tempo-metrics/tempo"},
		{"http", "http://github.com/owner/repo.git", "owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRemoteURL(tt.remote)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToSet(t *testing.T) {
	s := toSet([]string{"a", "b", "c", "a"})
	if len(s) != 3 {
		t.Errorf("expected 3 unique entries, got %d", len(s))
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := s[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

func TestSessionMaxAge_Default(t *testing.T) {
	t.Setenv("TEMPO_SESSION_MAX_AGE", "")
	got := sessionMaxAge()
	want := 72 * 60 * 60 // 72 hours in seconds
	if int(got.Seconds()) != want {
		t.Errorf("got %v, want 72h", got)
	}
}

func TestSessionMaxAge_Override(t *testing.T) {
	t.Setenv("TEMPO_SESSION_MAX_AGE", "48")
	got := sessionMaxAge()
	want := 48 * 60 * 60
	if int(got.Seconds()) != want {
		t.Errorf("got %v, want 48h", got)
	}
}

func TestSessionMaxAge_Invalid(t *testing.T) {
	t.Setenv("TEMPO_SESSION_MAX_AGE", "notanumber")
	got := sessionMaxAge()
	want := 72 * 60 * 60
	if int(got.Seconds()) != want {
		t.Errorf("got %v, want 72h default on invalid input", got)
	}
}
