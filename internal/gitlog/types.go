package gitlog

import "time"

// Commit is a single commit parsed from `git log` output.
type Commit struct {
	Hash    string
	Author  string
	Date    time.Time
	Subject string
	Body    string
	Files   []FileChange
}

// FileChange is one entry of a `--name-status` block.
type FileChange struct {
	// Status is the raw git status letter(s): "A", "M", "D", "R100", "C75", ...
	Status string
	// Path is the (new) path of the file.
	Path string
	// Old is the previous path for renames/copies (R/C statuses); empty otherwise.
	Old string
}
