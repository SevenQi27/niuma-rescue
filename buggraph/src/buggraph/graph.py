from __future__ import annotations

import subprocess
from typing import Any, Callable, Protocol, TypedDict

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
    fix_session_id: str
    affected_files: list[str]
    skip_investigation: bool
    stop_after_investigation: bool
    test_ok: bool
    test_output: str
    review_output: str
    questions: str
    status: str
    summary: str


class Bridge(Protocol):
    def run(
        self, *, engine: str, cwd: str, prompt: str, timeout: int,
        session_id: str = "", write_access: bool = False,
    ) -> AgentReply: ...


_VERDICT_TOKENS = frozenset({
    "DIAGNOSED", "NEEDS_INPUT", "BLOCKED", "FIXED", "PASS", "FAIL",
})


def _find_verdict_line(output: str, allowed: set[str] | frozenset[str]) -> tuple[str, int]:
    lines = output.strip().splitlines()
    found = ("", -1)
    for index, line in enumerate(lines):
        token = line.strip().upper()
        if token in allowed:
            found = (token, index)
    return found


def _verdict(output: str, allowed: set[str], fallback: str = "BLOCKED") -> str:
    verdict, _ = _find_verdict_line(output, allowed)
    return verdict or fallback


def _body(output: str) -> str:
    lines = output.strip().splitlines()
    _, index = _find_verdict_line(output, _VERDICT_TOKENS)
    if index >= 0:
        return "\n".join(lines[index + 1:]).strip()
    return "\n".join(lines).strip()


def _investigation_body(output: str) -> tuple[str, list[str]]:
    all_lines = output.strip().splitlines()
    _, verdict_index = _find_verdict_line(output, {"DIAGNOSED", "NEEDS_INPUT", "BLOCKED"})
    lines = all_lines[verdict_index + 1:] if verdict_index >= 0 else all_lines
    files: list[str] = []
    diagnosis: list[str] = []
    in_files = False
    for line in lines:
        stripped = line.strip()
        marker = stripped.upper().rstrip(":：")
        if marker in {"FILES", "AFFECTED_FILES"}:
            in_files = True
            continue
        if marker in {"SUMMARY", "DIAGNOSIS", "ROOT_CAUSE"}:
            in_files = False
            continue
        if in_files:
            candidate = stripped.lstrip("-*` ").rstrip("`").strip()
            if candidate and not candidate.startswith("/") and "../" not in candidate:
                files.append(candidate.replace("\\", "/"))
        elif stripped:
            diagnosis.append(line)
    unique_files = list(dict.fromkeys(files))
    body = "\n".join(diagnosis).strip()
    if not body:
        body = _body(output)
    return body, unique_files


def _baseline_policy(state: BugState) -> str:
    base_ref = state.get("base_ref", "").strip() or "任务创建基线"
    return f"""基线约束（优先级高于调查结论和 Reviewer 意见，必须遵守）：
- 当前 worktree 已由 Niuma 从 `{base_ref}` 创建；只允许在这个 worktree 和现有任务分支上工作。
- 其他分支（包括 test、develop、release）只能只读参考；禁止建议或执行 checkout、switch、rebase、merge、cherry-pick、reset，也不得要求从其他分支重建 worktree。
- 不得因为其他分支存在相邻或更新实现，就把当前任务未包含该分支作为阻塞或 Review 失败理由；只审查相对 `{base_ref}` 的当前任务改动。
- 分支集成、冲突处理和合并全部由人工负责，不属于 Agent 职责。
- 如果调查结论或 Reviewer 意见与本约束冲突，忽略其中换基线或合并分支的部分，继续在当前 worktree 完成任务。"""


