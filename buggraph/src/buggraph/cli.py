from __future__ import annotations

import json
import os
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

os.environ.setdefault("LANGGRAPH_STRICT_MSGPACK", "true")

from langgraph.checkpoint.sqlite import SqliteSaver

from .bridge import AgentBridge
from .graph import build_graph


def progress_writer(path_value: str):
    path_text = str(path_value or "").strip()
    if not path_text:
        return None
    path = Path(path_text)
    path.parent.mkdir(parents=True, exist_ok=True)

    def write(event: dict) -> None:
        payload = dict(event)
        payload["ts"] = time.time()
        payload["time"] = datetime.now(timezone.utc).isoformat()
        with path.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(payload, ensure_ascii=False) + "\n")

    return write


def run(payload: dict) -> dict:
    required = {"record_id", "title", "description", "worktree", "base_ref", "fix_agent", "review_agent", "agent_runner"}
    missing = sorted(key for key in required if not str(payload.get(key, "")).strip())
    if missing:
        raise ValueError(f"missing required fields: {', '.join(missing)}")
    if payload["fix_agent"] == payload["review_agent"]:
        raise ValueError("fix_agent and review_agent must be different")

    checkpoint_db = Path(payload["checkpoint_db"])
    checkpoint_db.parent.mkdir(parents=True, exist_ok=True)
    state = {
        "record_id": payload["record_id"],
        "title": payload["title"],
        "description": payload["description"],
        "clarifications": payload.get("clarifications", ""),
        "worktree": payload["worktree"],
        "base_ref": payload["base_ref"],
        "test_cmd": payload.get("test_cmd", ""),
        "fix_agent": payload["fix_agent"],
        "review_agent": payload["review_agent"],
        "fix_timeout": int(payload.get("fix_timeout", 1800)),
        "review_timeout": int(payload.get("review_timeout", 900)),
        "test_timeout": int(payload.get("test_timeout", 1800)),
        "max_repairs": max(1, int(payload.get("max_repairs", 2))),
        "iteration": 0,
        "fix_session_id": payload.get("initial_session_id", ""),
        "diagnosis": payload.get("initial_diagnosis", ""),
        "affected_files": payload.get("initial_affected_files", []),
        "skip_investigation": bool(payload.get("skip_investigation", False)),
        "stop_after_investigation": bool(payload.get("stop_after_investigation", False)),
        "status": "DIAGNOSED" if payload.get("skip_investigation", False) else "NEW",
    }
    config = {"configurable": {"thread_id": payload.get("thread_id", f"bug:{payload['record_id']}")}}
    bridge = AgentBridge(payload["agent_runner"])
    on_event = progress_writer(payload.get("progress_file", ""))
    with SqliteSaver.from_conn_string(str(checkpoint_db)) as checkpointer:
        graph = build_graph(bridge, checkpointer, on_event=on_event)
        snapshot = graph.get_state(config)
        if snapshot.values and snapshot.next:
            result = graph.invoke(None, config=config)
        else:
            result = graph.invoke(state, config=config)
    return {
        "status": result.get("status", "BLOCKED"),
        "summary": result.get("summary", ""),
        "diagnosis": result.get("diagnosis", ""),
        "affected_files": result.get("affected_files", []),
        "fix_session_id": result.get("fix_session_id", ""),
        "questions": result.get("questions", ""),
        "test_ok": bool(result.get("test_ok", False)),
        "test_output": result.get("test_output", ""),
        "review_output": result.get("review_output", ""),
        "iteration": int(result.get("iteration", 0)),
    }


def main() -> int:
    try:
        payload = json.load(sys.stdin)
        json.dump(run(payload), sys.stdout, ensure_ascii=False)
        sys.stdout.write("\n")
        return 0
    except Exception as exc:  # CLI boundary: keep stdout parseable only on success.
        print(f"buggraph failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
