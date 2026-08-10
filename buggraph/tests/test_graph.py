from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from buggraph.bridge import AgentReply
from buggraph.graph import build_graph


class FakeBridge:
    def __init__(self, outputs: list[str | AgentReply]) -> None:
        self.outputs = iter(outputs)
        self.engines: list[str] = []
        self.session_ids: list[str] = []
        self.write_access: list[bool] = []
        self.prompts: list[str] = []

    def run(
        self, *, engine: str, cwd: str, prompt: str, timeout: int,
        session_id: str = "", write_access: bool = False,
    ) -> AgentReply:
        self.engines.append(engine)
        self.session_ids.append(session_id)
        self.write_access.append(write_access)
        self.prompts.append(prompt)
        output = next(self.outputs)
        return output if isinstance(output, AgentReply) else AgentReply(True, output)


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

    def test_all_agents_keep_the_task_creation_baseline(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge(["DIAGNOSED\nroot cause", "FIXED\nchanged a.go", "PASS\nlooks good"])
            state = initial(tmp)
            state["base_ref"] = "origin/main"
            result = build_graph(bridge).invoke(state)
        self.assertEqual("PASS", result["status"])
        self.assertEqual(3, len(bridge.prompts))
        for prompt in bridge.prompts:
            self.assertIn("当前 worktree 已由 Niuma 从 `origin/main` 创建", prompt)
            self.assertIn("禁止建议或执行 checkout、switch、rebase、merge、cherry-pick、reset", prompt)
            self.assertIn("分支集成、冲突处理和合并全部由人工负责", prompt)

    def test_reuses_codex_session_for_fix_and_repair(self) -> None:
        session_id = "019fdb28-2a22-7ea2-80a5-b20b2464cf28"
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge([
                AgentReply(True, "DIAGNOSED\nroot cause", session_id=session_id),
                AgentReply(True, "FIXED\nfirst", session_id=session_id),
                "FAIL\nmissing test",
                AgentReply(True, "FIXED\nsecond", session_id=session_id),
                "PASS\nnow good",
            ])
            result = build_graph(bridge).invoke(initial(tmp))
        self.assertEqual("PASS", result["status"])
        self.assertEqual(session_id, result["fix_session_id"])
        self.assertEqual(["", session_id, "", session_id, ""], bridge.session_ids)
        self.assertEqual([False, True, False, True, False], bridge.write_access)

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

    def test_can_stop_after_investigation_for_code_coordination(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge(["DIAGNOSED\nFILES:\n- src/App.java\nSUMMARY:\nroot cause"])
            state = initial(tmp)
            state["stop_after_investigation"] = True
            result = build_graph(bridge).invoke(state)
        self.assertEqual("DIAGNOSED", result["status"])
        self.assertEqual(["src/App.java"], result["affected_files"])
        self.assertEqual(["codex"], bridge.engines)

    def test_accepts_preamble_before_investigation_verdict(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge([
                "I have a comprehensive diagnosis.\n"
                "Let me write the result.\n\n"
                "DIAGNOSED\nFILES:\n- src/App.java\nSUMMARY:\nroot cause",
            ])
            state = initial(tmp)
            state["stop_after_investigation"] = True
            result = build_graph(bridge).invoke(state)
        self.assertEqual("DIAGNOSED", result["status"])
        self.assertEqual(["src/App.java"], result["affected_files"])
        self.assertEqual("root cause", result["diagnosis"])

    def test_accepts_preamble_before_fix_and_review_verdicts(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge([
                "analysis complete\nDIAGNOSED\nroot cause",
                "implementation complete\nFIXED\nchanged app",
                "review complete\nPASS\nlooks good",
            ])
            result = build_graph(bridge).invoke(initial(tmp))
        self.assertEqual("PASS", result["status"])
        self.assertEqual("looks good", result["summary"])

    def test_accepts_verdict_after_long_reasoning_preamble(self) -> None:
        reasoning = "\n".join(f"reasoning line {index}" for index in range(30))
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge([
                "DIAGNOSED\nroot cause",
                f"{reasoning}\nFIXED\nchanged app",
                f"{reasoning}\nPASS\nlooks good",
            ])
            result = build_graph(bridge).invoke(initial(tmp))
        self.assertEqual("PASS", result["status"])
        self.assertEqual("looks good", result["summary"])

    def test_does_not_accept_verdict_embedded_in_regular_text(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge(["The task is not DIAGNOSED yet.\nMore investigation is required."])
            state = initial(tmp)
            state["stop_after_investigation"] = True
            result = build_graph(bridge).invoke(state)
        self.assertEqual("BLOCKED", result["status"])
        self.assertIn("not DIAGNOSED", result["summary"])

    def test_can_resume_at_fix_after_code_coordination(self) -> None:
        session_id = "019fdb28-2a22-7ea2-80a5-b20b2464cf28"
        with tempfile.TemporaryDirectory() as tmp:
            bridge = FakeBridge([
                AgentReply(True, "FIXED\nchanged app", session_id=session_id),
                "PASS\nreviewed",
            ])
            state = initial(tmp)
            state.update({
                "skip_investigation": True,
                "status": "DIAGNOSED",
                "diagnosis": "root cause",
                "fix_session_id": session_id,
                "affected_files": ["src/App.java"],
            })
            result = build_graph(bridge).invoke(state)
        self.assertEqual("PASS", result["status"])
        self.assertEqual(["codex", "cursor"], bridge.engines)
        self.assertEqual(session_id, bridge.session_ids[0])

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
