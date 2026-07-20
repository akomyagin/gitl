package gitlog

import (
	"fmt"
	"strings"
	"time"
)

const fieldSep = "\x00" // separates the 5 metadata fields of each record; git
// emits exactly 5 of these per commit regardless of field content, so a flat
// split on this single byte always yields 5*N+1 tokens for N commits.

// ParseLog parses the output of
//
//	git log --pretty=format:%H%x00%an%x00%aI%x00%s%x00%b%x00 --name-status <range>
//
// The format emits exactly 5 literal NULs per commit — one after each of
// hash/author/date/subject/body — regardless of field content (an empty %s or
// %b still emits its trailing NUL), so a flat split on the single NUL byte
// yields exactly 5*N+1 tokens for N commits. Each group of 5 tokens is
// (head, author, date, subject, body); empty subjects/bodies are simply empty
// strings in their positions, never a collapsed token. Never split on "\n":
// commit bodies legitimately contain newlines. NUL is the only byte git
// refuses to store in a commit message (fsck nulInCommit), so a field can
// never contain one.
//
// The head token carries the --name-status block of the PREVIOUS commit (if
// any) followed by this commit's hash on the last line; the first record has
// no preceding block, and the final "extra" token past the last 5-token group
// is the name-status block of the last commit (no hash follows it).
func ParseLog(out string) ([]Commit, error) {
	if strings.Trim(out, "\x00\n\r\t ") == "" {
		return nil, nil
	}

	tokens := strings.Split(out, fieldSep)
	if (len(tokens)-1)%5 != 0 {
		return nil, fmt.Errorf("parse git log: malformed output (%d NUL-separated tokens): %q", len(tokens), truncateForError(out))
	}

	numRecords := (len(tokens) - 1) / 5
	var commits []Commit
	for i := 0; i < numRecords; i++ {
		base := i * 5
		headField := tokens[base]
		author := tokens[base+1]
		dateStr := tokens[base+2]
		subject := tokens[base+3]
		body := tokens[base+4]

		nsBlock, hash := splitHead(headField)
		if len(commits) > 0 {
			files, err := parseNameStatus(nsBlock)
			if err != nil {
				return nil, err
			}
			commits[len(commits)-1].Files = files
		} else if strings.TrimSpace(nsBlock) != "" {
			return nil, fmt.Errorf("parse git log: unexpected content before first commit: %q", truncateForError(nsBlock))
		}

		date, err := time.Parse(time.RFC3339, dateStr)
		if err != nil {
			return nil, fmt.Errorf("parse git log: bad author date %q for commit %s: %w", dateStr, hash, err)
		}

		commits = append(commits, Commit{
			Hash:    hash,
			Author:  author,
			Date:    date,
			Subject: subject,
			Body:    strings.TrimSpace(body),
		})
	}

	// The trailing token (past the last 5-field group) is the name-status
	// block of the LAST commit — there is no further hash after it.
	trailing := tokens[len(tokens)-1]
	if len(commits) > 0 {
		files, err := parseNameStatus(trailing)
		if err != nil {
			return nil, err
		}
		commits[len(commits)-1].Files = files
	} else if strings.TrimSpace(trailing) != "" {
		return nil, fmt.Errorf("parse git log: output has no record separator: %q", truncateForError(trailing))
	}

	return commits, nil
}

// splitHead splits the leading field of a record into the name-status block
// of the previous commit and the hash of the current one (the last non-empty
// line). The number of blank lines around the block varies, so the split is
// driven by "last line = hash", not by a fixed layout.
func splitHead(head string) (nsBlock, hash string) {
	head = strings.TrimRight(head, "\n")
	idx := strings.LastIndex(head, "\n")
	if idx < 0 {
		return "", strings.TrimSpace(head)
	}
	return head[:idx], strings.TrimSpace(head[idx+1:])
}

// parseNameStatus parses a --name-status block: lines like "M\tpath",
// "A\tpath", "D\tpath" and "R100\told\tnew" (renames/copies carry two paths).
func parseNameStatus(block string) ([]FileChange, error) {
	var files []FileChange
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		status := parts[0]
		switch {
		case strings.HasPrefix(status, "R"), strings.HasPrefix(status, "C"):
			if len(parts) != 3 {
				return nil, fmt.Errorf("parse git log: malformed rename/copy line %q", line)
			}
			files = append(files, FileChange{Status: status, Old: parts[1], Path: parts[2]})
		default:
			if len(parts) != 2 {
				return nil, fmt.Errorf("parse git log: malformed name-status line %q", line)
			}
			files = append(files, FileChange{Status: status, Path: parts[1]})
		}
	}
	return files, nil
}

func truncateForError(s string) string {
	const max = 120
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