def build_graph(
    bridge: Bridge,
    checkpointer=None,
    on_event: Callable[[dict[str, Any]], None] | None = None,
):
    def emit(stage: str, state: str, *, agent: str = "", detail: str = "", iteration: int = 0) -> None:
        if on_event is None:
            return
        try:
            on_event({
                "stage": stage,
                "state": state,
                "agent": agent,
                "detail": detail[-12000:],
                "iteration": iteration,
            })
        except Exception:
            pass

    def investigate(state: BugState) -> BugState:
        emit("investigate", "running", agent=state["fix_agent"], detail="正在读取代码并定位根因")
        prompt = f"""你是 Bug 调查 Agent，只能读取和分析，绝对不要修改文件、commit 或 push。
请在当前 worktree 中复现或定位问题，检查真实代码、日志线索和测试。

{_baseline_policy(state)}

Bug: {state['title']}
现象/描述:
{state['description']}

人工补充:
{state.get('clarifications', '') or '无'}

输出协议：第一行必须且只能是 DIAGNOSED、NEEDS_INPUT 或 BLOCKED。
- DIAGNOSED：随后必须先输出 `FILES:`，逐行列出预计会修改的仓库相对路径；再输出 `SUMMARY:`，写根因、代码证据、最小修复计划和建议验证命令。无法判断路径时在 FILES 下写 `*`。
- NEEDS_INPUT：后续列出必须由人补充的信息。
- BLOCKED：后续说明无法继续的环境或权限阻塞。
不要输出开场白。"""
        reply = bridge.run(
            engine=state["fix_agent"], cwd=state["worktree"], prompt=prompt,
            timeout=state["fix_timeout"], session_id="", write_access=False,
        )
        if not reply.ok:
            emit("investigate", "failed", agent=state["fix_agent"], detail=reply.output)
            return {
                "status": "BLOCKED", "summary": reply.output, "diagnosis": reply.output,
                "fix_session_id": reply.session_id,
            }
        verdict = _verdict(reply.output, {"DIAGNOSED", "NEEDS_INPUT", "BLOCKED"})
        body, affected_files = _investigation_body(reply.output)
        if verdict == "DIAGNOSED":
            emit("investigate", "done", agent=state["fix_agent"], detail=body)
            return {
                "status": "DIAGNOSED", "diagnosis": body, "summary": body,
                "fix_session_id": reply.session_id, "affected_files": affected_files,
            }
        if verdict == "NEEDS_INPUT":
            emit("investigate", "waiting", agent=state["fix_agent"], detail=body)
            return {
                "status": "NEEDS_INPUT", "questions": body, "summary": body,
                "fix_session_id": reply.session_id,
            }
        emit("investigate", "failed", agent=state["fix_agent"], detail=body or reply.output)
        return {
            "status": "BLOCKED", "diagnosis": body, "summary": body or reply.output,
            "fix_session_id": reply.session_id,
        }

    def fix(state: BugState) -> BugState:
        iteration = int(state.get("iteration", 0)) + 1
        emit("fix", "running", agent=state["fix_agent"], detail="正在实施最小修复", iteration=iteration)
        feedback = state.get("review_output", "")
        test_output = state.get("test_output", "")
        session_id = state.get("fix_session_id", "").strip()
        diagnosis_context = (
            "继续刚才的调查会话，直接基于你已经得到的调查结论实施修复。"
            if session_id else f"调查结论:\n{state.get('diagnosis', '')}"
        )
        prompt = f"""你是 Bug 修复 Agent。请在当前隔离 worktree 内完成最小、可验证的修复。
必须先遵守仓库里的 AGENTS.md；不要碰密钥/.env，不要 commit、push、创建 PR 或合并。

{_baseline_policy(state)}

Bug: {state['title']}
原始描述:
{state['description']}

{diagnosis_context}

上一轮测试结果:
{test_output or '无（第一轮）'}

上一轮 Reviewer 意见:
{feedback or '无（第一轮）'}

这是第 {iteration}/{state['max_repairs']} 轮修复。请直接检查代码、编辑文件并补必要测试。
输出协议：第一行必须且只能是 FIXED、NEEDS_INPUT 或 BLOCKED；后续简述改动文件和验证情况。
不要 commit / push。"""
        reply = bridge.run(
            engine=state["fix_agent"], cwd=state["worktree"], prompt=prompt,
            timeout=state["fix_timeout"], session_id=session_id, write_access=True,
        )
        next_session_id = reply.session_id or session_id
        if not reply.ok:
            emit("fix", "failed", agent=state["fix_agent"], detail=reply.output, iteration=iteration)
            return {
                "iteration": iteration, "status": "BLOCKED", "fix_output": reply.output,
                "summary": reply.output, "fix_session_id": next_session_id,
            }
        verdict = _verdict(reply.output, {"FIXED", "NEEDS_INPUT", "BLOCKED"})
        body = _body(reply.output)
        if verdict == "NEEDS_INPUT":
            emit("fix", "waiting", agent=state["fix_agent"], detail=body, iteration=iteration)
            return {
                "iteration": iteration, "status": "NEEDS_INPUT", "questions": body,
                "fix_output": body, "summary": body, "fix_session_id": next_session_id,
            }
        if verdict == "BLOCKED":
            emit("fix", "failed", agent=state["fix_agent"], detail=body, iteration=iteration)
            return {
                "iteration": iteration, "status": "BLOCKED", "fix_output": body,
                "summary": body, "fix_session_id": next_session_id,
            }
        emit("fix", "done", agent=state["fix_agent"], detail=body, iteration=iteration)
        return {
            "iteration": iteration, "status": "FIXED", "fix_output": body,
            "summary": body, "fix_session_id": next_session_id,
        }

    def test(state: BugState) -> BugState:
        iteration = int(state.get("iteration", 0))
        emit("test", "running", agent="Niuma", detail="正在执行工作区验证命令", iteration=iteration)
        command = state.get("test_cmd", "").strip()
        if not command:
            emit("test", "done", agent="Niuma", detail="SKIPPED: workspace 未配置 test_cmd", iteration=iteration)
            return {"test_ok": True, "test_output": "SKIPPED: workspace 未配置 test_cmd", "status": "TESTED"}
        try:
            completed = subprocess.run(
                ["/bin/sh", "-lc", command], cwd=state["worktree"], text=True,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                timeout=int(state.get("test_timeout", state["fix_timeout"])), check=False,
            )
            output = completed.stdout[-12000:]
            emit(
                "test", "done" if completed.returncode == 0 else "failed",
                agent="Niuma", detail=output, iteration=iteration,
            )
            return {"test_ok": completed.returncode == 0, "test_output": output, "status": "TESTED"}
        except (OSError, subprocess.TimeoutExpired) as exc:
            output = f"test command failed: {exc}"
            emit("test", "failed", agent="Niuma", detail=output, iteration=iteration)
            return {"test_ok": False, "test_output": output, "status": "TESTED"}

    def review(state: BugState) -> BugState:
        iteration = int(state.get("iteration", 0))
        if not state.get("test_ok", False):
            output = state.get("test_output", "")
            emit("review", "failed", agent=state["review_agent"], detail=f"测试未通过，未进入 Review：{output}", iteration=iteration)
            return {
                "status": "BLOCKED",
                "review_output": "",
                "summary": f"测试未通过，禁止进入 Review：{output}",
            }
        emit("review", "running", agent=state["review_agent"], detail="正在独立检查真实 diff 和回归风险", iteration=iteration)
        prompt = f"""你是独立 Reviewer，只读审查，绝对不要修改文件、commit、push、创建 PR 或合并。
你必须检查当前 worktree 的真实 git diff 和相关代码，不可只信修复 Agent 的总结。

{_baseline_policy(state)}

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
            timeout=state["review_timeout"], session_id="", write_access=False,
        )
        if not reply.ok:
            emit("review", "failed", agent=state["review_agent"], detail=reply.output, iteration=iteration)
            return {"status": "BLOCKED", "review_output": reply.output, "summary": reply.output}
        verdict = _verdict(reply.output, {"PASS", "FAIL", "NEEDS_INPUT", "BLOCKED"})
        body = _body(reply.output)
        if verdict == "PASS":
            emit("review", "done", agent=state["review_agent"], detail=body, iteration=iteration)
            return {"status": "PASS", "review_output": body, "summary": body}
        if verdict == "FAIL":
            emit("review", "failed", agent=state["review_agent"], detail=body, iteration=iteration)
            return {"status": "FAIL", "review_output": body, "summary": body}
        if verdict == "NEEDS_INPUT":
            emit("review", "waiting", agent=state["review_agent"], detail=body, iteration=iteration)
            return {"status": "NEEDS_INPUT", "questions": body, "review_output": body, "summary": body}
        emit("review", "failed", agent=state["review_agent"], detail=body or reply.output, iteration=iteration)
        return {"status": "BLOCKED", "review_output": body, "summary": body or reply.output}

    def route_investigation(state: BugState) -> str:
        if state.get("stop_after_investigation", False):
            return "end"
        return "fix" if state.get("status") == "DIAGNOSED" else "end"

    def route_start(state: BugState) -> str:
        return "fix" if state.get("skip_investigation", False) else "investigate"

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
        summary = f"达到最大返修轮次 {state.get('max_repairs', 2)}；{reason}"
        emit("pipeline", "failed", agent="Niuma", detail=summary, iteration=int(state.get("iteration", 0)))
        return {
            "status": "BLOCKED",
            "summary": summary,
        }

    graph = StateGraph(BugState)
    graph.add_node("investigate", investigate)
    graph.add_node("fix", fix)
    graph.add_node("test", test)
    graph.add_node("review", review)
    graph.add_node("exhausted", exhausted)
    graph.add_conditional_edges(START, route_start, {"investigate": "investigate", "fix": "fix"})
    graph.add_conditional_edges("investigate", route_investigation, {"fix": "fix", "end": END})
    graph.add_conditional_edges("fix", route_fix, {"test": "test", "end": END})
    graph.add_conditional_edges("test", route_test, {"review": "review", "fix": "fix", "exhausted": "exhausted"})
    graph.add_conditional_edges("review", route_review, {"fix": "fix", "exhausted": "exhausted", "end": END})
    graph.add_edge("exhausted", END)
    return graph.compile(checkpointer=checkpointer)
