package gitlog

import (
	"regexp"
	"sort"
	"strings"
)

// Keep a Changelog category names (§9.2). CategoryOther is the catch-all for
// non-conventional commits and conventional types with no direct mapping
// (docs/style/test/build/ci/chore).
const (
	CategoryAdded      = "Added"
	CategoryChanged    = "Changed"
	CategoryDeprecated = "Deprecated"
	CategoryRemoved    = "Removed"
	CategoryFixed      = "Fixed"
	CategorySecurity   = "Security"
	CategoryOther      = "Other"
)

// CategoryOrder is the fixed, documented print order for changelog output
// (§9.4): breaking changes first, then Keep a Changelog categories, Other
// last.
var CategoryOrder = []string{
	CategoryAdded,
	CategoryChanged,
	CategoryDeprecated,
	CategoryRemoved,
	CategoryFixed,
	CategorySecurity,
	CategoryOther,
}

// typeToCategory maps a lowercased conventional-commit type to its Keep a
// Changelog category (§9.2).
var typeToCategory = map[string]string{
	"feat":      CategoryAdded,
	"fix":       CategoryFixed,
	"perf":      CategoryChanged,
	"refactor":  CategoryChanged,
	"revert":    CategoryChanged,
	"deprecate": CategoryDeprecated,
	"remove":    CategoryRemoved,
	"security":  CategorySecurity,
	"docs":      CategoryOther,
	"style":     CategoryOther,
	"test":      CategoryOther,
	"build":     CategoryOther,
	"ci":        CategoryOther,
	"chore":     CategoryOther,
}

// conventionalPrefixRe matches the conventional-commits header grammar:
// type, optional (scope), optional breaking "!", then ": ".
// e.g. "feat(auth)!: rework session store".
var conventionalPrefixRe = regexp.MustCompile(`^(\w+)(\([^)]*\))?(!)?:\s*(.*)$`)

// ChangelogEntry is one commit rendered into a changelog category.
type ChangelogEntry struct {
	Hash     string // short (7-char) hash, per §9.4 uniform styling
	Subject  string // subject with the "type(scope)!:" prefix stripped
	Category string
	Breaking bool
	// BreakingText is the callout text used in the "⚠ BREAKING CHANGES"
	// section: the first line after "BREAKING CHANGE:" in the body, or the
	// stripped subject if the only marker was "!". Empty when !Breaking.
	BreakingText string
}

// Changelog is the categorized result of a commit range (§9.2, §9.4).
type Changelog struct {
	// Categories maps every CategoryOrder name to its entries (possibly
	// empty, but the key is always present — §9.4 JSON contract).
	Categories map[string][]ChangelogEntry
	// Breaking is the ordered list of breaking-change entries, duplicated
	// from their home category (§9.2).
	Breaking []ChangelogEntry
}

// breakingChangeRe matches a "BREAKING CHANGE:" footer line (conventional-
// commits spec — case-sensitive by convention).
var breakingChangeRe = regexp.MustCompile(`(?m)^BREAKING CHANGE:\s*(.*)$`)

// CategorizeCommits classifies commits into Keep a Changelog categories
// (§9.2). Merge commits (Subject starting with "Merge ") are excluded
// entirely. Duplicate hashes keep only the first occurrence.
func CategorizeCommits(commits []Commit) Changelog {
	cl := Changelog{Categories: make(map[string][]ChangelogEntry, len(CategoryOrder))}
	for _, name := range CategoryOrder {
		cl.Categories[name] = nil
	}

	seen := make(map[string]bool, len(commits))
	for _, c := range commits {
		if strings.HasPrefix(c.Subject, "Merge ") {
			continue
		}
		if seen[c.Hash] {
			continue
		}
		seen[c.Hash] = true

		entry := categorizeOne(c)
		cl.Categories[entry.Category] = append(cl.Categories[entry.Category], entry)
		if entry.Breaking {
			cl.Breaking = append(cl.Breaking, entry)
		}
	}
	return cl
}

// categorizeOne classifies a single commit.
func categorizeOne(c Commit) ChangelogEntry {
	hash := c.Hash
	if len(hash) > 7 {
		hash = hash[:7]
	}

	m := conventionalPrefixRe.FindStringSubmatch(c.Subject)
	bangBreaking := false
	category := CategoryOther
	subject := c.Subject

	if m != nil {
		typ := strings.ToLower(m[1])
		bangBreaking = m[3] == "!"
		subject = m[4]
		if cat, ok := typeToCategory[typ]; ok {
			category = cat
		}
	}

	footerText, footerBreaking := breakingFooter(c.Body)
	breaking := bangBreaking || footerBreaking

	entry := ChangelogEntry{
		Hash:     hash,
		Subject:  subject,
		Category: category,
		Breaking: breaking,
	}
	if breaking {
		if footerText != "" {
			entry.BreakingText = footerText
		} else {
			entry.BreakingText = subject
		}
	}
	return entry
}

// breakingFooter extracts the first "BREAKING CHANGE:" footer line from a
// commit body, if present.
func breakingFooter(body string) (text string, found bool) {
	m := breakingChangeRe.FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// MissingRequiredCategories reports which of the required category names
// have zero entries in cl, sorted alphabetically for a deterministic CI
// message regardless of the order required categories were configured in
// (§9.3). Unknown category names (typos in policy config) are reported too —
// CategorizeCommits only ever writes to CategoryOrder keys, so an unknown
// name is always "missing".
func MissingRequiredCategories(cl Changelog, required []string) []string {
	var missing []string
	for _, name := range required {
		if len(cl.Categories[name]) == 0 {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
