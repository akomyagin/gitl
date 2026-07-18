package llm

import "testing"

func TestParseChangelogResponse(t *testing.T) {
	t.Parallel()

	valid := "Some prose the model wrote first.\n\n" +
		"```changelog\n" +
		`{"categories": {"Added": [{"subject": "New feature", "hashes": ["abc1234"]}]}, "breaking": []}` +
		"\n```\n"

	tests := []struct {
		name    string
		content string
		wantOK  bool
	}{
		{"valid block with surrounding prose", valid, true},
		{"no fenced block at all", "just prose, no block", false},
		{"empty content", "", false},
		{
			"json-tagged block is not a changelog block",
			"```json\n{\"categories\": {\"Added\": []}}\n```\n",
			false,
		},
		{
			"invalid JSON inside the block",
			"```changelog\n{not json at all\n```\n",
			false,
		},
		{
			"valid JSON but missing categories object",
			"```changelog\n{\"breaking\": []}\n```\n",
			false,
		},
		{
			"empty categories object is still valid",
			"```changelog\n{\"categories\": {}}\n```\n",
			true,
		},
		{
			"uppercase language tag accepted",
			"```CHANGELOG\n{\"categories\": {}}\n```\n",
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, ok := ParseChangelogResponse(tc.content)
			if ok != tc.wantOK {
				t.Errorf("ParseChangelogResponse(...) ok = %v, want %v\ncontent:\n%s", ok, tc.wantOK, tc.content)
			}
		})
	}
}

func TestParseChangelogResponsePayload(t *testing.T) {
	t.Parallel()
	content := "```changelog\n" +
		`{"categories": {"Added": [{"subject": "New feature", "hashes": ["abc1234", "def5678"]}], "Fixed": [{"subject": "Bug gone", "hashes": ["1111111"]}]}, "breaking": [{"subject": "API break", "hashes": ["abc1234"]}]}` +
		"\n```"

	p, ok := ParseChangelogResponse(content)
	if !ok {
		t.Fatal("expected payload to parse")
	}
	if len(p.Categories["Added"]) != 1 || p.Categories["Added"][0].Subject != "New feature" {
		t.Errorf("Added = %+v", p.Categories["Added"])
	}
	if got := p.Categories["Added"][0].Hashes; len(got) != 2 || got[0] != "abc1234" {
		t.Errorf("Added hashes = %v", got)
	}
	if len(p.Categories["Fixed"]) != 1 {
		t.Errorf("Fixed = %+v", p.Categories["Fixed"])
	}
	if len(p.Breaking) != 1 || p.Breaking[0].Subject != "API break" {
		t.Errorf("Breaking = %+v", p.Breaking)
	}
}

// The LAST valid ```changelog block wins — same convention as ParseRisk.
func TestParseChangelogResponseLastBlockWins(t *testing.T) {
	t.Parallel()
	content := "```changelog\n" +
		`{"categories": {"Added": [{"subject": "first", "hashes": ["aaaaaaa"]}]}}` +
		"\n```\nsome revision prose\n```changelog\n" +
		`{"categories": {"Added": [{"subject": "second", "hashes": ["bbbbbbb"]}]}}` +
		"\n```"

	p, ok := ParseChangelogResponse(content)
	if !ok {
		t.Fatal("expected payload to parse")
	}
	if p.Categories["Added"][0].Subject != "second" {
		t.Errorf("subject = %q, want the LAST block's %q", p.Categories["Added"][0].Subject, "second")
	}
}

// An invalid last block does not shadow an earlier valid one.
func TestParseChangelogResponseSkipsInvalidLastBlock(t *testing.T) {
	t.Parallel()
	content := "```changelog\n" +
		`{"categories": {"Added": [{"subject": "good", "hashes": ["aaaaaaa"]}]}}` +
		"\n```\n```changelog\n{broken json\n```"

	p, ok := ParseChangelogResponse(content)
	if !ok {
		t.Fatal("expected the earlier valid block to be used")
	}
	if p.Categories["Added"][0].Subject != "good" {
		t.Errorf("subject = %q, want %q", p.Categories["Added"][0].Subject, "good")
	}
}
