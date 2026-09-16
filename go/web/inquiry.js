(() => {
  const procTokenPattern = /^procId=([0-9]{1,64})$/;
  const storageKey = "niuma-inquiry-job";
  const byId = (id) => document.getElementById(id);
  const form = byId("inquiryForm");
  const procToken = byId("procToken");
  const workspace = byId("workspace");
  const question = byId("question");
  const submitButton = byId("submitButton");
  const formError = byId("formError");
  let pollTimer = 0;
	let activeJobID = "";

  async function api(path, options = {}) {
    const headers = { ...(options.headers || {}) };
    if (options.body) headers["Content-Type"] = "application/json";
    const response = await fetch(path, { ...options, headers });
    let data = {};
    try { data = await response.json(); } catch (_) { /* empty response */ }
    if (!response.ok) throw new Error(data.error || `请求失败（${response.status}）`);
    return data;
  }

  function parseProcID() {
    const match = procToken.value.match(procTokenPattern);
    return match ? match[1] : "";
  }

  function validateForm() {
    const allowed = Boolean(parseProcID());
    byId("gateState").classList.toggle("is-valid", allowed);
    byId("gateState").querySelector("span").innerHTML = allowed
      ? `已识别唯一流程实例 <code>procId=${parseProcID()}</code>`
      : "必须完整填写 <code>procId=纯数字</code>";
    byId("questionCount").textContent = `${question.value.length} / 12000`;
    submitButton.disabled = !(allowed && workspace.value && question.value.trim());
  }

  async function bootstrap() {
    const params = new URLSearchParams(window.location.search);
    const initialProcID = params.get("procId") || "";
    if (/^[0-9]{1,64}$/.test(initialProcID)) procToken.value = `procId=${initialProcID}`;
    try {
      const meta = await api("/api/meta");
      workspace.innerHTML = (meta.workspaces || []).map((key) => `<option value="${escapeHTML(key)}">${escapeHTML(key)}</option>`).join("");
      if (meta.default_workspace) workspace.value = meta.default_workspace;
    } catch (error) {
      workspace.innerHTML = '<option value="">工作区读取失败</option>';
      formError.textContent = error.message;
    }
    validateForm();
    await loadHistory();
    const existingJob = sessionStorage.getItem(storageKey);
    if (existingJob) pollJob(existingJob);
  }

  function escapeHTML(value = "") {
    return String(value).replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
  }

  function showJob(job) {
    const statusNames = { queued: "排队中", running: "查询中", completed: "已完成", failed: "查询失败" };
    const status = statusNames[job.status] || job.status;
    byId("jobStatus").textContent = status;
    byId("jobStatus").className = `status-badge is-${job.status}`;
    byId("answerEmpty").hidden = true;
    const finished = job.status === "completed" || job.status === "failed";
    byId("answerRunning").hidden = finished;
    byId("answerResult").hidden = !finished;
    renderProgress(job);
    if (!finished) {
      const progress = job.progress || {};
      byId("runningTitle").textContent = job.status === "running" ? (progress.phase || "Codex 正在查询") : "已进入查询队列";
      const elapsed = Number(progress.elapsed || 0);
      byId("runningMeta").textContent = `procId=${job.proc_id} · ${job.workspace}${elapsed ? ` · ${elapsed} 秒` : ""}`;
      return;
    }
    const duration = job.duration ? ` · ${Math.round(job.duration)} 秒` : "";
    byId("resultMeta").textContent = `procId=${job.proc_id} · ${job.workspace} · Codex${duration}`;
    byId("resultOutput").textContent = job.status === "completed" ? job.output : job.error;
  }

	function formatTime(value) {
		if (!value) return "";
		const date = new Date(value);
		return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat("zh-CN", {
			month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit",
		}).format(date);
	}

	async function loadHistory() {
		const list = byId("historyList");
		try {
			const data = await api("/api/inquiries");
			const inquiries = data.inquiries || [];
			byId("historyCount").textContent = inquiries.length ? `保留最近 ${inquiries.length} 条` : "";
			if (!inquiries.length) {
				list.innerHTML = '<p class="history-empty">暂无问询记录</p>';
				return;
			}
			list.replaceChildren(...inquiries.map((item) => {
				const button = document.createElement("button");
				button.type = "button";
				button.className = `history-item${item.id === activeJobID ? " is-active" : ""}`;
				const proc = document.createElement("span");
				proc.className = "history-proc";
				proc.textContent = `procId=${item.proc_id}`;
				const summary = document.createElement("strong");
				summary.className = "history-question";
				summary.textContent = item.question;
				const meta = document.createElement("span");
				meta.className = "history-meta";
				const status = document.createElement("i");
				status.className = `history-status is-${item.status}`;
				status.textContent = ({ queued: "排队中", running: "查询中", completed: "已完成", failed: "失败" })[item.status] || item.status;
				const detail = document.createElement("span");
				const duration = item.duration ? ` · ${Math.round(item.duration)} 秒` : "";
				detail.textContent = `${item.workspace} · ${formatTime(item.created_at)}${duration}`;
				meta.append(status, detail);
				button.append(proc, summary, meta);
				button.addEventListener("click", () => {
					if (item.status === "queued" || item.status === "running") sessionStorage.setItem(storageKey, item.id);
					else sessionStorage.removeItem(storageKey);
					pollJob(item.id);
				});
				return button;
			}));
		} catch (error) {
			list.innerHTML = `<p class="history-empty">${escapeHTML(error.message)}</p>`;
		}
	}

  function renderProgress(job) {
    const progress = job.progress || {};
    const events = Array.isArray(progress.timeline) ? progress.timeline : [];
    const panel = byId("activityPanel");
    panel.hidden = events.length === 0;
    const elapsed = Number(progress.elapsed || 0);
    const usage = Number(progress.input_tokens || 0) + Number(progress.output_tokens || 0);
    byId("activityMeta").textContent = [elapsed ? `${elapsed} 秒` : "", usage ? `${usage} tokens` : ""].filter(Boolean).join(" · ");
    const list = byId("activityList");
		const scrollTop = list.scrollTop;
    list.replaceChildren(...events.map((event) => {
      const item = document.createElement("li");
      item.className = `activity-item is-${event.status || "running"}`;
      const marker = document.createElement("i");
      marker.textContent = ({ analysis: "✦", command: ">_", tool: "◇", search: "⌕", plan: "☷", message: "●", error: "!", file: "!" })[event.kind] || "·";
      const content = document.createElement("div");
      const title = document.createElement("b");
      title.textContent = event.title || "执行事件";
      content.appendChild(title);
      if (event.detail) {
        const detail = document.createElement("pre");
        detail.textContent = event.detail;
        content.appendChild(detail);
      }
      item.append(marker, content);
      return item;
    }));
		list.scrollTop = scrollTop;
  }

  async function pollJob(id) {
    window.clearTimeout(pollTimer);
		activeJobID = id;
    try {
      const data = await api(`/api/inquiries/${encodeURIComponent(id)}`);
      const job = data.job;
      showJob(job);
      if (job.status === "queued" || job.status === "running") {
        pollTimer = window.setTimeout(() => pollJob(id), 1500);
      } else {
        sessionStorage.removeItem(storageKey);
        submitButton.disabled = false;
        validateForm();
				loadHistory();
      }
    } catch (error) {
      sessionStorage.removeItem(storageKey);
      byId("answerEmpty").hidden = true;
      byId("answerRunning").hidden = true;
      byId("answerResult").hidden = false;
      byId("activityPanel").hidden = true;
      byId("jobStatus").textContent = "读取失败";
      byId("jobStatus").className = "status-badge is-failed";
      byId("resultMeta").textContent = "无法读取问询任务";
      byId("resultOutput").textContent = error.message;
      validateForm();
    }
  }

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    formError.textContent = "";
    const procID = parseProcID();
    if (!procID) {
      formError.textContent = "必须按 procId=纯数字 的格式填写流程实例凭证。";
      return;
    }
    submitButton.disabled = true;
    try {
      const data = await api(`/api/inquiries?procId=${encodeURIComponent(procID)}`, {
        method: "POST",
        body: JSON.stringify({ question: question.value.trim(), workspace: workspace.value }),
      });
      sessionStorage.setItem(storageKey, data.job.id);
      showJob(data.job);
			loadHistory();
      pollJob(data.job.id);
    } catch (error) {
      formError.textContent = error.message;
      validateForm();
    }
  });

  procToken.addEventListener("input", validateForm);
  workspace.addEventListener("change", validateForm);
  question.addEventListener("input", validateForm);
	byId("refreshHistory").addEventListener("click", loadHistory);
  byId("copyButton").addEventListener("click", async () => {
    await navigator.clipboard.writeText(byId("resultOutput").textContent || "");
    byId("copyButton").textContent = "已复制";
    window.setTimeout(() => { byId("copyButton").textContent = "复制结果"; }, 1200);
  });

  bootstrap();
})();
