package gitlog

import (
	"regexp"
	"strings"
)

// Remote URL forms that identify a hosted git repository: https (with or
// without a trailing .git) and ssh scp-like syntax. Host is captured, not
// assumed, so GitHub Enterprise / self-hosted remotes parse identically.
// Owner/repo comparison downstream is case-insensitive (GitHub semantics).
var (
	httpsRemotePattern = regexp.MustCompile(`(?i)^https://([^/]+)/([^/]+)/([^/]+?)(?:\.git)?/?$`)
	sshRemotePattern   = regexp.MustCompile(`(?i)^(?:ssh://)?git@([^:/]+)[:/]([^/]+)/([^/]+?)(?:\.git)?$`)
)

// RemoteRepo is a parsed hosted-repository identity: scheme-free, .git-free.
type RemoteRepo struct {
	Host  string
	Owner string
	Repo  string
}

// ParseRemoteURL parses a `git remote get-url` result (https or ssh scp-like
// form) into its host/owner/repo. ok=false for anything unrecognized (a bare
// path, a local file remote, malformed input) — never an error. Pure function.
func ParseRemoteURL(remoteURL string) (RemoteRepo, bool) {
	u := strings.TrimSpace(remoteURL)
	for _, re := range []*regexp.Regexp{httpsRemotePattern, sshRemotePattern} {
		if m := re.FindStringSubmatch(u); m != nil {
			return RemoteRepo{Host: m[1], Owner: m[2], Repo: m[3]}, true
		}
	}
	return RemoteRepo{}, false
}
