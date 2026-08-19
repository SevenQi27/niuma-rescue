# niuma BugGraph

Python LangGraph sidecar for the Bug-only repair workflow:

```text
investigate → report affected files → fix → test → independent review
                                      ↑              │
                                      └── FAIL (bounded)
```

The Go daemon owns Feishu, Git worktrees, agent CLI authentication/retries,
commit/push policy and the human merge gate. This package only owns graph state,
routing and SQLite checkpoints.

The Go coordinator deliberately runs the graph in two phases. It stops after
investigation, records the diagnosis and affected files, resolves file claims or
shared-session placement, and only then resumes the graph with write access. A
later Bug that overlaps an active file claim waits instead of editing the same
code concurrently.

## Setup

```bash
python3 -m venv .venv
.venv/bin/python -m pip install -e .
.venv/bin/python -m unittest discover -s tests -v
```

`PIPELINE_BUG_GRAPH_PYTHON` can override the interpreter. By default niuma
auto-detects `buggraph/.venv/bin/python` (or the Windows equivalent).

## Test gate

`test_cmd` is supplied by the selected workspace. When it is empty the graph
currently records `SKIPPED` and continues; this is not executable acceptance
evidence. Configure a real command for any codebase where test proof is required.

## Agent sessions

Claude and Codex repair runs can resume the session ID captured during
investigation. The session belongs to one Bug and is not reused across unrelated
tasks. Fix and review agents must be different.
