## Agent skills

### Issue tracker

Issues live in this repo's GitHub Issues (`EziosWJ/canteen-wallet`), managed with the `gh` CLI. See `docs/agents/issue-tracker.md`.

Sandbox gotcha: `gh auth status` can report an invalid token, and issue queries can fail on the local proxy, even when host authentication works. If `gh` fails in the sandbox, retry `gh auth status` and a read-only issue query outside it before diagnosing credentials. If both succeed, run the requested `gh` operation outside the sandbox.

### Triage labels

The five canonical triage roles map to labels of the same name. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `CONTEXT.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.
