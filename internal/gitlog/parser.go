package gitlog

import (
	"fmt"
	"strings"
	"time"
)

const (
	fieldSep  = "\x1f" // ASCII unit separator: between fields of one commit
	recordSep = "\x1e" // ASCII record separator: after each commit header+body
)

// ParseLog parses the output of
//
//	git log --pretty=format:%H%x1f%an%x1f%aI%x1f%s%x1f%b%x1e --name-status <range>
//
// Records are split on 0x1E and fields on 0x1F — never on "\n", because commit
// bodies legitimately contain newlines. The record separator sits at the end
// of the pretty format, so the --name-status block of commit N appears
// *after* N's record separator, i.e. at the start of the next chunk; the
// parser attributes it back to the previous commit.
func ParseLog(out string) ([]Commit, error) {
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}

	chunks := strings.Split(out, recordSep)
	var commits []Commit
	for i, chunk := range chunks {
		if i == len(chunks)-1 {
			// Trailing chunk after the last record separator: only the
			// name-status block of the last commit (or nothing).
			if len(commits) == 0 {
				if strings.TrimSpace(chunk) != "" {
					return nil, fmt.Errorf("parse git log: output has no record separator: %q", truncateForError(chunk))
				}
				break
			}
			files, err := parseNameStatus(chunk)
			if err != nil {
				return nil, err
			}
			commits[len(commits)-1].Files = files
			break
		}

		fields := strings.SplitN(chunk, fieldSep, 5)
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse git log: expected 5 fields in record, got %d: %q", len(fields), truncateForError(chunk))
		}

		// The first field carries the name-status block of the previous
		// commit (if any) followed by this commit's hash on the last line.
		nsBlock, hash := splitHead(fields[0])
		if len(commits) > 0 {
			files, err := parseNameStatus(nsBlock)
			if err != nil {
				return nil, err
			}
			commits[len(commits)-1].Files = files
		} else if strings.TrimSpace(nsBlock) != "" {
			return nil, fmt.Errorf("parse git log: unexpected content before first commit: %q", truncateForError(nsBlock))
		}

		date, err := time.Parse(time.RFC3339, fields[2])
		if err != nil {
			return nil, fmt.Errorf("parse git log: bad author date %q for commit %s: %w", fields[2], hash, err)
		}

		commits = append(commits, Commit{
			Hash:    hash,
			Author:  fields[1],
			Date:    date,
			Subject: fields[3],
			Body:    strings.TrimSpace(fields[4]),
		})
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
