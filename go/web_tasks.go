package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// webTask is the task-center view shared by the Bug and requirement tabs.
// The legacy /api/bugs API remains available for existing LAN clients.
type webTask struct {
	ID            string        `json:"id"`
	TaskType      string        `json:"task_type"`
	Title         string        `json:"title"`
	Description   string        `json:"description"`
	Clarification string        `json:"clarification"`
	PRD           string        `json:"prd"`
	Status        string        `json:"status"`
	Workspace     string        `json:"workspace"`
	ClarifyAgent  string        `json:"clarify_agent"`
	CodeAgent     string        `json:"code_agent"`
	FixAgent      string        `json:"fix_agent"`
	ReviewAgent   string        `json:"review_agent"`
	Link          string        `json:"link"`
	Log           string        `json:"log"`
	Attachments   []webBugImage `json:"attachments"`
	Editable      bool          `json:"editable"`
	Startable     bool          `json:"startable"`
	SyncState     string        `json:"sync_state"`
	SyncError     string        `json:"sync_error"`
	ExternalID    string        `json:"external_id"`
}

type webTaskInput struct {
	TaskType      string `json:"task_type"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Clarification string `json:"clarification"`
	PRD           string `json:"prd"`
	Workspace     string `json:"workspace"`
	ClarifyAgent  string `json:"clarify_agent"`
	CodeAgent     string `json:"code_agent"`
	FixAgent      string `json:"fix_agent"`
	ReviewAgent   string `json:"review_agent"`
	Reporter      string `json:"reporter"`
	Start         bool   `json:"start"`
}

func (c *bugConsole) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c.listTasks(w, r)
	case http.MethodPost:
		c.createTask(w, r)
	default:
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
	}
}

func (c *bugConsole) handleTask(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	parts := strings.Split(path, "/")
	if path == "" || len(parts) == 0 || !safeWebRecordID(parts[0]) {
		writeWebError(w, http.StatusNotFound, "任务不存在")
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodPut {
		c.updateTask(w, r, id)
		return
	}
	if len(parts) == 2 {
		switch {
		case parts[1] == "pipeline" && r.Method == http.MethodGet:
			c.getTaskPipeline(w, id)
		case (parts[1] == "attachments" || parts[1] == "images") && r.Method == http.MethodPost:
			c.uploadBugImages(w, r, id)
		case parts[1] == "start" && r.Method == http.MethodPost:
			c.startTask(w, id)
		case parts[1] == "confirm" && r.Method == http.MethodPost:
			c.confirmTask(w, id)
		case parts[1] == "develop" && r.Method == http.MethodPost:
			c.developTask(w, id)
		case parts[1] == "review" && r.Method == http.MethodPost:
			c.reviewTask(w, id)
		case parts[1] == "stop" && r.Method == http.MethodPost:
			c.stopTask(w, id)
		case parts[1] == "retry" && r.Method == http.MethodPost:
			c.retryTask(w, id)
		case parts[1] == "complete" && r.Method == http.MethodPost:
			c.completeTask(w, id)
		case parts[1] == "archive" && r.Method == http.MethodPost:
			c.archiveTask(w, id)
		default:
			writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		}
		return
	}
	if len(parts) == 3 && (parts[1] == "attachments" || parts[1] == "images") && r.Method == http.MethodGet {
		c.serveBugImage(w, id, parts[2])
		return
	}
	writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
}

func (c *bugConsole) listTasks(w http.ResponseWriter, r *http.Request) {
	records, err := c.fs.listRecords()
	if err != nil {
		writeWebError(w, http.StatusBadGateway, "读取任务失败："+err.Error())
		return
	}
	wantedType := strings.TrimSpace(r.URL.Query().Get("type"))
	tasks := make([]webTask, 0, len(records))
	for i := range records {
		taskType := webTaskType(&records[i])
		if wantedType != "" && !strings.EqualFold(wantedType, taskType) {
			continue
		}
		task := webTaskFromRecord(&records[i])
		if provider, ok := c.fs.(interface {
			recordMeta(string) (recordSyncMeta, error)
		}); ok {
			if meta, metaErr := provider.recordMeta(records[i].RecordID); metaErr == nil {
				task.SyncState, task.SyncError, task.ExternalID = meta.SyncState, meta.SyncError, meta.ExternalID
			}
		}
		tasks = append(tasks, task)
	}
	order := map[string]int{
		SBlocked: 0, SAnswer: 1, SConfirm: 2, SSetup: 3, SDevReady: 4,
		SClarify: 5, SDev: 6, SBug: 6, SReview: 7, SMerge: 8, SDone: 9,
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		left, lok := order[tasks[i].Status]
		right, rok := order[tasks[j].Status]
		if !lok {
			left = 99
		}
		if !rok {
			right = 99
		}
		if left != right {
			return left < right
		}
		return tasks[i].ID > tasks[j].ID
	})
	writeWebJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func webTaskType(rec *Record) string {
	if isBugRecord(rec) {
		return TaskBug
	}
	return TaskRequirement
}

func webTaskFromRecord(rec *Record) webTask {
	status := fieldText(rec.Fields[FStatus])
	taskType := webTaskType(rec)
	clarify := orDefault(fieldText(rec.Fields[FAgentClarify]), cfg.EngineClarify)
	code := orDefault(fieldText(rec.Fields[FAgentCode]), cfg.EngineCode)
	review := orDefault(fieldText(rec.Fields[FAgentReview]), cfg.EngineReview)
	fix := code
	if taskType == TaskBug {
		fix, review = resolveBugAgents(rec)
		code = fix
	}
	return webTask{
		ID:            rec.RecordID,
		TaskType:      taskType,
		Title:         fieldText(rec.Fields[FTitle]),
		Description:   fieldText(rec.Fields[FDesc]),
		Clarification: fieldText(rec.Fields[FClarify]),
		PRD:           fieldText(rec.Fields[FPRD]),
		Status:        status,
		Workspace:     fieldText(rec.Fields[FWorkspace]),
		ClarifyAgent:  clarify,
		CodeAgent:     code,
		FixAgent:      fix,
		ReviewAgent:   review,
		Link:          fieldText(rec.Fields[FLink]),
		Log:           tail(fieldText(rec.Fields[FLog]), 5000),
		Attachments:   webTaskAttachments(rec),
		Editable:      webTaskEditable(rec),
		Startable:     webTaskStartable(rec),
	}
}

func webTaskAttachments(rec *Record) []webBugImage {
	stored := storedWebImages(fieldText(rec.Fields[FLog]))
	attachments := make([]webBugImage, 0, len(stored))
	for _, item := range stored {
		attachments = append(attachments, webBugImage{
			Name: item.Name,
			URL:  "/api/tasks/" + rec.RecordID + "/attachments/" + item.File,
			MIME: item.MIME,
		})
	}
	return attachments
}

func webTaskEditable(rec *Record) bool {
	status := fieldText(rec.Fields[FStatus])
	if isBugRecord(rec) {
		return webBugEditable(status)
	}
	return status == SSetup || status == SAnswer || status == SConfirm || status == SBlocked
}

func webTaskStartable(rec *Record) bool {
	status := fieldText(rec.Fields[FStatus])
	return status == SSetup || status == SAnswer || status == SBlocked
}

func (c *bugConsole) createTask(w http.ResponseWriter, r *http.Request) {
	input, ok := decodeWebTaskInput(w, r)
	if !ok {
		return
	}
	if msg := validateWebTaskInput(input); msg != "" {
		writeWebError(w, http.StatusBadRequest, msg)
		return
	}
	fields := webTaskFields(input)
	status := SSetup
	if input.Start {
		if input.TaskType == TaskBug {
			status = SBug
		} else {
			status = SClarify
		}
	}
	fields[FStatus] = status
	logLine := "[web] " + input.TaskType + "已录入"
	if reporter := singleLine(input.Reporter, 60); reporter != "" {
		logLine += "，提交人=" + reporter
		fields[FOwner] = reporter
	}
	fields[FLog] = logLine + "\n"
	created, err := c.fs.createRecord(fields)
	if err != nil {
		writeWebError(w, http.StatusBadGateway, "创建任务失败："+err.Error())
		return
	}
	created.Fields = fields
	if input.Start && c.fire != nil {
		c.fire()
	}
	writeWebJSON(w, http.StatusCreated, map[string]any{"task": webTaskFromRecord(created)})
}

func (c *bugConsole) updateTask(w http.ResponseWriter, r *http.Request, id string) {
	input, ok := decodeWebTaskInput(w, r)
	if !ok {
		return
	}
	rec, err := c.findTask(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	input.TaskType = webTaskType(rec)
	if msg := validateWebTaskInput(input); msg != "" {
		writeWebError(w, http.StatusBadRequest, msg)
		return
	}
	if !webTaskEditable(rec) {
		writeWebError(w, http.StatusConflict, "当前状态「"+fieldText(rec.Fields[FStatus])+"」不能修改")
		return
	}
	fields := webTaskFields(input)
	fields[FLog] = appendWebLog(rec, "[web] "+input.TaskType+"内容已修改")
	if err := c.fs.updateRecord(id, fields); err != nil {
		writeWebError(w, http.StatusBadGateway, "更新任务失败："+err.Error())
		return
	}
	for key, value := range fields {
		rec.Fields[key] = value
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"task": webTaskFromRecord(rec)})
}

func webTaskFields(input webTaskInput) map[string]any {
	fields := map[string]any{
		FTitle:     singleLine(input.Title, 120),
		FDesc:      strings.TrimSpace(input.Description),
		FClarify:   strings.TrimSpace(input.Clarification),
		FPRD:       strings.TrimSpace(input.PRD),
		FTaskType:  input.TaskType,
		FWorkspace: input.Workspace,
	}
	if input.TaskType == TaskBug {
		fix, review := bugAgentPair(input.FixAgent, cfg.EngineBugFix, cfg.EngineBugReview)
		fields[FAgentCode], fields[FAgentReview] = fix, review
		return fields
	}
	fields[FAgentClarify] = webAgentOr(input.ClarifyAgent, cfg.EngineClarify)
	fields[FAgentCode] = webAgentOr(input.CodeAgent, cfg.EngineCode)
	fields[FAgentReview] = webAgentOr(input.ReviewAgent, cfg.EngineReview)
	return fields
}

func webAgentOr(value, fallback string) string {
	value = normalizeAgent(value)
	if _, ok := AgentCmds[value]; ok {
		return value
	}
	return normalizeAgent(fallback)
}

func validateWebTaskInput(input webTaskInput) string {
	if input.TaskType != TaskBug && input.TaskType != TaskRequirement {
		return "任务类型只能选择 Bug 或需求"
	}
	label := input.TaskType
	if strings.TrimSpace(input.Title) == "" {
		return "请填写" + label + "标题"
	}
	if len([]rune(strings.TrimSpace(input.Title))) > 120 {
		return label + "标题不能超过 120 个字"
	}
	if strings.TrimSpace(input.Description) == "" {
		return "请填写" + label + "描述"
	}
	if len([]rune(strings.TrimSpace(input.Description))) > 12000 {
		return label + "描述不能超过 12000 个字"
	}
	if strings.TrimSpace(input.Workspace) == "" {
		return "请选择代码工作区"
	}
	if _, err := workspaceGet(input.Workspace); err != nil {
		return "工作区无效：" + err.Error()
	}
	if input.TaskType == TaskBug {
		if !validBugAgent(input.FixAgent) {
			return "修复 Agent 只能选择 codex 或 cursor"
		}
		return ""
	}
	for label, value := range map[string]string{
		"澄清 Agent":     input.ClarifyAgent,
		"开发 Agent":     input.CodeAgent,
		"Review Agent": input.ReviewAgent,
	} {
		if _, ok := AgentCmds[normalizeAgent(value)]; !ok {
			return label + " 无效"
		}
	}
	return ""
}

func decodeWebTaskInput(w http.ResponseWriter, r *http.Request) (webTaskInput, bool) {
	var input webTaskInput
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeWebError(w, http.StatusBadRequest, "请求内容无效")
		return webTaskInput{}, false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeWebError(w, http.StatusBadRequest, "请求只能包含一个 JSON 对象")
		return webTaskInput{}, false
	}
	return input, true
}

func (c *bugConsole) startTask(w http.ResponseWriter, id string) {
	rec, err := c.findTask(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if !webTaskStartable(rec) {
		writeWebError(w, http.StatusConflict, "当前状态「"+fieldText(rec.Fields[FStatus])+"」不能开始")
		return
	}
	next := SClarify
	line := "[web] 已确认开始需求澄清"
	if isBugRecord(rec) {
		next = SBug
		line = "[web] 已确认开始调查并修复"
	}
	fields := map[string]any{FStatus: next, FFails: 0, FLog: appendWebLog(rec, line)}
	if err := c.fs.updateRecord(id, fields); err != nil {
		writeWebError(w, http.StatusBadGateway, "启动任务失败："+err.Error())
		return
	}
	if c.clearRun != nil {
		c.clearRun(id)
	}
	if c.fire != nil {
		c.fire()
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *bugConsole) confirmTask(w http.ResponseWriter, id string) {
	rec, err := c.findRequirement(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if fieldText(rec.Fields[FStatus]) != SConfirm {
		writeWebError(w, http.StatusConflict, "只有待确认需求可以确认")
		return
	}
	fields := map[string]any{FStatus: SDevReady, FLog: appendWebLog(rec, "[web] 需求已人工确认，进入待开发队列")}
	if err := c.fs.updateRecord(id, fields); err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *bugConsole) developTask(w http.ResponseWriter, id string) {
	rec, err := c.findRequirement(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if fieldText(rec.Fields[FStatus]) != SDevReady {
		writeWebError(w, http.StatusConflict, "只有待开发需求可以开始开发")
		return
	}
	fields := map[string]any{FStatus: SDev, FFails: 0, FLog: appendWebLog(rec, "[web] 已人工触发开始开发")}
	if err := c.fs.updateRecord(id, fields); err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	if c.clearRun != nil {
		c.clearRun(id)
	}
	if c.fire != nil {
		c.fire()
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *bugConsole) reviewTask(w http.ResponseWriter, id string) {
	rec, err := c.findRequirement(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if fieldText(rec.Fields[FStatus]) != SMerge {
		writeWebError(w, http.StatusConflict, "只有待合并需求可以发起 Review")
		return
	}
	fields := map[string]any{FStatus: SReview, FFails: 0, FLog: appendWebLog(rec, "[web] 已人工发起独立 Review")}
	if err := c.fs.updateRecord(id, fields); err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	if c.clearRun != nil {
		c.clearRun(id)
	}
	if c.fire != nil {
		c.fire()
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *bugConsole) stopTask(w http.ResponseWriter, id string) {
	rec, err := c.findTask(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isBugRecord(rec) {
		writeWebError(w, http.StatusConflict, "需求 Agent 暂不支持安全强制停止，请等待当前阶段结束")
		return
	}
	c.stopBug(w, id)
}

func (c *bugConsole) retryTask(w http.ResponseWriter, id string) {
	rec, err := c.findTask(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if c.app == nil {
		writeWebError(w, http.StatusServiceUnavailable, "任务管理不可用")
		return
	}
	res := c.app.restartClarify(rec)
	if !res.ok {
		writeWebError(w, http.StatusConflict, res.msg)
		return
	}
	if res.dispatch && c.fire != nil {
		c.fire()
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "message": res.msg})
}

func (c *bugConsole) completeTask(w http.ResponseWriter, id string) {
	rec, err := c.findTask(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if c.app == nil {
		writeWebError(w, http.StatusServiceUnavailable, "任务管理不可用")
		return
	}
	res := c.app.markDone(rec)
	if !res.ok {
		writeWebError(w, http.StatusConflict, res.msg)
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "message": res.msg})
}

func (c *bugConsole) archiveTask(w http.ResponseWriter, id string) {
	rec, err := c.findTask(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if Actionable[fieldText(rec.Fields[FStatus])] {
		writeWebError(w, http.StatusConflict, "任务正在运行，不能归档")
		return
	}
	archiver, ok := c.fs.(interface{ archiveRecord(string) error })
	if !ok {
		writeWebError(w, http.StatusServiceUnavailable, "当前数据源不支持归档")
		return
	}
	if c.app != nil {
		c.app.st.clear(id, "archived from web task center")
	}
	if err := archiver.archiveRecord(id); err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *bugConsole) findTask(id string) (*Record, error) {
	records, err := c.fs.listRecords()
	if err != nil {
		return nil, errf("读取任务失败：%v", err)
	}
	rec := findByID(records, id)
	if rec == nil {
		return nil, errf("任务不存在")
	}
	return rec, nil
}

func (c *bugConsole) findRequirement(id string) (*Record, error) {
	rec, err := c.findTask(id)
	if err != nil {
		return nil, err
	}
	if isBugRecord(rec) {
		return nil, errf("需求不存在")
	}
	return rec, nil
}

func (c *bugConsole) getTaskPipeline(w http.ResponseWriter, id string) {
	rec, err := c.findTask(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	pipeline := webRequirementPipeline(rec)
	if isBugRecord(rec) {
		pipeline = webPipelineFromRecord(c.stateDir, rec)
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"pipeline": pipeline})
}

func webRequirementPipeline(rec *Record) webBugPipeline {
	clarify := orDefault(fieldText(rec.Fields[FAgentClarify]), cfg.EngineClarify)
	code := orDefault(fieldText(rec.Fields[FAgentCode]), cfg.EngineCode)
	review := orDefault(fieldText(rec.Fields[FAgentReview]), cfg.EngineReview)
	steps := []webPipelineStep{
		{Stage: "intake", Label: "需求录入", State: "done", Actor: "Niuma"},
		{Stage: "clarify", Label: "AI 澄清", State: "pending", Actor: clarify},
		{Stage: "confirm", Label: "人工确认", State: "pending", Actor: "需求方"},
		{Stage: "develop", Label: "AI 开发", State: "pending", Actor: code},
		{Stage: "review", Label: "独立 Review", State: "pending", Actor: review},
		{Stage: "delivery", Label: "人工合并", State: "pending", Actor: "开发者"},
	}
	status := fieldText(rec.Fields[FStatus])
	logText := fieldText(rec.Fields[FLog])
	setDoneThrough := func(index int) {
		for i := 0; i <= index && i < len(steps); i++ {
			steps[i].State = "done"
		}
	}
	switch status {
	case SSetup:
		steps[1].State, steps[1].Detail = "waiting", "等待人工开始需求澄清"
	case SClarify:
		steps[1].State, steps[1].Detail = "running", "AI 正在梳理范围与验收标准"
	case SAnswer:
		steps[1].State, steps[1].Detail = "waiting", "AI 正在等待人工补充信息"
	case SConfirm:
		setDoneThrough(1)
		steps[2].State, steps[2].Detail = "waiting", "PRD 已生成，等待人工确认"
	case SDevReady:
		setDoneThrough(2)
		steps[3].State, steps[3].Detail = "waiting", "需求已确认，等待开始开发"
	case SDev:
		setDoneThrough(2)
		steps[3].State, steps[3].Detail = "running", "Agent 正在实现需求"
	case SReview:
		setDoneThrough(3)
		steps[4].State, steps[4].Detail = "running", "另一个 Agent 正在审查改动"
	case SMerge:
		setDoneThrough(3)
		if strings.Contains(logText, "[review:") {
			steps[4].State = "done"
		} else {
			steps[4].State, steps[4].Detail = "waiting", "可发起独立 Review，或由人工直接验收"
		}
		steps[5].State, steps[5].Detail = "waiting", "等待人工检查并合并"
	case SDone:
		setDoneThrough(5)
		if !strings.Contains(logText, "[review:") {
			steps[4].Detail = "本次由人工直接验收完成"
		}
	case SBlocked:
		for i := 1; i < len(steps); i++ {
			if steps[i].State == "pending" {
				steps[i].State, steps[i].Detail = "failed", tail(strings.TrimSpace(logText), 1200)
				break
			}
		}
	}
	serverTime := time.Now().Format(time.RFC3339)
	events := make([]bugProgressEvent, 0, 3)
	if clarification := strings.TrimSpace(fieldText(rec.Fields[FClarify])); clarification != "" {
		events = append(events, bugProgressEvent{Stage: "clarify", State: stepState(steps, "clarify"), Agent: clarify, Detail: clarification})
	}
	if prd := strings.TrimSpace(fieldText(rec.Fields[FPRD])); prd != "" {
		events = append(events, bugProgressEvent{Stage: "confirm", State: stepState(steps, "confirm"), Agent: clarify, Detail: prd})
	}
	if strings.TrimSpace(logText) != "" {
		events = append(events, bugProgressEvent{Stage: "pipeline", State: "done", Agent: "Niuma", Detail: tail(strings.TrimSpace(logText), 5000)})
	}
	return webBugPipeline{
		BugID: rec.RecordID, Status: status, Live: Actionable[status],
		ServerTime: serverTime, Steps: steps, Events: events,
	}
}

func stepState(steps []webPipelineStep, stage string) string {
	for _, step := range steps {
		if step.Stage == stage {
			return step.State
		}
	}
	return "pending"
}
