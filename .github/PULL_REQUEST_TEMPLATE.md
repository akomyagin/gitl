## What does this change

<!-- One or two sentences: what does this PR do, and why. -->

## Related issue

<!-- Link an issue if there is one. For anything non-trivial, please open
     one first — see CONTRIBUTING.md. -->

## Checklist

- [ ] `go build ./...`, `go vet ./...`, and `go test -race ./...` pass locally
- [ ] `gofmt -l .` prints nothing
- [ ] Tests added/updated for the behavior change (if any)
- [ ] README / README_RU updated (if user-facing behavior changed)
- [ ] No new network calls in offline mode (if this touches `internal/llm` or `internal/cli`)
