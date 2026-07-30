from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from buggraph.bridge import AgentReply
from buggraph.cli import run


class ProtocolBridge:
    def __init__(self, runner: str) -> None:
        self.runner = runner

    def run(self, *, engine: str, cwd: str, prompt: str, timeout: int) -> AgentReply:
        if "Bug 调查 Agent" in prompt:
            return AgentReply(True, "DIAGNOSED\nroot cause")
        if "Bug 修复 Agent" in prompt:
            return AgentReply(True, "FIXED\nchanged app.py")
        return AgentReply(True, "PASS\nreviewed")


class CliTest(unittest.TestCase):
    def test_sqlite_checkpoint_cli_run(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            payload = {
                "record_id": "rec-sqlite", "thread_id": "bug:rec-sqlite",
                "title": "broken", "description": "fails", "clarifications": "",
                "worktree": tmp, "base_ref": "main", "test_cmd": "",
                "fix_agent": "codex", "review_agent": "cursor", "agent_runner": "/fake/niuma",
                "fix_timeout": 10, "review_timeout": 10, "test_timeout": 10,
                "max_repairs": 2, "checkpoint_db": str(Path(tmp) / "checkpoints.sqlite3"),
            }
            with patch("buggraph.cli.AgentBridge", ProtocolBridge):
                result = run(payload)
            self.assertEqual("PASS", result["status"])
            self.assertTrue((Path(tmp) / "checkpoints.sqlite3").exists())


if __name__ == "__main__":
    unittest.main()
