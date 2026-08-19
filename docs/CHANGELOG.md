# Changelog

## Unreleased

### Development coordination

- Added shared development sessions: create an untracked session branch from the configured baseline, run task worktrees in parallel, serialize reviewed Git integration and leave the final delivery target to the user.
- Added daily and manual session rollover, session freeze guards, task sequence/status visibility and management APIs.
- Added workspace-level queue mode, delivery target, upstream tracking policy, JDK home and Maven home.
- Added Claude/Codex session continuation for repair loops and workspace-specific toolchain injection for Agent and test processes.

### Bug quality and conflict control

- Added Claude as a Bug fix/review Agent; the fix and review roles must use different Agents.
- Split Bug execution into investigation and write phases. Investigation now records diagnosis and affected files before code modification.
- Added similar-Bug hints, file-level claims, a “waiting for code area” state and predecessor branch ordering for overlapping Bugs.
- Added safe hand-off from a reviewed predecessor branch and conservative workspace-level locking when investigation cannot locate exact files.
- Added shared-session support for Bug tasks: each Bug still edits an isolated task worktree, then enters the session branch through serialized integration.
- Added conflict isolation: a failed task rebase is aborted and only that task becomes `integration_conflict`; the session worktree remains clean.
- Expanded attachment selection so files can be accumulated and removed before upload; each file remains limited to 8 MB and validated by type.

### Local-first control plane

- Made local SQLite the task source of truth; Web and Agent workflows run without Feishu.
- Added the Bug/requirement task center, attachments, live pipeline output, filtering, editing and archive operations.
- Added the management console for task recovery, pipeline defaults, workspaces, development sessions and optional integrations.
- Made Feishu an optional runtime connector with enable/disable, connection testing, immediate sync and masked secrets.
- Added ZenTao/Jira validated and idempotent Webhook ingestion plus Slack connection configuration.

### Documentation

- Reworked README around the current local-first Go architecture and added sanitized task-center and management-console screenshots.
- Replaced obsolete Python listener/dispatcher setup, operations, release and Agent CLI instructions.
- Documented shared sessions, Bug file coordination, human delivery ownership, test-command limitations and current connector boundaries.

### Known boundaries

- The Web console has no authentication and must remain on a trusted LAN.
- An empty workspace `test_cmd` is recorded as skipped; it is not executable acceptance evidence.
- ZenTao/Jira events are validated and queued but are not yet converted automatically into full tasks or written back to the source system.
- Slack Socket Mode consumption is not implemented.
- Final merge into `main`, `test` or another product branch is always a human decision.

## v0.1.0

- Initial Feishu Base requirement pool and message intake.
- Added Cursor, Claude, Codex and Gemini CLI execution.
- Added requirement clarification, development, review and PR adapter skeletons.
- Added the first Go rewrite and the Python LangGraph Bug repair sidecar.
