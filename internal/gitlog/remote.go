package gitlog

import (
	"regexp"
	"strings"
)

// Remote URL forms that identify a hosted git repository: https (with or
// without a trailing .git) and two ssh forms. Host is captured, not assumed,
// so GitHub Enterprise / self-hosted remotes parse identically. Owner/repo
// comparison downstream is case-insensitive (GitHub semantics).
var (
	// httpsRemotePattern — HTTPS remotes.
	httpsRemotePattern = regexp.MustCompile(`(?i)^https://([^/]+)/([^/]+)/([^/]+?)(?:\.git)?/?$`)

	// sshSchemeRemotePattern — the explicit "ssh://" URL form, with an
	// OPTIONAL ":port" before the path (RFC-style ssh:// URLs put the port
	// between host and path, e.g. self-hosted GitLab/Gitea on a non-standard
	// port). Groups:
	//   1: host  — ([^:/]+)   stops at ":" (port) or "/" (path)
	//   (?::\d+)? — optional literal ":" + digits, NOT captured (the port is
	//               irrelevant to owner/repo identity)
	//   2: owner — ([^/]+)
	//   3: repo  — ([^/]+?)   lazy, so an optional ".git" suffix is stripped
	sshSchemeRemotePattern = regexp.MustCompile(`(?i)^ssh://git@([^:/]+)(?::\d+)?/([^/]+)/([^/]+?)(?:\.git)?/?$`)

	// sshSCPRemotePattern — the SCP-like short form "git@host:owner/repo(.git)"
	// with NO "ssh://" scheme and NO port: here ":" is the host/path separator,
	// not a port delimiter. Groups mirror the scheme form (host, owner, repo).
	sshSCPRemotePattern = regexp.MustCompile(`(?i)^git@([^:/]+):([^/]+)/([^/]+?)(?:\.git)?/?$`)
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
	for _, re := range []*regexp.Regexp{httpsRemotePattern, sshSchemeRemotePattern, sshSCPRemotePattern} {
		if m := re.FindStringSubmatch(u); m != nil {
			return RemoteRepo{Host: m[1], Owner: m[2], Repo: m[3]}, true
		}
	}
	return RemoteRepo{}, false
}
