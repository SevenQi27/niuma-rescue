from __future__ import annotations

import json
import subprocess
from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class AgentReply:
    ok: bool
    output: str
    duration: float = 0.0
    artifacts_dir: str = ""


class AgentBridge:
    """Invoke Codex/Cursor through niuma's Go adapter."""

    def __init__(self, runner: str) -> None:
        self.runner = runner

    def run(self, *, engine: str, cwd: str, prompt: str, timeout: int) -> AgentReply:
        request = json.dumps(
            {"engine": engine, "cwd": cwd, "prompt": prompt, "timeout": timeout},
            ensure_ascii=False,
        )
        try:
            completed = subprocess.run(
                [self.runner, "agent-run"],
                input=request,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=timeout * 4 + 120,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            return AgentReply(False, f"agent bridge failed: {exc}")
        try:
            payload: dict[str, Any] = json.loads(completed.stdout)
        except json.JSONDecodeError:
            detail = (completed.stderr or completed.stdout).strip()
            return AgentReply(False, f"invalid agent bridge response: {detail[:1000]}")
        return AgentReply(
            bool(payload.get("ok")),
            str(payload.get("output", "")),
            float(payload.get("duration", 0.0)),
            str(payload.get("artifacts_dir", "")),
        )
