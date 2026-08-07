(() => {
  const state = {
    meta: null,
    bugs: [],
    expandedPipelines: new Set(),
    pipelines: {},
    pipelineLoading: new Set(),
    pipelineErrors: {},
    bugDetailsOpen: new Map(),
    pipelineOutputOpen: new Map(),
    scrollPositions: new Map(),
  };
  const byId = (id) => document.getElementById(id);
  const bugForm = byId("bugForm");
  const createError = byId("createError");
  const bugList = byId("bugList");
  const emptyState = byId("emptyState");
  const editDialog = byId("editDialog");
  const editForm = byId("editForm");
  const editError = byId("editError");
  const toast = byId("toast");

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
    const reporter = localStorage.getItem("niuma-reporter") || "";
    byId("bugReporter").value = reporter;
    await refreshBugs();
  }

  function fillWorkspaceSelects() {
    const options = state.meta.workspaces.map((workspace) => `<option value="${esc(workspace)}">${esc(workspace)}</option>`).join("");
    byId("bugWorkspace").innerHTML = `<option value="">请选择代码仓库…</option>${options}`;
    byId("editWorkspace").innerHTML = options;
  }

  async function refreshBugs() {
    byId("refreshButton").disabled = true;
    try {
      const data = await api("/api/bugs");
      state.bugs = data.bugs || [];
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
      const data = await api(`/api/bugs/${encodeURIComponent(id)}/pipeline`);
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
    if (["Bug处理中", "Review中"].includes(status)) return "status-active";
    if (["待选择", "待回答"].includes(status)) return "status-waiting";
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
    const visible = state.bugs.filter((bug) => {
      const matchesText = !query || `${bug.title} ${bug.description}`.toLowerCase().includes(query);
      return matchesText && (!status || bug.status === status);
    });
    renderSummary();
    bugList.innerHTML = visible.map(renderBug).join("");
    bugList.querySelectorAll("[data-scroll-key]").forEach((element) => {
      const position = state.scrollPositions.get(element.dataset.scrollKey);
      if (!position) return;
      element.scrollTop = position.top;
      element.scrollLeft = position.left;
    });
    window.scrollTo(viewport.x, viewport.y);
    emptyState.hidden = visible.length !== 0;
  }

  function renderSummary() {
    const active = state.bugs.filter((bug) => ["Bug处理中", "Review中"].includes(bug.status)).length;
    const waiting = state.bugs.filter((bug) => ["待选择", "待回答", "已阻塞"].includes(bug.status)).length;
    const merge = state.bugs.filter((bug) => bug.status === "待合并").length;
    byId("summary").innerHTML = `<span>处理中 ${active}</span><span>待你处理 ${waiting}</span><span>待合并 ${merge}</span>`;
  }

  function renderBug(bug) {
    const tone = statusTone(bug.status);
    const editButton = bug.editable ? `<button class="button secondary" data-action="edit" data-id="${esc(bug.id)}">修改</button>` : "";
    const startLabel = bug.status === "待回答" ? "补充后继续" : bug.status === "已阻塞" ? "重新开始" : "开始修复";
    const startButton = bug.startable ? `<button class="button primary" data-action="start" data-id="${esc(bug.id)}">${startLabel}</button>` : "";
    const clarification = bug.clarification ? `<div class="bug-meta"><span>补充：${esc(bug.clarification).slice(0, 240)}</span></div>` : "";
    const link = bug.link ? (/^https?:\/\//i.test(bug.link) ? `<a href="${esc(bug.link)}" target="_blank" rel="noreferrer">查看 PR / MR</a>` : `<code>${esc(bug.link)}</code>`) : "";
    const detailsOpen = state.bugDetailsOpen.get(bug.id) === true;
    const details = bug.log || bug.clarification ? `<details class="bug-details" data-bug-details-key="${esc(bug.id)}" ${detailsOpen ? "open" : ""}><summary>查看处理记录</summary>${bug.clarification ? `<p>${esc(bug.clarification)}</p>` : ""}${bug.log ? `<pre data-scroll-key="bug-log:${esc(bug.id)}">${esc(bug.log)}</pre>` : ""}</details>` : "";
    const attachments = (bug.images || []).map((attachment) => {
      const name = attachment.name || "Bug 附件";
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
    return `<article class="bug-card ${tone}">
      <div class="bug-main">
        <div>
          <div class="bug-title-row"><h3>${esc(bug.title)}</h3><span class="status-badge ${tone}">${esc(bug.status)}</span></div>
          <p class="bug-description">${esc(bug.description)}</p>
          <div class="bug-meta"><span>工作区 · ${esc(bug.workspace || "未设置")}</span><span>${esc(bug.fix_agent)} 修复</span><span>${esc(bug.review_agent)} Review</span>${link}</div>
          ${clarification}${attachmentStrip}${details}
        </div>
        <div class="bug-actions">${pipelineButton}${editButton}${startButton}</div>
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
      prepare: "准备隔离分支",
      investigate: "AI 调查",
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
    const get = (suffix) => byId(`${prefix}${suffix}`).value;
    const fixAgent = prefix === "bug" ? new FormData(bugForm).get("fixAgent") : byId("editAgent").value;
    return {
      title: get("Title").trim(),
      description: get("Description").trim(),
      clarification: prefix === "edit" ? byId("editClarification").value.trim() : "",
      workspace: get("Workspace"),
      fix_agent: fixAgent,
      reporter: prefix === "bug" ? byId("bugReporter").value.trim() : "",
    };
  }

  function selectedAttachments(input) {
    const files = Array.from(input.files || []);
    if (files.length > 5) throw new Error("每次最多选择 5 个附件");
    if (files.some((file) => file.size > 8 * 1024 * 1024)) throw new Error("单个附件不能超过 8MB");
    return files;
  }

  async function uploadAttachments(id, files) {
    if (!files.length) return;
    const form = new FormData();
    files.forEach((file) => form.append("images", file, file.name));
    await api(`/api/bugs/${encodeURIComponent(id)}/images`, { method: "POST", body: form });
  }

  function showSelectedAttachments(input, target) {
    target.innerHTML = Array.from(input.files || []).map((file) => `<span>${esc(file.name)}</span>`).join("");
  }

  function resetCreateForm(reporter) {
    bugForm.reset();
    byId("bugReporter").value = reporter;
    byId("bugWorkspace").value = "";
    byId("bugAttachmentSelection").innerHTML = "";
    bugForm.querySelector('input[value="codex"]').checked = true;
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
      const files = selectedAttachments(byId("bugAttachments"));
      const payload = { ...bugPayload("bug"), start: false };
      const created = await api("/api/bugs", { method: "POST", body: JSON.stringify(payload) });
      createdBug = created.bug;
      await uploadAttachments(createdBug.id, files);
      if (start) await api(`/api/bugs/${encodeURIComponent(createdBug.id)}/start`, { method: "POST", body: "{}" });
      localStorage.setItem("niuma-reporter", payload.reporter);
      resetCreateForm(payload.reporter);
      await refreshBugs();
      showToast(start ? "Bug 已开始进入修复流水线" : "Bug 已保存，之后还可以修改");
    } catch (error) {
      createError.textContent = createdBug ? `Bug 已保存，但附件上传或启动失败：${error.message}` : error.message;
      if (createdBug) await refreshBugs();
    } finally {
      buttons.forEach((button) => button.disabled = false);
    }
  });

  bugList.addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action]");
    if (!button) return;
    const bug = state.bugs.find((item) => item.id === button.dataset.id);
    if (!bug) return;
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
      byId("editId").value = bug.id;
      byId("editTitle").value = bug.title;
      byId("editDescription").value = bug.description;
      byId("editClarification").value = bug.clarification || "";
      byId("editWorkspace").value = bug.workspace;
      byId("editAgent").value = bug.fix_agent;
      byId("editAttachments").value = "";
      byId("editAttachmentSelection").innerHTML = "";
      editError.textContent = "";
      editDialog.showModal();
      return;
    }
    if (button.dataset.action === "start") {
      if (!window.confirm(`确认开始修复“${bug.title}”？开始后处理期间不能再修改。`)) return;
      button.disabled = true;
      try {
        await api(`/api/bugs/${encodeURIComponent(bug.id)}/start`, { method: "POST", body: "{}" });
        await refreshBugs();
        showToast("已进入 Bug 修复流水线");
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
      const files = selectedAttachments(byId("editAttachments"));
      await api(`/api/bugs/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(bugPayload("edit")) });
      await uploadAttachments(id, files);
      editDialog.close();
      await refreshBugs();
      showToast("Bug 修改已同步到飞书");
    } catch (error) {
      editError.textContent = error.message;
    } finally {
      byId("saveEditButton").disabled = false;
    }
  });

  function showToast(message, isError = false) {
    toast.textContent = message;
    toast.style.background = isError ? "#b72f44" : "#211f2f";
    toast.hidden = false;
    clearTimeout(showToast.timer);
    showToast.timer = setTimeout(() => toast.hidden = true, 3000);
  }

  byId("refreshButton").addEventListener("click", refreshBugs);
  byId("bugAttachments").addEventListener("change", () => showSelectedAttachments(byId("bugAttachments"), byId("bugAttachmentSelection")));
  byId("editAttachments").addEventListener("change", () => showSelectedAttachments(byId("editAttachments"), byId("editAttachmentSelection")));
  byId("searchInput").addEventListener("input", render);
  byId("statusFilter").addEventListener("change", render);

  bootstrap().catch((error) => showToast(error.message, true));
  setInterval(() => {
    if (!document.hidden) refreshBugs();
  }, 10000);
  setInterval(() => {
    if (document.hidden) return;
    state.expandedPipelines.forEach((id) => refreshPipeline(id, true));
  }, 2000);
})();
