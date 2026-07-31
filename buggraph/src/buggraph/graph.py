from __future__ import annotations

import subprocess
from typing import Protocol, TypedDict

from langgraph.graph import END, START, StateGraph

from .bridge import AgentReply


class BugState(TypedDict, total=False):
    record_id: str
    title: str
    description: str
    clarifications: str
    worktree: str
    base_ref: str
    test_cmd: str
    fix_agent: str
    review_agent: str
    fix_timeout: int
    review_timeout: int
    test_timeout: int
    max_repairs: int
    iteration: int
    diagnosis: str
    fix_output: str
    test_ok: bool
    test_output: str
    review_output: str
    questions: str
    status: str
    summary: str


class Bridge(Protocol):
    def run(self, *, engine: str, cwd: str, prompt: str, timeout: int) -> AgentReply: ...


def _verdict(output: str, allowed: set[str], fallback: str = "BLOCKED") -> str:
    first = output.strip().splitlines()[0].strip().upper() if output.strip() else ""
    return first if first in allowed else fallback


def _body(output: str) -> str:
    lines = output.strip().splitlines()
    return "\n".join(lines[1:]).strip() if len(lines) > 1 else ""


def build_graph(bridge: Bridge, checkpointer=None):
    def investigate(state: BugState) -> BugState:
        prompt = f"""你是 Bug 调查 Agent，只能读取和分析，绝对不要修改文件、commit 或 push。
请在当前 worktree 中复现或定位问题，检查真实代码、日志线索和测试。

Bug: {state['title']}
现象/描述:
{state['description']}

人工补充:
{state.get('clarifications', '') or '无'}

输出协议：第一行必须且只能是 DIAGNOSED、NEEDS_INPUT 或 BLOCKED。
- DIAGNOSED：后续写根因、代码证据、最小修复计划和建议验证命令。
- NEEDS_INPUT：后续列出必须由人补充的信息。
- BLOCKED：后续说明无法继续的环境或权限阻塞。
不要输出开场白。"""
        reply = bridge.run(
            engine=state["fix_agent"], cwd=state["worktree"], prompt=prompt,
            timeout=state["fix_timeout"],
        )
        if not reply.ok:
            return {"status": "BLOCKED", "summary": reply.output, "diagnosis": reply.output}
        verdict = _verdict(reply.output, {"DIAGNOSED", "NEEDS_INPUT", "BLOCKED"})
        body = _body(reply.output)
        if verdict == "DIAGNOSED":
            return {"status": "DIAGNOSED", "diagnosis": body, "summary": body}
        if verdict == "NEEDS_INPUT":
            return {"status": "NEEDS_INPUT", "questions": body, "summary": body}
        return {"status": "BLOCKED", "diagnosis": body, "summary": body or reply.output}

    def fix(state: BugState) -> BugState:
        iteration = int(state.get("iteration", 0)) + 1
        feedback = state.get("review_output", "")
        test_output = state.get("test_output", "")
        prompt = f"""你是 Bug 修复 Agent。请在当前隔离 worktree 内完成最小、可验证的修复。
必须先遵守仓库里的 AGENTS.md；不要碰密钥/.env，不要 commit、push、创建 PR 或合并。

Bug: {state['title']}
原始描述:
{state['description']}

调查结论:
{state.get('diagnosis', '')}

上一轮测试结果:
{test_output or '无（第一轮）'}

上一轮 Reviewer 意见:
{feedback or '无（第一轮）'}

这是第 {iteration}/{state['max_repairs']} 轮修复。请直接检查代码、编辑文件并补必要测试。
输出协议：第一行必须且只能是 FIXED、NEEDS_INPUT 或 BLOCKED；后续简述改动文件和验证情况。
不要 commit / push。"""
        reply = bridge.run(
            engine=state["fix_agent"], cwd=state["worktree"], prompt=prompt,
            timeout=state["fix_timeout"],
        )
        if not reply.ok:
            return {"iteration": iteration, "status": "BLOCKED", "fix_output": reply.output, "summary": reply.output}
        verdict = _verdict(reply.output, {"FIXED", "NEEDS_INPUT", "BLOCKED"})
        body = _body(reply.output)
        if verdict == "NEEDS_INPUT":
            return {"iteration": iteration, "status": "NEEDS_INPUT", "questions": body, "fix_output": body, "summary": body}
        if verdict == "BLOCKED":
            return {"iteration": iteration, "status": "BLOCKED", "fix_output": body, "summary": body}
        return {"iteration": iteration, "status": "FIXED", "fix_output": body, "summary": body}

    def test(state: BugState) -> BugState:
        command = state.get("test_cmd", "").strip()
        if not command:
            return {"test_ok": True, "test_output": "SKIPPED: workspace 未配置 test_cmd", "status": "TESTED"}
        try:
            completed = subprocess.run(
                ["/bin/sh", "-lc", command], cwd=state["worktree"], text=True,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                timeout=int(state.get("test_timeout", state["fix_timeout"])), check=False,
            )
            output = completed.stdout[-12000:]
            return {"test_ok": completed.returncode == 0, "test_output": output, "status": "TESTED"}
        except (OSError, subprocess.TimeoutExpired) as exc:
            return {"test_ok": False, "test_output": f"test command failed: {exc}", "status": "TESTED"}

    def review(state: BugState) -> BugState:
        if not state.get("test_ok", False):
            output = state.get("test_output", "")
            return {
                "status": "BLOCKED",
                "review_output": "",
                "summary": f"测试未通过，禁止进入 Review：{output}",
            }
        prompt = f"""你是独立 Reviewer，只读审查，绝对不要修改文件、commit、push、创建 PR 或合并。
你必须检查当前 worktree 的真实 git diff 和相关代码，不可只信修复 Agent 的总结。

Bug: {state['title']}
调查结论:
{state.get('diagnosis', '')}

修复 Agent 总结:
{state.get('fix_output', '')}

测试是否通过: {state.get('test_ok', False)}
测试输出:
{state.get('test_output', '')[-8000:]}

审查正确性、回归风险、测试覆盖和是否超出范围。
输出协议：第一行必须且只能是 PASS、FAIL、NEEDS_INPUT 或 BLOCKED。
- PASS：后续给出可供人工合并的审查摘要。
- FAIL：后续给出具体、可执行的返修意见。
- NEEDS_INPUT/BLOCKED：说明缺什么或为何无法审查。
不要输出开场白。"""
        reply = bridge.run(
            engine=state["review_agent"], cwd=state["worktree"], prompt=prompt,
            timeout=state["review_timeout"],
        )
        if not reply.ok:
            return {"status": "BLOCKED", "review_output": reply.output, "summary": reply.output}
        verdict = _verdict(reply.output, {"PASS", "FAIL", "NEEDS_INPUT", "BLOCKED"})
        body = _body(reply.output)
        if verdict == "PASS":
            return {"status": "PASS", "review_output": body, "summary": body}
        if verdict == "FAIL":
            return {"status": "FAIL", "review_output": body, "summary": body}
        if verdict == "NEEDS_INPUT":
            return {"status": "NEEDS_INPUT", "questions": body, "review_output": body, "summary": body}
        return {"status": "BLOCKED", "review_output": body, "summary": body or reply.output}

    def route_investigation(state: BugState) -> str:
        return "fix" if state.get("status") == "DIAGNOSED" else "end"

    def route_fix(state: BugState) -> str:
        return "test" if state.get("status") == "FIXED" else "end"

    def route_test(state: BugState) -> str:
        if state.get("test_ok", False):
            return "review"
        if int(state.get("iteration", 0)) < int(state.get("max_repairs", 2)):
            return "fix"
        return "exhausted"

    def route_review(state: BugState) -> str:
        if state.get("status") == "PASS":
            return "end"
        if state.get("status") == "FAIL" and int(state.get("iteration", 0)) < int(state.get("max_repairs", 2)):
            return "fix"
        return "exhausted" if state.get("status") == "FAIL" else "end"

    def exhausted(state: BugState) -> BugState:
        if not state.get("test_ok", False):
            reason = f"最后测试失败：{state.get('test_output', '')}"
        else:
            reason = f"最后 Review：{state.get('review_output', '')}"
        return {
            "status": "BLOCKED",
            "summary": f"达到最大返修轮次 {state.get('max_repairs', 2)}；{reason}",
        }

    graph = StateGraph(BugState)
    graph.add_node("investigate", investigate)
    graph.add_node("fix", fix)
    graph.add_node("test", test)
    graph.add_node("review", review)
    graph.add_node("exhausted", exhausted)
    graph.add_edge(START, "investigate")
    graph.add_conditional_edges("investigate", route_investigation, {"fix": "fix", "end": END})
    graph.add_conditional_edges("fix", route_fix, {"test": "test", "end": END})
    graph.add_conditional_edges("test", route_test, {"review": "review", "fix": "fix", "exhausted": "exhausted"})
    graph.add_conditional_edges("review", route_review, {"fix": "fix", "exhausted": "exhausted", "end": END})
    graph.add_edge("exhausted", END)
    return graph.compile(checkpointer=checkpointer)
