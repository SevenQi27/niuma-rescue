(() => {
  const state = {
    meta: null,
    bugs: [],
    activeType: localStorage.getItem("niuma-task-tab") === "需求" ? "需求" : "Bug",
    filters: {
      Bug: { query: "", status: "", workspace: "" },
      需求: { query: "", status: "", workspace: "" },
    },
    detailTaskID: "",
    expandedPipelines: new Set(),
    pipelines: {},
    pipelineLoading: new Set(),
    pipelineErrors: {},
    bugDetailsOpen: new Map(),
    pipelineOutputOpen: new Map(),
    scrollPositions: new Map(),
    admin: null,
    workspaceEntries: [],
    selectedConnector: "feishu",
    createAttachments: [],
    editAttachments: [],
  };
  const byId = (id) => document.getElementById(id);
  const bugForm = byId("bugForm");
  const createError = byId("createError");
  const bugList = byId("bugList");
  const emptyState = byId("emptyState");
  const createTaskDialog = byId("createTaskDialog");
  const editDialog = byId("editDialog");
  const editForm = byId("editForm");
  const editError = byId("editError");
  const toast = byId("toast");
  const manageDialog = byId("manageDialog");
  const integrationForm = byId("integrationForm");
  const thirdPartyIntegrationForm = byId("thirdPartyIntegrationForm");
  const pipelineSettingsForm = byId("pipelineSettingsForm");

  const connectorDefinitions = {
    feishu: { color: "#3376f6", icon: "飞", label: "飞书", eyebrow: "TASK + MESSAGE CONNECTOR", description: "多维表格同步、消息卡片和长连接接入。" },
    zentao: { color: "#18a875", icon: "禅", label: "禅道", eyebrow: "WORK ITEM CONNECTOR", description: "接收 Bug / 需求 Webhook，并映射到本地工作区。" },
    jira: { color: "#1769e0", icon: "J", label: "Jira", eyebrow: "WORK ITEM CONNECTOR", description: "接收 Jira Issue，支持 Cloud 与 Data Center 配置。" },
    slack: { color: "#5b285f", icon: "S", label: "Slack", eyebrow: "MESSAGE CONNECTOR", description: "团队提交入口与进度通知，支持 Socket / Events API 配置。" },
  };

  const esc = (value = "") => String(value).replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" })[char]);

  async function api(path, options = {}) {
    const headers = { ...(options.headers || {}) };
    if (options.body && !(options.body instanceof FormData)) headers["Content-Type"] = "application/json";
    const response = await fetch(path, {
      ...options,
      headers,
    });
    let data = {};
    try { data = await response.json(); } catch (_) { /* empty response */ }
    if (!response.ok) throw new Error(data.error || `请求失败（${response.status}）`);
    return data;
  }

  async function bootstrap() {
    state.meta = await api("/api/meta");
    fillWorkspaceSelects();
    updateDataMode();
    const reporter = localStorage.getItem("niuma-reporter") || "";
    byId("bugReporter").value = reporter;
    applyTaskTab(state.activeType, false);
    await refreshBugs();
    if (window.location.pathname === "/manage") await openManagement();
  }

  function updateDataMode() {
    const integration = state.meta.integration || {};
    byId("dataMode").innerHTML = `<i></i> ${integration.enabled ? "本地 + 飞书" : "本地数据源"}`;
    byId("syncFooter").textContent = integration.enabled ? "本地 SQLite 为主，飞书作为可选同步渠道" : "任务数据保存在本机 SQLite，飞书未启用";
  }

  function fillWorkspaceSelects() {
    const options = state.meta.workspaces.map((workspace) => `<option value="${esc(workspace)}">${esc(workspace)}</option>`).join("");
    byId("bugWorkspace").innerHTML = `<option value="">请选择代码仓库…</option>${options}`;
    byId("editWorkspace").innerHTML = options;
    byId("editRequirementWorkspace").innerHTML = options;
    byId("workspaceFilter").innerHTML = `<option value="">全部工作区</option>${options}`;
  }

  async function refreshBugs() {
    byId("refreshButton").disabled = true;
    try {
      const data = await api("/api/tasks");
      state.bugs = data.tasks || [];
      render();
    } catch (error) {
      showToast(error.message, true);
    } finally {
      byId("refreshButton").disabled = false;
    }
  }

  async function refreshPipeline(id, quiet = false) {
    if (state.pipelineLoading.has(id)) return;
    state.pipelineLoading.add(id);
    let shouldRender = !quiet;
    if (!quiet) render();
    try {
      const data = await api(`/api/tasks/${encodeURIComponent(id)}/pipeline`);
      if (JSON.stringify(state.pipelines[id] || null) !== JSON.stringify(data.pipeline || null)) {
        state.pipelines[id] = data.pipeline;
        shouldRender = true;
      }
      if (state.pipelineErrors[id]) shouldRender = true;
      delete state.pipelineErrors[id];
    } catch (error) {
      if (state.pipelineErrors[id] !== error.message) shouldRender = true;
      state.pipelineErrors[id] = error.message;
      if (!quiet) showToast(error.message, true);
    } finally {
      state.pipelineLoading.delete(id);
      if (state.expandedPipelines.has(id) && shouldRender) render();
    }
  }

  function statusTone(status) {
    if (status === "已阻塞") return "status-blocked";
    if (["Bug处理中", "待澄清", "开发中", "Review中"].includes(status)) return "status-active";
    if (["待选择", "待回答", "待确认", "待开发", "等待代码区域"].includes(status)) return "status-waiting";
    if (status === "待合并") return "status-merge";
    if (status === "完成") return "status-done";
    return "";
  }

  function render() {
    const viewport = { x: window.scrollX, y: window.scrollY };
    bugList.querySelectorAll("details[data-bug-details-key]").forEach((details) => {
      state.bugDetailsOpen.set(details.dataset.bugDetailsKey, details.open);
    });
    bugList.querySelectorAll("details[data-output-key]").forEach((details) => {
      state.pipelineOutputOpen.set(details.dataset.outputKey, details.open);
    });
    bugList.querySelectorAll("[data-scroll-key]").forEach((element) => {
      state.scrollPositions.set(element.dataset.scrollKey, { top: element.scrollTop, left: element.scrollLeft });
    });
    const query = byId("searchInput").value.trim().toLowerCase();
    const status = byId("statusFilter").value;
    const workspace = byId("workspaceFilter").value;
    const visible = state.bugs.filter((bug) => {
      const matchesText = !query || `${bug.title} ${bug.description}`.toLowerCase().includes(query);
      return bug.task_type === state.activeType && matchesText && (!status || bug.status === status) && (!workspace || bug.workspace === workspace);
    });
    const detailTask = state.bugs.find((bug) => bug.id === state.detailTaskID);
    if (state.detailTaskID && !detailTask) state.detailTaskID = "";
    updateTaskTabCounts();
    renderSummary();
    document.body.classList.toggle("task-detail-open", Boolean(detailTask));
    bugList.innerHTML = `${visible.length ? `
      <div class="task-table" role="table" aria-label="任务管理列表">
        <div class="task-table-head" role="row">
          <span>任务</span><span>状态</span><span>工作区</span><span>执行 Agent</span><span>数据</span><span></span>
        </div>
        <div class="task-table-body">${visible.map(renderTaskTableRow).join("")}</div>
      </div>` : ""}${detailTask ? renderTaskDetailLayer(detailTask) : ""}`;
    bugList.querySelectorAll("[data-scroll-key]").forEach((element) => {
      const position = state.scrollPositions.get(element.dataset.scrollKey);
      if (!position) return;
      element.scrollTop = position.top;
      element.scrollLeft = position.left;
    });
    window.scrollTo(viewport.x, viewport.y);
    emptyState.hidden = visible.length !== 0;
  }

  function renderTaskTableRow(task) {
    const tone = statusTone(task.status);
    const primaryAgent = task.task_type === "需求" ? task.code_agent : task.fix_agent;
    const syncLabel = task.sync_state === "synced" ? "飞书已同步" : task.sync_state === "error" ? "同步失败" : task.sync_state === "pending" ? "待同步" : "仅本地";
    const syncTone = task.sync_state === "synced" ? "is-synced" : task.sync_state === "error" ? "is-error" : task.sync_state === "pending" ? "is-pending" : "";
    const coordination = task.coordination || {};
    const relation = coordination.predecessor_id
      ? `<small class="task-relation">合并顺序：${esc(coordination.predecessor_id)} → 当前任务</small>`
      : coordination.similar_task_id ? `<small class="task-relation">与 ${esc(coordination.similar_task_id)} 相似 ${Math.round((coordination.similarity || 0) * 100)}%</small>` : "";
    return `<button class="task-table-row" type="button" role="row" data-action="view-task" data-id="${esc(task.id)}">
      <span class="task-table-main" role="cell"><strong>${esc(task.title)}</strong><small>${esc(task.description)}</small>${relation}</span>
      <span class="task-table-status" role="cell"><em class="status-badge ${tone}">${esc(task.status)}</em></span>
      <span class="task-table-cell" role="cell">${esc(task.workspace || "未设置")}</span>
      <span class="task-table-cell task-table-agents" role="cell">${esc(primaryAgent || "未指定")} <i>→</i> ${esc(task.review_agent || "未指定")}</span>
      <span class="task-table-cell" role="cell"><em class="sync-chip ${syncTone}">${syncLabel}</em></span>
      <span class="task-table-arrow" role="cell">›</span>
    </button>`;
  }

  function renderTaskDetailLayer(task) {
    const tone = statusTone(task.status);
    return `<div class="task-detail-layer" role="presentation">
      <button class="task-detail-backdrop" type="button" data-action="close-task-detail" aria-label="关闭任务详情"></button>
      <section class="task-detail-modal" role="dialog" aria-modal="true" aria-labelledby="taskDetailHeading">
        <div class="task-detail-modal-heading"><div><p class="eyebrow">${task.task_type === "需求" ? "REQUIREMENT DETAIL" : "BUG DETAIL"}</p><h2 id="taskDetailHeading">${esc(task.title)}</h2></div><div class="task-detail-heading-actions"><span class="status-badge ${tone}">${esc(task.status)}</span><button class="close-button" type="button" data-action="close-task-detail" aria-label="关闭">×</button></div></div>
        <div class="task-detail-scroll" data-scroll-key="task-detail:${esc(task.id)}">${renderBug(task)}</div>
      </section>
    </div>`;
  }

  function renderSummary() {
    const current = state.bugs.filter((bug) => bug.task_type === state.activeType);
    const activeStatuses = state.activeType === "Bug" ? ["Bug处理中", "Review中"] : ["待澄清", "开发中", "Review中"];
    const waitingStatuses = state.activeType === "Bug" ? ["待选择", "待回答", "等待代码区域", "已阻塞"] : ["待选择", "待回答", "待确认", "待开发", "已阻塞"];
    const active = current.filter((bug) => activeStatuses.includes(bug.status)).length;
    const waiting = current.filter((bug) => waitingStatuses.includes(bug.status)).length;
    const merge = current.filter((bug) => bug.status === "待合并").length;
    const done = current.filter((bug) => bug.status === "完成").length;
    byId("summary").innerHTML = `
      <div class="summary-stat is-active"><span>处理中</span><b>${active}</b><small>Agent 正在执行</small></div>
      <div class="summary-stat is-waiting"><span>待我处理</span><b>${waiting}</b><small>需要人工补充或确认</small></div>
      <div class="summary-stat is-merge"><span>待合并</span><b>${merge}</b><small>等待代码交付</small></div>
      <div class="summary-stat is-done"><span>已完成</span><b>${done}</b><small>本页签累计完成</small></div>`;
  }

  function updateTaskTabCounts() {
    byId("bugTabCount").textContent = state.bugs.filter((task) => task.task_type === "Bug").length;
    byId("requirementTabCount").textContent = state.bugs.filter((task) => task.task_type === "需求").length;
  }

  function updateStatusFilter() {
    const current = byId("statusFilter").value;
    const statuses = state.activeType === "Bug"
      ? ["待选择", "待回答", "Bug处理中", "等待代码区域", "Review中", "待合并", "已阻塞", "完成"]
      : ["待选择", "待澄清", "待回答", "待确认", "待开发", "开发中", "Review中", "待合并", "已阻塞", "完成"];
    byId("statusFilter").innerHTML = `<option value="">全部状态</option>${statuses.map((status) => `<option value="${status}">${status}</option>`).join("")}`;
    byId("statusFilter").value = statuses.includes(current) ? current : "";
  }

  function applyTaskTab(taskType, shouldRender = true) {
    state.activeType = taskType === "需求" ? "需求" : "Bug";
    localStorage.setItem("niuma-task-tab", state.activeType);
    document.body.classList.toggle("is-requirement", state.activeType === "需求");
    document.querySelectorAll("[data-task-tab]").forEach((tab) => {
      const active = tab.dataset.taskTab === state.activeType;
      tab.classList.toggle("is-active", active);
      tab.setAttribute("aria-selected", String(active));
    });
    const requirement = state.activeType === "需求";
    byId("heroEyebrow").textContent = requirement ? "REQUIREMENT WORKFLOW" : "BUG WORKFLOW";
    byId("heroTitle").textContent = requirement ? "需求任务" : "Bug 任务";
    byId("heroLead").textContent = requirement
      ? "管理和跟踪 AI 澄清、开发、Review 与人工交付进度。"
      : "管理和跟踪 AI 调查、修复、Review 与人工合并进度。";
    byId("formTitle").textContent = requirement ? "提交一个需求" : "提交一个 Bug";
    byId("formDescription").textContent = requirement ? "先保存到需求池，准备好后再让 AI 开始澄清。" : "先保存还能继续修改，确认清楚后再开始修复。";
    byId("bugTitle").placeholder = requirement ? "例如：新增销售日报的按项目导出功能" : "例如：大金额折扣计算结果为负数";
    byId("descriptionLabel").textContent = requirement ? "目标、使用场景、范围和验收预期" : "现象、复现步骤和期望结果";
    byId("bugDescription").placeholder = requirement ? "想解决什么问题？谁会使用？哪些内容要做、哪些不做？怎样算完成？" : "发生了什么？怎样复现？你期望看到什么？如有报错信息也请一起贴上。";
    byId("bugAgentFields").hidden = requirement;
    byId("requirementAgentFields").hidden = !requirement;
    byId("startSubmitButton").textContent = requirement ? "保存并开始澄清" : "保存并开始修复";
    byId("openCreateTaskButton").textContent = requirement ? "+ 提交需求" : "+ 提交 Bug";
    byId("boardEyebrow").textContent = "TASKS";
    byId("boardTitle").textContent = "任务列表";
    byId("emptyIcon").textContent = requirement ? "✨" : "☕";
    byId("emptyTitle").textContent = requirement ? "这里还没有匹配的需求" : "这里还没有匹配的 Bug";
    byId("emptyDescription").textContent = requirement ? "把目标和边界写下来，让 Agent 先帮你澄清。" : "写清楚问题，剩下的交给流水线。";
    byId("searchInput").value = state.filters[state.activeType].query;
    updateStatusFilter();
    byId("statusFilter").value = state.filters[state.activeType].status;
    byId("workspaceFilter").value = state.filters[state.activeType].workspace;
    if (shouldRender) render();
  }

  function renderBug(bug) {
    const tone = statusTone(bug.status);
    const requirement = bug.task_type === "需求";
    const editButton = bug.editable ? `<button class="button secondary" data-action="edit" data-id="${esc(bug.id)}">修改</button>` : "";
    const startLabel = bug.status === "待回答" ? "补充后继续" : bug.status === "已阻塞" ? "重新开始" : requirement ? "开始澄清" : "开始修复";
    const startButton = bug.startable && bug.status !== "已阻塞" ? `<button class="button primary" data-action="start" data-id="${esc(bug.id)}">${startLabel}</button>` : "";
    const clarification = bug.clarification ? `<div class="bug-meta"><span>补充：${esc(bug.clarification).slice(0, 240)}</span></div>` : "";
    const prd = requirement && bug.prd ? `<details class="bug-details" data-bug-details-key="prd:${esc(bug.id)}" ${state.bugDetailsOpen.get(`prd:${bug.id}`) === true ? "open" : ""}><summary>查看 PRD / 验收标准</summary><pre data-scroll-key="prd:${esc(bug.id)}">${esc(bug.prd)}</pre></details>` : "";
    const link = bug.link ? (/^https?:\/\//i.test(bug.link) ? `<a href="${esc(bug.link)}" target="_blank" rel="noreferrer">查看 PR / MR</a>` : `<code>${esc(bug.link)}</code>`) : "";
    const detailsOpen = state.bugDetailsOpen.get(bug.id) === true;
    const details = bug.log || bug.clarification ? `<details class="bug-details" data-bug-details-key="${esc(bug.id)}" ${detailsOpen ? "open" : ""}><summary>查看处理记录</summary>${bug.clarification ? `<p>${esc(bug.clarification)}</p>` : ""}${bug.log ? `<pre data-scroll-key="bug-log:${esc(bug.id)}">${esc(bug.log)}</pre>` : ""}</details>` : "";
    const attachments = (bug.attachments || bug.images || []).map((attachment) => {
      const name = attachment.name || "任务附件";
      const mime = (attachment.mime || "").toLowerCase();
      if (mime.startsWith("image/")) {
        return `<a class="bug-attachment" href="${esc(attachment.url)}" target="_blank" rel="noreferrer"><img src="${esc(attachment.url)}" alt="${esc(name)}" loading="lazy"><span class="bug-attachment-name">${esc(name)}</span></a>`;
      }
      const lowerName = name.toLowerCase();
      const typeLabel = mime === "application/pdf" || lowerName.endsWith(".pdf") ? "PDF" : lowerName.endsWith(".csv") ? "CSV" : "EXCEL";
      return `<a class="bug-attachment is-file" href="${esc(attachment.url)}" target="_blank" rel="noreferrer"><span class="bug-file-icon">${typeLabel}</span><span class="bug-attachment-name">${esc(name)}</span></a>`;
    }).join("");
    const attachmentStrip = attachments ? `<div class="bug-attachments">${attachments}</div>` : "";
    const expanded = state.expandedPipelines.has(bug.id);
    const pipelineButton = `<button class="button pipeline-button" data-action="pipeline" data-id="${esc(bug.id)}" aria-expanded="${expanded}">${expanded ? "收起 AI 执行过程" : "查看 AI 执行过程"}</button>`;
    const stopButton = !requirement && ["Bug处理中", "Review中"].includes(bug.status) ? `<button class="button danger" data-action="stop" data-id="${esc(bug.id)}">停止任务</button>` : "";
    const retryButton = bug.status === "已阻塞" ? `<button class="button secondary" data-action="retry" data-id="${esc(bug.id)}">重新执行</button>` : "";
    const confirmButton = requirement && bug.status === "待确认" ? `<button class="button primary" data-action="confirm" data-id="${esc(bug.id)}">确认需求</button>` : "";
    const developButton = requirement && bug.status === "待开发" ? `<button class="button primary" data-action="develop" data-id="${esc(bug.id)}">开始开发</button>` : "";
    const reviewButton = requirement && bug.status === "待合并" ? `<button class="button secondary" data-action="review" data-id="${esc(bug.id)}">发起 Review</button>` : "";
    const completeButton = bug.status === "待合并" ? `<button class="button primary" data-action="complete" data-id="${esc(bug.id)}">确认已合并</button>` : "";
    const archiveButton = ["待选择", "待回答", "待确认", "待开发", "已阻塞", "完成"].includes(bug.status) ? `<button class="button quiet" data-action="archive" data-id="${esc(bug.id)}">归档</button>` : "";
    const syncLabel = bug.sync_state === "synced" ? "飞书已同步" : bug.sync_state === "error" ? "飞书同步失败" : bug.sync_state === "pending" ? "等待飞书同步" : "仅保存在本地";
    const syncTone = bug.sync_state === "synced" ? "is-synced" : bug.sync_state === "error" ? "is-error" : bug.sync_state === "pending" ? "is-pending" : "";
    const syncChip = `<span class="sync-chip ${syncTone}" title="${esc(bug.sync_error || syncLabel)}">${syncLabel}</span>`;
    const coordination = bug.coordination || {};
    const affectedFiles = (coordination.affected_files || []).filter((file) => file !== "*");
    const coordinationPanel = coordination.message ? `<section class="coordination-panel">
      <div><b>代码冲突协调</b><span>${esc(coordination.message)}</span></div>
      ${coordination.similar_task_id ? `<small>相似任务：${esc(coordination.similar_task_id)} · ${Math.round((coordination.similarity || 0) * 100)}%</small>` : ""}
      ${coordination.merge_order ? `<small>建议合并顺序：${coordination.merge_order.map(esc).join(" → ")}</small>` : ""}
      ${affectedFiles.length ? `<details><summary>预计修改 ${affectedFiles.length} 个文件</summary><code>${affectedFiles.map(esc).join("\n")}</code></details>` : ""}
    </section>` : "";
    return `<article class="bug-card ${tone}">
      <div class="bug-main">
        <div>
          <div class="bug-title-row"><h3>${esc(bug.title)}</h3><span class="status-badge ${tone}">${esc(bug.status)}</span></div>
          <p class="bug-description">${esc(bug.description)}</p>
          <div class="bug-meta"><span>工作区 · ${esc(bug.workspace || "未设置")}</span>${requirement ? `<span>${esc(bug.clarify_agent)} 澄清</span><span>${esc(bug.code_agent)} 开发</span>` : `<span>${esc(bug.fix_agent)} 修复</span>`}<span>${esc(bug.review_agent)} Review</span>${syncChip}${link}</div>
          ${clarification}${prd}${attachmentStrip}${coordinationPanel}${details}
        </div>
        <div class="bug-actions">${pipelineButton}${stopButton}${retryButton}${confirmButton}${developButton}${reviewButton}${completeButton}${editButton}${startButton}${archiveButton}</div>
      </div>
      ${expanded ? renderPipelinePanel(bug) : ""}
    </article>`;
  }

  function pipelineStateMeta(value) {
    return ({
      pending: ["○", "未开始"],
      running: ["●", "进行中"],
      done: ["✓", "已完成"],
      failed: ["!", "未通过"],
      waiting: ["…", "等待中"],
    })[value] || ["○", value || "未开始"];
  }

  function formatEventTime(value) {
    if (!value) return "";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
  }

  function pipelineEventLabel(event) {
    const label = ({
      intake: "需求录入",
      clarify: "AI 澄清",
      confirm: "人工确认",
      develop: "AI 开发",
      prepare: "准备隔离分支",
      investigate: "AI 调查",
      coordinate: "代码范围协调",
      fix: "AI 修复",
      test: "自动验证",
      review: "独立 Review",
      delivery: "交付人工合并",
      pipeline: "流水线",
    })[event.stage] || event.stage;
    return event.iteration && ["fix", "test", "review"].includes(event.stage) ? `${label} · 第 ${event.iteration} 轮` : label;
  }

  function renderPipelinePanel(bug) {
    const pipeline = state.pipelines[bug.id];
    const loading = state.pipelineLoading.has(bug.id);
    const error = state.pipelineErrors[bug.id];
    if (!pipeline) {
      return `<section class="pipeline-panel" aria-live="polite"><div class="pipeline-empty ${error ? "is-error" : ""}">${error ? esc(error) : "正在读取 AI 执行过程…"}</div></section>`;
    }
    const steps = (pipeline.steps || []).map((step) => {
      const meta = pipelineStateMeta(step.state);
      return `<div class="pipeline-step is-${esc(step.state)}" title="${esc(step.detail || meta[1])}">
        <span class="pipeline-step-icon">${meta[0]}</span>
        <span class="pipeline-step-copy"><b>${esc(step.label)}</b><small>${esc(step.actor)} · ${meta[1]}</small></span>
      </div>`;
    }).join("");
    const detailedEvents = (pipeline.events || []).filter((event) => event.detail);
    const outputs = detailedEvents.map((event, index) => {
      const meta = pipelineStateMeta(event.state);
      const isLatest = index === detailedEvents.length - 1;
      const outputKey = `${bug.id}:${event.stage}:${event.iteration || 0}:${event.time || index}`;
      const isOpen = state.pipelineOutputOpen.has(outputKey) ? state.pipelineOutputOpen.get(outputKey) : isLatest;
      return `<details class="pipeline-output is-${esc(event.state)}" data-output-key="${esc(outputKey)}" ${isOpen ? "open" : ""}>
        <summary><span>${meta[0]}</span><b>${esc(pipelineEventLabel(event))}</b><small>${esc(event.agent || "Niuma")} · ${formatEventTime(event.time)}</small></summary>
        <pre data-scroll-key="pipeline:${esc(outputKey)}">${esc(event.detail)}</pre>
      </details>`;
    }).join("");
    const liveText = pipeline.live ? `<span class="pipeline-live"><i></i> 实时更新中</span>` : `<span class="pipeline-finished">流水线记录</span>`;
    const updated = pipeline.updated_at ? `最近更新 ${formatEventTime(pipeline.updated_at)}` : "等待流水线开始";
    return `<section class="pipeline-panel" aria-label="AI 执行过程" aria-live="polite">
      <div class="pipeline-heading"><div><b>AI 执行过程</b><small>${updated}</small></div>${liveText}</div>
      <div class="pipeline-rail">${steps}</div>
      <div class="pipeline-results-heading"><b>过程与结果</b><span>${loading ? "正在同步…" : "展开每一步可查看完整输出"}</span></div>
      <div class="pipeline-outputs">${outputs || `<div class="pipeline-empty">${pipeline.live ? "当前步骤正在执行，完成后会在这里显示结果。" : "这条任务暂时没有保存详细的 AI 输出。"}</div>`}</div>
    </section>`;
  }

  function bugPayload(prefix = "bug") {
    const editing = prefix === "edit";
    const taskType = editing ? byId("editTaskType").value : state.activeType;
    const requirement = taskType === "需求";
    return {
      task_type: taskType,
      title: byId(editing ? "editTitle" : "bugTitle").value.trim(),
      description: byId(editing ? "editDescription" : "bugDescription").value.trim(),
      clarification: editing ? byId("editClarification").value.trim() : "",
      prd: editing && requirement ? byId("editPRD").value.trim() : "",
      workspace: editing ? byId(requirement ? "editRequirementWorkspace" : "editWorkspace").value : byId("bugWorkspace").value,
      fix_agent: requirement ? "" : (editing ? byId("editAgent").value : new FormData(bugForm).get("fixAgent")),
      clarify_agent: requirement ? byId(editing ? "editClarifyAgent" : "requirementClarifyAgent").value : "",
      code_agent: requirement ? byId(editing ? "editCodeAgent" : "requirementCodeAgent").value : "",
      review_agent: requirement ? byId(editing ? "editReviewAgent" : "requirementReviewAgent").value : "",
      reporter: editing ? "" : byId("bugReporter").value.trim(),
    };
  }

  function selectedAttachments(files) {
    if (files.some((file) => file.size > 8 * 1024 * 1024)) throw new Error("单个附件不能超过 8MB");
    return Array.from(files);
  }

  async function uploadAttachments(id, files) {
    if (!files.length) return;
    const form = new FormData();
    files.forEach((file) => form.append("images", file, file.name));
    await api(`/api/tasks/${encodeURIComponent(id)}/attachments`, { method: "POST", body: form });
  }

  function attachmentKey(file) {
    return `${file.name}:${file.size}:${file.lastModified}`;
  }

  function showSelectedAttachments(files, target, scope) {
    target.innerHTML = files.map((file, index) => `<span><b>${esc(file.name)}</b><button type="button" data-remove-attachment="${index}" data-attachment-scope="${scope}" aria-label="移除 ${esc(file.name)}">×</button></span>`).join("");
  }

  function appendSelectedAttachments(input, scope) {
    const stateKey = scope === "edit" ? "editAttachments" : "createAttachments";
    const target = byId(scope === "edit" ? "editAttachmentSelection" : "bugAttachmentSelection");
    const known = new Set(state[stateKey].map(attachmentKey));
    Array.from(input.files || []).forEach((file) => {
      if (!known.has(attachmentKey(file))) {
        state[stateKey].push(file);
        known.add(attachmentKey(file));
      }
    });
    input.value = "";
    showSelectedAttachments(state[stateKey], target, scope);
  }

  function resetCreateForm(reporter) {
    bugForm.reset();
    byId("bugReporter").value = reporter;
    byId("bugWorkspace").value = "";
    state.createAttachments = [];
    byId("bugAttachmentSelection").innerHTML = "";
    bugForm.querySelector('input[value="claude"]').checked = true;
    byId("requirementClarifyAgent").value = "cursor";
    byId("requirementCodeAgent").value = "cursor";
    byId("requirementReviewAgent").value = "gemini";
  }

  bugForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    createError.textContent = "";
    const submitter = event.submitter;
    const start = submitter && submitter.dataset.start === "true";
    const buttons = bugForm.querySelectorAll("button");
    buttons.forEach((button) => button.disabled = true);
    let createdBug = null;
    try {
      const files = selectedAttachments(state.createAttachments);
      const payload = { ...bugPayload("bug"), start: false };
      const created = await api("/api/tasks", { method: "POST", body: JSON.stringify(payload) });
      createdBug = created.task;
      await uploadAttachments(createdBug.id, files);
      if (start) await api(`/api/tasks/${encodeURIComponent(createdBug.id)}/start`, { method: "POST", body: "{}" });
      localStorage.setItem("niuma-reporter", payload.reporter);
      resetCreateForm(payload.reporter);
      createTaskDialog.close();
      await refreshBugs();
      showToast(start ? (payload.task_type === "需求" ? "需求已进入 AI 澄清" : "Bug 已开始进入修复流水线") : `${payload.task_type}已保存，之后还可以修改`);
    } catch (error) {
      createError.textContent = createdBug ? `任务已保存，但附件上传或启动失败：${error.message}` : error.message;
      if (createdBug) await refreshBugs();
    } finally {
      buttons.forEach((button) => button.disabled = false);
    }
  });

  bugList.addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action]");
    if (!button) return;
    if (button.dataset.action === "close-task-detail") {
      state.detailTaskID = "";
      render();
      return;
    }
    const bug = state.bugs.find((item) => item.id === button.dataset.id);
    if (!bug) return;
    if (button.dataset.action === "view-task") {
      state.detailTaskID = bug.id;
      render();
      return;
    }
    if (button.dataset.action === "pipeline") {
      if (state.expandedPipelines.has(bug.id)) {
        state.expandedPipelines.delete(bug.id);
        render();
      } else {
        state.expandedPipelines.add(bug.id);
        render();
        await refreshPipeline(bug.id);
      }
      return;
    }
    if (button.dataset.action === "edit") {
      const requirement = bug.task_type === "需求";
      byId("editId").value = bug.id;
      byId("editTaskType").value = bug.task_type;
      byId("editEyebrow").textContent = requirement ? "EDIT REQUIREMENT" : "EDIT BUG";
      byId("editHeading").textContent = requirement ? "修改需求" : "修改 Bug";
      byId("editTitle").value = bug.title;
      byId("editDescription").value = bug.description;
      byId("editClarification").value = bug.clarification || "";
      byId("editPRDField").hidden = !requirement;
      byId("editPRD").value = bug.prd || "";
      byId("editBugAgentFields").hidden = requirement;
      byId("editRequirementAgentFields").hidden = !requirement;
      if (requirement) {
        byId("editRequirementWorkspace").value = bug.workspace;
        byId("editClarifyAgent").value = bug.clarify_agent;
        byId("editCodeAgent").value = bug.code_agent;
        byId("editReviewAgent").value = bug.review_agent;
      } else {
        byId("editWorkspace").value = bug.workspace;
        byId("editAgent").value = bug.fix_agent;
      }
      byId("editAttachments").value = "";
      state.editAttachments = [];
      byId("editAttachmentSelection").innerHTML = "";
      editError.textContent = "";
      editDialog.showModal();
      return;
    }
    if (button.dataset.action === "start") {
      const verb = bug.task_type === "需求" ? "澄清" : "修复";
      if (!window.confirm(`确认开始${verb}“${bug.title}”？开始后当前阶段不能再修改。`)) return;
      button.disabled = true;
      try {
        await api(`/api/tasks/${encodeURIComponent(bug.id)}/start`, { method: "POST", body: "{}" });
        await refreshBugs();
        showToast(bug.task_type === "需求" ? "已进入需求澄清流水线" : "已进入 Bug 修复流水线");
      } catch (error) {
        showToast(error.message, true);
        button.disabled = false;
      }
      return;
    }
    const managedActions = {
      stop: ["停止", "确认停止这条正在运行的任务？任务会停在“已阻塞”，之后可以重新执行。"],
      retry: ["重新执行", "确认重新执行这条任务？"],
      confirm: ["确认需求", "确认当前 PRD 和验收标准，可以进入待开发队列？"],
      develop: ["开始开发", "确认开始开发这条需求？Agent 会在对应工作区执行。"],
      review: ["发起 Review", "确认交给另一个 Agent 审查当前改动？"],
      complete: ["确认完成", bug.task_type === "Bug" ? "确认这条 Bug 已人工处理完成？确认后会直接标记完成，不再校验修复分支是否已合入 main。" : "确认需求代码已经人工检查并合并，可以标记完成？"],
      archive: ["归档", "确认归档这条任务？归档后不会出现在看板中。"],
    };
    if (managedActions[button.dataset.action]) {
      const [label, question] = managedActions[button.dataset.action];
      if (!window.confirm(question)) return;
      button.disabled = true;
      try {
        await api(`/api/tasks/${encodeURIComponent(bug.id)}/${button.dataset.action}`, { method: "POST", body: "{}" });
        await refreshBugs();
        showToast(`${label}操作已提交`);
      } catch (error) {
        showToast(error.message, true);
        button.disabled = false;
      }
    }
  });

  bugList.addEventListener("toggle", (event) => {
    const details = event.target;
    if (!(details instanceof HTMLDetailsElement)) return;
    if (details.dataset.bugDetailsKey) {
      state.bugDetailsOpen.set(details.dataset.bugDetailsKey, details.open);
    }
    if (details.dataset.outputKey) {
      state.pipelineOutputOpen.set(details.dataset.outputKey, details.open);
    }
  }, true);

  bugList.addEventListener("scroll", (event) => {
    const element = event.target;
    if (!(element instanceof HTMLElement) || !element.dataset.scrollKey) return;
    state.scrollPositions.set(element.dataset.scrollKey, { top: element.scrollTop, left: element.scrollLeft });
  }, true);

  editForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (event.submitter && event.submitter.value === "cancel") {
      editDialog.close();
      return;
    }
    editError.textContent = "";
    byId("saveEditButton").disabled = true;
    try {
      const id = byId("editId").value;
      const files = selectedAttachments(state.editAttachments);
      const payload = bugPayload("edit");
      await api(`/api/tasks/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(payload) });
      await uploadAttachments(id, files);
      editDialog.close();
      await refreshBugs();
      showToast(`${payload.task_type}修改已保存；如已启用飞书会自动同步`);
    } catch (error) {
      editError.textContent = error.message;
    } finally {
      byId("saveEditButton").disabled = false;
    }
  });

  function formatUptime(seconds = 0) {
    if (seconds < 60) return `${seconds} 秒`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`;
    return `${Math.floor(seconds / 3600)} 小时 ${Math.floor((seconds % 3600) / 60)} 分钟`;
  }

  async function openManagement() {
    if (!manageDialog.open) manageDialog.showModal();
    byId("adminOverview").innerHTML = `<div class="admin-loading">正在读取运行状态…</div>`;
    try {
      const [overview, pipeline, workspaces, integrations, developmentSessions] = await Promise.all([
        api("/api/admin/overview"),
        api("/api/admin/pipeline"),
        api("/api/admin/workspaces"),
        api("/api/admin/integrations"),
		api("/api/admin/development-sessions"),
      ]);
	  state.admin = { overview, pipeline: pipeline.pipeline, workspaces: workspaces.workspaces, integrations, developmentSessions };
      state.workspaceEntries = Object.entries(state.admin.workspaces.items || {}).map(([key, item]) => ({ key, ...item }));
      renderManagement();
    } catch (error) {
      byId("adminOverview").innerHTML = `<div class="admin-loading">${esc(error.message)}</div>`;
      showToast(error.message, true);
    }
  }

  function renderManagement() {
    const overview = state.admin.overview;
    const integration = overview.integration || {};
    const statuses = overview.statuses || {};
    byId("adminOverview").innerHTML = `
      <div class="admin-stat"><span>任务主数据源</span><b>本地 SQLite</b><small>飞书关闭或断线也可运行</small></div>
      <div class="admin-stat"><span>任务总数</span><b>${overview.total || 0}</b><small>处理中 ${(statuses["Bug处理中"] || 0) + (statuses["待澄清"] || 0) + (statuses["开发中"] || 0) + (statuses["Review中"] || 0)} · 已阻塞 ${statuses["已阻塞"] || 0}</small></div>
      <div class="admin-stat"><span>当前运行</span><b>${(overview.active_runs || []).length}</b><small>${(overview.active_runs || []).map((run) => esc(run.record_id)).join("、") || "没有 Agent 正在运行"}</small></div>
      <div class="admin-stat"><span>服务运行时间</span><b>${formatUptime(overview.uptime_seconds || 0)}</b><small>已启用 ${overview.enabled_integrations || 0} 个连接器</small></div>`;

    byId("integrationEnabled").checked = Boolean(integration.enabled);
    byId("integrationAppId").value = "";
    byId("integrationAppId").placeholder = integration.app_id ? `已保存：${integration.app_id}` : "cli_...";
    byId("integrationAppSecret").value = "";
    byId("integrationAppSecret").placeholder = integration.has_app_secret ? "已保存，留空不修改" : "请输入 App Secret";
    byId("integrationBaseToken").value = "";
    byId("integrationBaseToken").placeholder = integration.base_token ? `已保存：${integration.base_token}` : "Base Token";
    byId("integrationTableId").value = integration.table_id || "";
    byId("integrationSyncPeriod").value = integration.sync_period || 60;
    const badge = byId("integrationBadge");
    badge.textContent = integration.enabled ? (integration.last_sync_error ? "连接异常" : "已启用") : "未启用";
    badge.className = `admin-badge ${integration.enabled ? (integration.last_sync_error ? "is-error" : "is-on") : ""}`;
    byId("integrationStatusText").textContent = integration.last_sync_error
      ? `最近同步失败：${integration.last_sync_error}`
      : integration.last_sync_at ? `最近同步：${new Date(integration.last_sync_at).toLocaleString("zh-CN")}` : "尚未执行飞书同步";

    renderConnectorHub();

    const pipeline = state.admin.pipeline;
    byId("adminFixAgent").value = pipeline.bug_fix_agent;
    byId("adminReviewAgent").value = pipeline.bug_review_agent;
    byId("adminRepairLimit").value = pipeline.bug_repair_limit;
    byId("adminCodeTimeout").value = pipeline.timeout_code;
    byId("adminReviewTimeout").value = pipeline.timeout_review;
    byId("adminBugTimeout").value = pipeline.timeout_bug;
	  renderWorkspaceEditors();
	  renderDevelopmentSessions();
  }

  function connectorStatuses() {
    const feishu = state.admin?.overview?.integration || {};
    return [{
      kind: "feishu", label: "飞书", icon: "飞", enabled: Boolean(feishu.enabled), configured: Boolean(feishu.configured),
      description: "多维表格、消息卡片和长连接", last_test_error: feishu.last_sync_error || "",
      capabilities: ["双向同步", "消息卡片", "长连接"],
    }, ...(state.admin?.integrations?.integrations || [])];
  }

  function selectedConnectorStatus() {
    return connectorStatuses().find((item) => item.kind === state.selectedConnector) || connectorStatuses()[0];
  }

  function renderConnectorHub() {
    const statuses = connectorStatuses();
    const enabled = statuses.filter((item) => item.enabled).length;
    const errors = statuses.filter((item) => item.last_test_error).length;
    const hubBadge = byId("integrationHubBadge");
    hubBadge.textContent = errors ? `${errors} 个异常` : enabled ? `已启用 ${enabled}` : "全部可选";
    hubBadge.className = `admin-badge ${errors ? "is-error" : enabled ? "is-on" : ""}`;
    byId("connectorGrid").innerHTML = statuses.map((item) => {
      const definition = connectorDefinitions[item.kind] || {};
      const stateClass = item.last_test_error ? "is-error" : item.enabled ? "is-on" : "";
      const footer = item.last_test_error ? "连接检查失败" : item.enabled ? "已启用配置" : item.configured ? "已保存，当前停用" : "等待配置";
      return `<button class="connector-card ${item.kind === state.selectedConnector ? "is-selected" : ""}" type="button" data-connector-kind="${esc(item.kind)}" style="--connector-color:${definition.color || "#6d4aff"}">
        <span class="connector-card-body">
          <span class="connector-card-top"><span class="connector-logo">${esc(definition.icon || item.icon || "?")}</span><i class="connector-state ${stateClass}"></i></span>
          <h4>${esc(item.name || definition.label || item.label)}</h4>
          <p>${esc(item.description || definition.description || "第三方连接器")}</p>
          <span class="connector-card-footer">${esc(footer)}</span>
        </span>
      </button>`;
    }).join("");
    renderConnectorEditor(selectedConnectorStatus());
    renderIntegrationEvents();
  }

  function connectorField(label, name, value = "", options = null, secretSaved = false, placeholder = "") {
    if (options) {
      return `<div><label>${esc(label)}</label><select data-connector-field="${esc(name)}">${options.map(([optionValue, optionLabel]) => `<option value="${esc(optionValue)}" ${value === optionValue ? "selected" : ""}>${esc(optionLabel)}</option>`).join("")}</select></div>`;
    }
    const isSecret = ["api_token", "app_token", "signing_secret", "webhook_secret"].includes(name);
    const type = isSecret ? "password" : name === "base_url" ? "url" : "text";
    const shownValue = isSecret ? "" : value;
    const hint = isSecret && secretSaved ? "已保存，留空不修改" : placeholder;
    return `<div><label>${esc(label)}</label><input data-connector-field="${esc(name)}" type="${type}" value="${esc(shownValue)}" placeholder="${esc(hint)}" ${isSecret ? 'autocomplete="new-password"' : ""}></div>`;
  }

  function workspaceConnectorField(status) {
    const options = [["", "使用系统默认工作区"], ...(state.workspaceEntries || []).map((entry) => [entry.key, entry.key])];
    return connectorField("映射到代码工作区", "workspace", status.workspace || "", options);
  }

  function thirdPartyFieldMarkup(status) {
    if (status.kind === "zentao") {
      return [
        connectorField("连接器名称", "name", status.name || "禅道"),
        connectorField("禅道地址", "base_url", status.base_url || "", null, false, "http://zentao.example.com"),
        connectorField("API 路径", "api_path", status.api_path || "/api.php/v1"),
        connectorField("账号（可选）", "api_user", status.api_user || ""),
        connectorField("API Token", "api_token", "", null, status.has_api_token),
        connectorField("产品 / 项目标识", "project", status.project || ""),
        workspaceConnectorField(status),
        connectorField("触发过滤", "trigger_filter", status.trigger_filter || "", null, false, "例如 bug,story"),
        connectorField("Webhook Secret", "webhook_secret", "", null, status.has_webhook_secret),
      ].join("");
    }
    if (status.kind === "jira") {
      return [
        connectorField("连接器名称", "name", status.name || "Jira"),
        connectorField("部署类型", "deployment", status.deployment || "cloud", [["cloud", "Jira Cloud"], ["dc", "Jira Data Center"]]),
        connectorField("Jira 地址", "base_url", status.base_url || "", null, false, "https://company.atlassian.net"),
        connectorField("REST API 路径", "api_path", status.api_path || "/rest/api/3"),
        connectorField("邮箱 / 用户名", "api_user", status.api_user || ""),
        connectorField("API Token", "api_token", "", null, status.has_api_token),
        connectorField("项目 Key", "project", status.project || "", null, false, "例如 QTCC"),
        workspaceConnectorField(status),
        connectorField("JQL / 标签过滤", "trigger_filter", status.trigger_filter || "", null, false, "例如 labels = niuma"),
        connectorField("Webhook Secret", "webhook_secret", "", null, status.has_webhook_secret),
      ].join("");
    }
    return [
      connectorField("连接器名称", "name", status.name || "Slack"),
      connectorField("接入模式", "mode", status.mode || "socket", [["socket", "Socket Mode（局域网推荐）"], ["webhook", "Events API Webhook"]]),
      connectorField("Bot Token", "api_token", "", null, status.has_api_token, "xoxb-..."),
      connectorField("App Token", "app_token", "", null, status.has_app_token, "xapp-..."),
      connectorField("Signing Secret", "signing_secret", "", null, status.has_signing_secret),
      connectorField("默认频道", "default_channel", status.default_channel || "", null, false, "例如 #研发通知"),
      workspaceConnectorField(status),
      connectorField("触发规则", "trigger_filter", status.trigger_filter || "", null, false, "例如 /niuma 或特定频道"),
    ].join("");
  }

  function renderConnectorEditor(status) {
    if (!status) return;
    const definition = connectorDefinitions[status.kind] || {};
    byId("connectorEditorIcon").textContent = definition.icon || status.icon || "?";
    byId("connectorEditorIcon").style.setProperty("--connector-color", definition.color || "#6d4aff");
    byId("connectorEditorEyebrow").textContent = definition.eyebrow || "CONNECTOR";
    byId("connectorEditorTitle").textContent = `配置${status.name || definition.label || status.label}`;
    byId("connectorEditorDescription").textContent = definition.description || status.description || "第三方连接器";

    const badge = byId("integrationBadge");
    badge.textContent = status.last_test_error ? "连接异常" : status.enabled ? "已启用" : status.configured ? "已保存" : "未配置";
    badge.className = `admin-badge ${status.last_test_error ? "is-error" : status.enabled ? "is-on" : ""}`;

    const isFeishu = status.kind === "feishu";
    byId("feishuConnectorPanel").hidden = !isFeishu;
    thirdPartyIntegrationForm.hidden = isFeishu;
    if (isFeishu) return;

    byId("thirdPartyKind").value = status.kind;
    byId("thirdPartyEnabled").checked = Boolean(status.enabled);
    byId("thirdPartyEnabledLabel").textContent = `启用${status.name || status.label}连接器`;
    byId("thirdPartyFields").innerHTML = thirdPartyFieldMarkup(status);
    const absoluteWebhook = `${window.location.origin}${status.webhook_path || `/api/integrations/${status.kind}/events`}`;
    if (status.kind === "slack" && status.mode === "socket") {
      byId("thirdPartyWebhook").innerHTML = `<b>Socket Mode</b> 不需要公网回调地址。当前 MVP 已完成配置、Token 测试和 Events API 验签入口；Socket 长连接消费会在下一步启用。`;
    } else {
      byId("thirdPartyWebhook").innerHTML = `<b>Webhook 事件入口</b><code>${esc(absoluteWebhook)}</code>禅道 / Jira 可通过查询参数 <code>?token=你的 Webhook Secret</code>，也可以发送 X-Niuma-Integration-Token 请求头。事件会先验签、去重并落入本地队列。`;
    }
    byId("thirdPartyStatusText").textContent = status.last_test_error
      ? `最近测试失败：${status.last_test_error}`
      : status.last_test_at ? `${status.last_message || "连接正常"} · ${new Date(status.last_test_at).toLocaleString("zh-CN")}`
        : "尚未测试连接。保存前可以先验证 API Token 是否可用。";
  }

  function renderIntegrationEvents() {
    const events = state.admin?.integrations?.events || [];
    byId("integrationEventCount").textContent = events.length;
    byId("integrationEventList").innerHTML = events.length ? events.map((event) => `<div class="integration-event">
      <b>${esc(event.integration_kind)}</b><span>${esc(event.summary || event.event_key)}</span><time>${new Date(Number(event.created_at || 0) * 1000).toLocaleString("zh-CN")}</time>
    </div>`).join("") : "<p>还没有收到第三方事件。配置连接器后，这里会显示 Webhook 接收记录。</p>";
  }

  function workspaceEditor(entry, index) {
    const value = (key, fallback = "") => esc(entry[key] ?? fallback);
    return `<article class="workspace-editor" data-index="${index}">
      <div class="workspace-editor-head"><strong>${value("key", `workspace${index + 1}`)}</strong><button class="button remove-workspace" data-remove-workspace="${index}" type="button">删除</button></div>
      <div class="workspace-editor-grid">
        <div><label>工作区 Key</label><input data-field="key" value="${value("key")}" required></div>
        <div class="wide"><label>仓库绝对路径</label><input data-field="path" value="${value("path")}" required></div>
        <div><label>SCM</label><select data-field="scm"><option value="git" ${entry.scm !== "svn" ? "selected" : ""}>Git</option><option value="svn" ${entry.scm === "svn" ? "selected" : ""}>SVN</option></select></div>
        <div><label>工作模式</label><select data-field="work_mode"><option value="worktree" ${entry.work_mode !== "inline" ? "selected" : ""}>独立 worktree</option><option value="inline" ${entry.work_mode === "inline" ? "selected" : ""}>当前目录 inline</option></select></div>
		<div><label>工作区范围</label><select data-field="workspace_scope"><option value="task" ${entry.workspace_scope !== "session" ? "selected" : ""}>每任务独立</option><option value="session" ${entry.workspace_scope === "session" ? "selected" : ""}>共享开发会话</option></select></div>
		<div><label>任务执行</label><select data-field="queue_mode"><option value="parallel" ${entry.queue_mode !== "serial" ? "selected" : ""}>并行开发、串行集成</option><option value="serial" ${entry.queue_mode === "serial" ? "selected" : ""}>全部串行</option></select></div>
		<div><label>会话滚动</label><select data-field="session_rollover"><option value="daily" ${entry.session_rollover === "daily" ? "selected" : ""}>每天</option><option value="manual" ${entry.session_rollover !== "daily" ? "selected" : ""}>手动</option></select></div>
		<div><label>交付目标</label><select data-field="delivery_target"><option value="user_choose" ${entry.delivery_target === "user_choose" ? "selected" : ""}>完成后由用户选择</option><option value="fixed" ${entry.delivery_target !== "user_choose" ? "selected" : ""}>固定目标分支</option></select></div>
        <div><label>基线</label><input data-field="base" value="${value("base", "origin/main")}"></div>
        <div><label>目标分支</label><input data-field="target_branch" value="${value("target_branch", "main")}"></div>
        <div class="wide"><label>worktree 统一目录</label><input data-field="worktree_base" value="${value("worktree_base")}"></div>
        <div><label>规则来源目录</label><input data-field="rules_source" value="${value("rules_source")}"></div>
        <div class="wide"><label>JDK Home</label><input data-field="java_home" value="${value("java_home")}" placeholder="例如 /path/to/jdk/Contents/Home"></div>
        <div class="wide"><label>Maven Home</label><input data-field="maven_home" value="${value("maven_home")}" placeholder="例如 /path/to/apache-maven-3.8.9"></div>
        <div class="wide"><label>验证命令</label><input data-field="test_cmd" value="${value("test_cmd")}" placeholder="例如 mvn test"></div>
        <div><label>PR 提供方</label><select data-field="pr_provider"><option value="none" ${!entry.pr_provider || entry.pr_provider === "none" ? "selected" : ""}>不自动创建</option><option value="github" ${entry.pr_provider === "github" ? "selected" : ""}>GitHub</option><option value="gitlab" ${entry.pr_provider === "gitlab" ? "selected" : ""}>GitLab</option></select></div>
        <div><label>GitHub 仓库</label><input data-field="gh_repo" value="${value("gh_repo")}" placeholder="owner/repo"></div>
      </div>
      <div class="workspace-checks">
        <label><input type="checkbox" data-field="push_enabled" ${entry.push_enabled ? "checked" : ""}>允许自动 Push</label>
        <label><input type="checkbox" data-field="pr_enabled" ${entry.pr_enabled ? "checked" : ""}>允许自动创建 PR</label>
		<label><input type="checkbox" data-field="track_upstream" ${entry.track_upstream ? "checked" : ""}>Push 后跟踪远端任务分支</label>
      </div>
    </article>`;
  }

  function renderDevelopmentSessions() {
	const sessions = state.admin?.developmentSessions?.sessions || [];
	byId("developmentSessionList").innerHTML = sessions.length ? sessions.map((session) => {
	  const tasks = session.tasks || [];
	  const integrated = tasks.filter((task) => task.state === "integrated").length;
	  const taskRows = tasks.map((task) => `<span class="development-session-task is-${esc(task.state)}">#${task.sequence} ${esc(task.task_kind)}-${esc(task.record_id)} · ${esc(task.state)}</span>`).join("");
	  return `<article class="development-session-card">
		<div class="development-session-head"><div><b>${esc(session.branch)}</b><small>${esc(session.workspace)} · ${esc(session.base_ref)}@${esc((session.base_sha || "").slice(0, 8))}</small></div><span class="admin-badge ${session.state === "open" ? "is-on" : ""}">${session.state === "open" ? "进行中" : "已冻结"}</span></div>
		<p>${integrated}/${tasks.length} 个任务已集成 · ${new Date(Number(session.created_at || 0) * 1000).toLocaleString("zh-CN")}</p>
		<div class="development-session-tasks">${taskRows || "<span>等待首个任务</span>"}</div>
		${session.state === "open" ? `<button class="button secondary" type="button" data-freeze-session="${esc(session.session_id)}">冻结会话</button>` : ""}
	  </article>`;
	}).join("") : "<p>还没有共享开发会话。</p>";
  }

  function renderWorkspaceEditors() {
    byId("workspaceEditorList").innerHTML = state.workspaceEntries.map(workspaceEditor).join("");
    const defaultSelect = byId("adminDefaultWorkspace");
    defaultSelect.innerHTML = state.workspaceEntries.map((entry) => `<option value="${esc(entry.key)}">${esc(entry.key)}</option>`).join("");
    defaultSelect.value = state.admin.workspaces.default || state.workspaceEntries[0]?.key || "";
  }

  function integrationPayload() {
    return {
      enabled: byId("integrationEnabled").checked,
      app_id: byId("integrationAppId").value.trim(),
      app_secret: byId("integrationAppSecret").value.trim(),
      base_token: byId("integrationBaseToken").value.trim(),
      table_id: byId("integrationTableId").value.trim(),
      sync_period: Number(byId("integrationSyncPeriod").value || 60),
    };
  }

  function thirdPartyIntegrationPayload() {
    const payload = { enabled: byId("thirdPartyEnabled").checked };
    byId("thirdPartyFields").querySelectorAll("[data-connector-field]").forEach((field) => {
      payload[field.dataset.connectorField] = field.value.trim();
    });
    return payload;
  }

  function collectWorkspaces() {
    const items = {};
    byId("workspaceEditorList").querySelectorAll(".workspace-editor").forEach((card) => {
      const read = (name) => card.querySelector(`[data-field="${name}"]`);
      const key = read("key").value.trim();
      items[key] = {
        path: read("path").value.trim(), scm: read("scm").value, work_mode: read("work_mode").value,
		workspace_scope: read("workspace_scope").value, queue_mode: read("queue_mode").value,
		session_rollover: read("session_rollover").value, delivery_target: read("delivery_target").value,
        worktree_base: read("worktree_base").value.trim(), rules_source: read("rules_source").value.trim(),
        base: read("base").value.trim(), target_branch: read("target_branch").value.trim(),
        java_home: read("java_home").value.trim(), maven_home: read("maven_home").value.trim(),
        test_cmd: read("test_cmd").value.trim(), pr_provider: read("pr_provider").value,
        gh_repo: read("gh_repo").value.trim(), push_enabled: read("push_enabled").checked,
        pr_enabled: read("pr_enabled").checked,
		track_upstream: read("track_upstream").checked,
      };
    });
    return { default: byId("adminDefaultWorkspace").value, items };
  }

  integrationForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    try {
      await api("/api/admin/integration", { method: "PUT", body: JSON.stringify(integrationPayload()) });
      state.meta = await api("/api/meta");
      updateDataMode();
      await openManagement();
      showToast("飞书设置已保存");
    } catch (error) { showToast(error.message, true); }
  });

  byId("testIntegrationButton").addEventListener("click", async () => {
    try {
      const result = await api("/api/admin/integration/test", { method: "POST", body: JSON.stringify(integrationPayload()) });
      showToast(result.message || "飞书连接正常");
    } catch (error) { showToast(error.message, true); }
  });

  byId("syncIntegrationButton").addEventListener("click", async () => {
    try {
      await api("/api/admin/integration/sync", { method: "POST", body: "{}" });
      await Promise.all([refreshBugs(), openManagement()]);
      showToast("飞书同步完成");
    } catch (error) { showToast(error.message, true); }
  });

  byId("connectorGrid").addEventListener("click", (event) => {
    const card = event.target.closest("[data-connector-kind]");
    if (!card) return;
    state.selectedConnector = card.dataset.connectorKind;
    renderConnectorHub();
  });

  thirdPartyIntegrationForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const kind = byId("thirdPartyKind").value;
    try {
      await api(`/api/admin/integrations/${encodeURIComponent(kind)}`, { method: "PUT", body: JSON.stringify(thirdPartyIntegrationPayload()) });
      await openManagement();
      showToast(`${connectorDefinitions[kind]?.label || kind}连接器已保存`);
    } catch (error) { showToast(error.message, true); }
  });

  byId("testThirdPartyButton").addEventListener("click", async () => {
    const kind = byId("thirdPartyKind").value;
    const button = byId("testThirdPartyButton");
    button.disabled = true;
    try {
      const result = await api(`/api/admin/integrations/${encodeURIComponent(kind)}/test`, { method: "POST", body: JSON.stringify(thirdPartyIntegrationPayload()) });
      await openManagement();
      showToast(result.message || `${connectorDefinitions[kind]?.label || kind}连接正常`);
    } catch (error) { showToast(error.message, true); }
    finally { button.disabled = false; }
  });

  pipelineSettingsForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const payload = {
      bug_fix_agent: byId("adminFixAgent").value, bug_review_agent: byId("adminReviewAgent").value,
      bug_repair_limit: Number(byId("adminRepairLimit").value), timeout_code: Number(byId("adminCodeTimeout").value),
      timeout_review: Number(byId("adminReviewTimeout").value), timeout_bug: Number(byId("adminBugTimeout").value),
    };
    try {
      await api("/api/admin/pipeline", { method: "PUT", body: JSON.stringify(payload) });
      showToast("流水线默认设置已保存");
      await openManagement();
    } catch (error) { showToast(error.message, true); }
  });

  byId("addWorkspaceButton").addEventListener("click", () => {
    let number = state.workspaceEntries.length + 1;
    while (state.workspaceEntries.some((entry) => entry.key === `workspace${number}`)) number += 1;
	state.workspaceEntries.push({ key: `workspace${number}`, scm: "git", work_mode: "worktree", workspace_scope: "task", queue_mode: "parallel", session_rollover: "manual", delivery_target: "fixed", base: "origin/main", target_branch: "main", pr_provider: "none" });
    renderWorkspaceEditors();
  });

  byId("workspaceEditorList").addEventListener("click", (event) => {
    const button = event.target.closest("[data-remove-workspace]");
    if (!button) return;
    if (state.workspaceEntries.length <= 1) { showToast("至少保留一个工作区", true); return; }
    state.workspaceEntries.splice(Number(button.dataset.removeWorkspace), 1);
    renderWorkspaceEditors();
  });

  byId("saveWorkspacesButton").addEventListener("click", async () => {
    try {
      await api("/api/admin/workspaces", { method: "PUT", body: JSON.stringify(collectWorkspaces()) });
      state.meta = await api("/api/meta");
      fillWorkspaceSelects();
      await openManagement();
      showToast("工作区配置已保存");
    } catch (error) { showToast(error.message, true); }
  });

  byId("developmentSessionList").addEventListener("click", async (event) => {
	const button = event.target.closest("[data-freeze-session]");
	if (!button) return;
	button.disabled = true;
	try {
	  await api(`/api/admin/development-sessions/${encodeURIComponent(button.dataset.freezeSession)}/freeze`, { method: "POST", body: "{}" });
	  await openManagement();
	  showToast("共享开发会话已冻结，可以人工选择交付分支");
	} catch (error) { showToast(error.message, true); }
	finally { button.disabled = false; }
  });

  function showToast(message, isError = false) {
    toast.textContent = message;
    toast.style.background = isError ? "#b72f44" : "#211f2f";
    toast.hidden = false;
    clearTimeout(showToast.timer);
    showToast.timer = setTimeout(() => toast.hidden = true, 3000);
  }

  byId("refreshButton").addEventListener("click", refreshBugs);
  byId("manageButton").addEventListener("click", openManagement);
  byId("openCreateTaskButton").addEventListener("click", () => {
    createError.textContent = "";
    createTaskDialog.showModal();
  });
  byId("closeCreateTaskButton").addEventListener("click", () => createTaskDialog.close());
  byId("closeManageButton").addEventListener("click", () => manageDialog.close());
  byId("bugAttachments").addEventListener("change", () => appendSelectedAttachments(byId("bugAttachments"), "create"));
  byId("editAttachments").addEventListener("change", () => appendSelectedAttachments(byId("editAttachments"), "edit"));
  [byId("bugAttachmentSelection"), byId("editAttachmentSelection")].forEach((container) => container.addEventListener("click", (event) => {
    const button = event.target.closest("button[data-remove-attachment]");
    if (!button) return;
    const stateKey = button.dataset.attachmentScope === "edit" ? "editAttachments" : "createAttachments";
    state[stateKey].splice(Number(button.dataset.removeAttachment), 1);
    showSelectedAttachments(state[stateKey], container, button.dataset.attachmentScope);
  }));
  document.querySelectorAll("[data-task-tab]").forEach((tab) => tab.addEventListener("click", () => {
    state.detailTaskID = "";
    applyTaskTab(tab.dataset.taskTab);
  }));
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && state.detailTaskID) {
      state.detailTaskID = "";
      render();
    }
  });
  byId("searchInput").addEventListener("input", () => {
    state.filters[state.activeType].query = byId("searchInput").value;
    render();
  });
  byId("statusFilter").addEventListener("change", () => {
    state.filters[state.activeType].status = byId("statusFilter").value;
    render();
  });
  byId("workspaceFilter").addEventListener("change", () => {
    state.filters[state.activeType].workspace = byId("workspaceFilter").value;
    render();
  });

  bootstrap().catch((error) => showToast(error.message, true));
  setInterval(() => {
    if (!document.hidden) refreshBugs();
  }, 10000);
  setInterval(() => {
    if (document.hidden) return;
    state.expandedPipelines.forEach((id) => refreshPipeline(id, true));
  }, 2000);
})();
