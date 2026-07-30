# niuma BugGraph

Python LangGraph sidecar for the Bug-only repair workflow:

```text
investigate → fix → test → independent review
                 ↑              │
                 └── FAIL (bounded)
```

The Go daemon owns Feishu, Git worktrees, agent CLI authentication/retries,
commit/push policy and the human merge gate. This package only owns graph state,
routing and SQLite checkpoints.

## Setup

```bash
python3 -m venv .venv
.venv/bin/python -m pip install -e .
.venv/bin/python -m unittest discover -s tests -v
```

`PIPELINE_BUG_GRAPH_PYTHON` can override the interpreter. By default niuma
auto-detects `buggraph/.venv/bin/python` (or the Windows equivalent).
