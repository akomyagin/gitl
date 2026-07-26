package gitlog

import "testing"

// TestParseRemoteURL: table-driven check of the shared remote-URL parser —
// https (with/without .git, trailing slash), ssh scp-like and ssh:// forms,
// plus unrecognized inputs (local paths, other schemes, malformed).
func TestParseRemoteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want RemoteRepo
		ok   bool
	}{
		{"https with .git", "https://github.com/owner/repo.git", RemoteRepo{"github.com", "owner", "repo"}, true},
		{"https without .git", "https://github.com/owner/repo", RemoteRepo{"github.com", "owner", "repo"}, true},
		{"https trailing slash", "https://github.com/owner/repo/", RemoteRepo{"github.com", "owner", "repo"}, true},
		{"https GHE host", "https://ghe.example.com/Owner/Repo.git", RemoteRepo{"ghe.example.com", "Owner", "Repo"}, true},
		{"ssh scp-like", "git@github.com:owner/repo.git", RemoteRepo{"github.com", "owner", "repo"}, true},
		{"ssh scp-like without .git", "git@example.com:me/proj", RemoteRepo{"example.com", "me", "proj"}, true},
		{"ssh:// scheme", "ssh://git@github.com/owner/repo.git", RemoteRepo{"github.com", "owner", "repo"}, true},
		{"surrounding whitespace", "  https://github.com/owner/repo.git\n", RemoteRepo{"github.com", "owner", "repo"}, true},
		{"local path", "/home/user/repos/proj", RemoteRepo{}, false},
		{"file scheme", "file:///home/user/repos/proj", RemoteRepo{}, false},
		{"http (not https)", "http://github.com/owner/repo.git", RemoteRepo{}, false},
		{"empty", "", RemoteRepo{}, false},
		{"garbage", "not a url at all", RemoteRepo{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseRemoteURL(tt.url)
			if ok != tt.ok {
				t.Fatalf("ParseRemoteURL(%q) ok = %v, want %v", tt.url, ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("ParseRemoteURL(%q) = %+v, want %+v", tt.url, got, tt.want)
			}
		})
	}
}
