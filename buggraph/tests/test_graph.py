from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from buggraph.bridge import AgentReply
from buggraph.graph import build_graph


class FakeBridge:
    def __init__(self, outputs: list[str]) -> None:
        self.outputs = iter(outputs)
        self.engines: list[str] = []

    def run(self, *, engine: str, cwd: str, prompt: str, timeout: int) -> AgentReply:
        self.engines.append(engine)
        return AgentReply(True, next(self.outputs))


def initial(worktree: str) -> dict:
    return {
        "record_id": "rec1", "title": "broken", "description": "fails",
        "clarifications": "", "worktree": worktree, "base_ref": "main", "test_cmd": "",
        "fix_agent": "codex", "review_agent": "cursor", "fix_timeout": 10, "review_timeout": 10,
        "test_timeout": 10, "max_repairs": 2, "iteration": 0, "status": "NEW",
    }


class GraphTest(unittest.TestCase):
    def test_passes_with_distinct_reviewer(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge(["DIAGNOSED\nroot cause", "FIXED\nchanged a.go", "PASS\nlooks good"])
            result = build_graph(bridge).invoke(initial(tmp))
        self.assertEqual("PASS", result["status"])
        self.assertEqual(["codex", "codex", "cursor"], bridge.engines)
        self.assertEqual(1, result["iteration"])

    def test_reports_live_pipeline_events(self) -> None:
        events: list[dict] = []
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge(["DIAGNOSED\nroot cause", "FIXED\nchanged a.go", "PASS\nlooks good"])
            result = build_graph(bridge, on_event=events.append).invoke(initial(tmp))
        self.assertEqual("PASS", result["status"])
        self.assertEqual(
            [
                ("investigate", "running"), ("investigate", "done"),
                ("fix", "running"), ("fix", "done"),
                ("test", "running"), ("test", "done"),
                ("review", "running"), ("review", "done"),
            ],
            [(event["stage"], event["state"]) for event in events],
        )

    def test_needs_input_stops_before_fix(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge(["NEEDS_INPUT\nplease provide stack trace"])
            result = build_graph(bridge).invoke(initial(tmp))
        self.assertEqual("NEEDS_INPUT", result["status"])
        self.assertEqual(["codex"], bridge.engines)

    def test_review_failure_loops_once(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge([
                "DIAGNOSED\nroot cause", "FIXED\nfirst", "FAIL\nmissing test",
                "FIXED\nsecond", "PASS\nnow good",
            ])
            result = build_graph(bridge).invoke(initial(tmp))
        self.assertEqual("PASS", result["status"])
        self.assertEqual(2, result["iteration"])
        self.assertEqual(["codex", "codex", "cursor", "codex", "cursor"], bridge.engines)

    def test_review_failure_blocks_at_limit(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge([
                "DIAGNOSED\nroot cause", "FIXED\nfirst", "FAIL\nno", "FIXED\nsecond", "FAIL\nstill no",
            ])
            result = build_graph(bridge).invoke(initial(tmp))
        self.assertEqual("BLOCKED", result["status"])
        self.assertIn("最大返修轮次", result["summary"])

    def test_test_failure_never_reaches_reviewer_and_blocks_at_limit(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge(["DIAGNOSED\nroot cause", "FIXED\nfirst", "FIXED\nsecond"])
            state = initial(tmp)
            state["test_cmd"] = "/usr/bin/false"
            result = build_graph(bridge).invoke(state)
        self.assertEqual("BLOCKED", result["status"])
        self.assertFalse(result["test_ok"])
        self.assertIn("最后测试失败", result["summary"])
        self.assertEqual(["codex", "codex", "codex"], bridge.engines)

    def test_test_failure_returns_to_fix_before_review(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            marker = Path(tmp) / "test-passed"
            command = f"test -f {marker} || (touch {marker} && exit 1)"
            bridge = FakeBridge([
                "DIAGNOSED\nroot cause", "FIXED\nfirst", "FIXED\nsecond", "PASS\nlooks good",
            ])
            state = initial(tmp)
            state["test_cmd"] = command
            result = build_graph(bridge).invoke(state)
        self.assertEqual("PASS", result["status"])
        self.assertTrue(result["test_ok"])
        self.assertEqual(2, result["iteration"])
        self.assertEqual(["codex", "codex", "codex", "cursor"], bridge.engines)


if __name__ == "__main__":
    unittest.main()
