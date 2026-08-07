package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed web/index.html web/app.css web/app.js
var webAssets embed.FS

type bugRecordStore interface {
	listRecords() ([]Record, error)
	createRecord(fields map[string]any) (*Record, error)
	updateRecord(recordID string, fields map[string]any) error
}

type bugConsole struct {
	fs       bugRecordStore
	stateDir string
	fire     func()
	clearRun func(string)
}

type webBugImage struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	MIME string `json:"mime"`
}

type storedWebImage struct {
	File string `json:"file"`
	Name string `json:"name"`
	MIME string `json:"mime"`
}

const (
	webImageLogPrefix = "[web-image] "
	maxWebImages      = 5
	maxWebImageBytes  = 8 << 20
)

type webBug struct {
	ID            string        `json:"id"`
	Title         string        `json:"title"`
	Description   string        `json:"description"`
	Clarification string        `json:"clarification"`
	Status        string        `json:"status"`
	Workspace     string        `json:"workspace"`
	FixAgent      string        `json:"fix_agent"`
	ReviewAgent   string        `json:"review_agent"`
	Link          string        `json:"link"`
	Log           string        `json:"log"`
	Images        []webBugImage `json:"images"`
	Editable      bool          `json:"editable"`
	Startable     bool          `json:"startable"`
}

type webBugInput struct {
	Title         string `json:"title"`
	Description   string `json:"description"`
	Clarification string `json:"clarification"`
	Workspace     string `json:"workspace"`
	FixAgent      string `json:"fix_agent"`
	Reporter      string `json:"reporter"`
	Start         bool   `json:"start"`
}

func (a *App) serveWeb(fire func()) error {
	console := &bugConsole{
		fs:       a.fs,
		stateDir: cfg.StateDir,
		fire:     fire,
		clearRun: func(recordID string) {
			a.st.clear(recordID, "web restart")
		},
	}
	server := &http.Server{
		Addr:              cfg.WebAddr,
		Handler:           console.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logf("Web 控制台启动 · http://%s", displayWebAddr(cfg.WebAddr))
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func displayWebAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "<本机局域网IP>" + addr
	}
	return addr
}

func (c *bugConsole) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", c.serveIndex)
	mux.HandleFunc("/assets/app.css", c.serveCSS)
	mux.HandleFunc("/assets/app.js", c.serveJS)
	mux.HandleFunc("/api/meta", c.handleMeta)
	mux.HandleFunc("/api/bugs", c.handleBugs)
	mux.HandleFunc("/api/bugs/", c.handleBug)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func (c *bugConsole) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	c.serveAsset(w, "web/index.html", "text/html; charset=utf-8")
}

func (c *bugConsole) serveCSS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	c.serveAsset(w, "web/app.css", "text/css; charset=utf-8")
}

func (c *bugConsole) serveJS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	c.serveAsset(w, "web/app.js", "text/javascript; charset=utf-8")
}

func (c *bugConsole) serveAsset(w http.ResponseWriter, name, contentType string) {
	b, err := webAssets.ReadFile(name)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Write(b)
}

func (c *bugConsole) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		return
	}
	keys := workspaceKeys()
	sort.Strings(keys)
	defaultKey := ""
	if f := loadWorkspaces(); f != nil {
		defaultKey = f.Default
	}
	writeWebJSON(w, http.StatusOK, map[string]any{
		"workspaces":        keys,
		"default_workspace": defaultKey,
		"agents":            []string{"codex", "cursor"},
	})
}

func (c *bugConsole) handleBugs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c.listBugs(w)
	case http.MethodPost:
		c.createBug(w, r)
	default:
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
	}
}

func (c *bugConsole) handleBug(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/bugs/"), "/")
	if path == "" {
		writeWebError(w, http.StatusNotFound, "Bug 不存在")
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodPut {
		c.updateBug(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "start" && r.Method == http.MethodPost {
		c.startBug(w, id)
		return
	}
	if len(parts) == 2 && parts[1] == "pipeline" && r.Method == http.MethodGet {
		c.getBugPipeline(w, id)
		return
	}
	if len(parts) == 2 && parts[1] == "images" && r.Method == http.MethodPost {
		c.uploadBugImages(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "images" && r.Method == http.MethodGet {
		c.serveBugImage(w, id, parts[2])
		return
	}
	writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
}

func (c *bugConsole) getBugPipeline(w http.ResponseWriter, id string) {
	if !safeWebRecordID(id) {
		writeWebError(w, http.StatusBadRequest, "Bug ID 无效")
		return
	}
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"pipeline": webPipelineFromRecord(c.stateDir, rec)})
}

func (c *bugConsole) listBugs(w http.ResponseWriter) {
	records, err := c.fs.listRecords()
	if err != nil {
		writeWebError(w, http.StatusBadGateway, "读取飞书 Bug 失败："+err.Error())
		return
	}
	bugs := make([]webBug, 0, len(records))
	for i := range records {
		if isBugRecord(&records[i]) {
			bugs = append(bugs, webBugFromRecord(&records[i]))
		}
	}
	order := map[string]int{SBlocked: 0, SAnswer: 1, SSetup: 2, SBug: 3, SReview: 4, SMerge: 5, SDone: 6}
	sort.SliceStable(bugs, func(i, j int) bool {
		left, lok := order[bugs[i].Status]
		right, rok := order[bugs[j].Status]
		if !lok {
			left = 99
		}
		if !rok {
			right = 99
		}
		if left != right {
			return left < right
		}
		return bugs[i].ID > bugs[j].ID
	})
	writeWebJSON(w, http.StatusOK, map[string]any{"bugs": bugs})
}

func webBugFromRecord(rec *Record) webBug {
	status := fieldText(rec.Fields[FStatus])
	fix, review := resolveBugAgents(rec)
	return webBug{
		ID:            rec.RecordID,
		Title:         fieldText(rec.Fields[FTitle]),
		Description:   fieldText(rec.Fields[FDesc]),
		Clarification: fieldText(rec.Fields[FClarify]),
		Status:        status,
		Workspace:     fieldText(rec.Fields[FWorkspace]),
		FixAgent:      fix,
		ReviewAgent:   review,
		Link:          fieldText(rec.Fields[FLink]),
		Log:           tail(fieldText(rec.Fields[FLog]), 3000),
		Images:        webImagesFromRecord(rec),
		Editable:      webBugEditable(status),
		Startable:     webBugEditable(status),
	}
}

func webImagesFromRecord(rec *Record) []webBugImage {
	stored := storedWebImages(fieldText(rec.Fields[FLog]))
	images := make([]webBugImage, 0, len(stored))
	for _, image := range stored {
		images = append(images, webBugImage{
			Name: image.Name,
			URL:  "/api/bugs/" + rec.RecordID + "/images/" + image.File,
			MIME: image.MIME,
		})
	}
	return images
}

func storedWebImages(log string) []storedWebImage {
	var images []storedWebImage
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, webImageLogPrefix) {
			continue
		}
		var image storedWebImage
		if json.Unmarshal([]byte(strings.TrimPrefix(line, webImageLogPrefix)), &image) == nil && safeWebFileName(image.File) {
			images = append(images, image)
		}
	}
	return images
}

func (c *bugConsole) uploadBugImages(w http.ResponseWriter, r *http.Request, id string) {
	if !safeWebRecordID(id) {
		writeWebError(w, http.StatusBadRequest, "Bug ID 无效")
		return
	}
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	status := fieldText(rec.Fields[FStatus])
	if !webBugEditable(status) {
		writeWebError(w, http.StatusConflict, "当前状态「"+status+"」不能添加附件")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebImages*maxWebImageBytes+(1<<20))
	if err := r.ParseMultipartForm(maxWebImageBytes); err != nil {
		writeWebError(w, http.StatusBadRequest, "附件上传内容无效或过大")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	files := r.MultipartForm.File["images"]
	existing := storedWebImages(fieldText(rec.Fields[FLog]))
	if len(files) == 0 {
		writeWebError(w, http.StatusBadRequest, "请选择要上传的附件")
		return
	}
	if len(existing)+len(files) > maxWebImages {
		writeWebError(w, http.StatusBadRequest, "每个 Bug 最多上传 5 个附件")
		return
	}
	dir := filepath.Join(c.stateDir, "bug-images", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeWebError(w, http.StatusInternalServerError, "创建附件目录失败")
		return
	}
	var saved []string
	cleanup := func() {
		for _, path := range saved {
			_ = os.Remove(path)
		}
	}
	added := make([]storedWebImage, 0, len(files))
	for _, header := range files {
		if header.Size > maxWebImageBytes {
			cleanup()
			writeWebError(w, http.StatusBadRequest, "单个附件不能超过 8MB")
			return
		}
		file, err := header.Open()
		if err != nil {
			cleanup()
			writeWebError(w, http.StatusBadRequest, "读取上传附件失败")
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(file, maxWebImageBytes+1))
		file.Close()
		if readErr != nil || len(body) == 0 || len(body) > maxWebImageBytes {
			cleanup()
			writeWebError(w, http.StatusBadRequest, "附件为空或超过 8MB")
			return
		}
		ext, mimeType := webAttachmentType(header.Filename, body)
		if ext == "" {
			cleanup()
			writeWebError(w, http.StatusBadRequest, "仅支持图片、PDF、XLSX、XLS 或 CSV 文件")
			return
		}
		random := make([]byte, 10)
		if _, err := rand.Read(random); err != nil {
			cleanup()
			writeWebError(w, http.StatusInternalServerError, "生成附件文件名失败")
			return
		}
		storedName := hex.EncodeToString(random) + ext
		path := filepath.Join(dir, storedName)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			cleanup()
			writeWebError(w, http.StatusInternalServerError, "保存附件失败")
			return
		}
		saved = append(saved, path)
		name := singleLine(filepath.Base(header.Filename), 120)
		if name == "" {
			name = "Bug 附件"
		}
		added = append(added, storedWebImage{File: storedName, Name: name, MIME: mimeType})
	}
	logText := fieldText(rec.Fields[FLog])
	for _, image := range added {
		encoded, _ := json.Marshal(image)
		logText = strings.TrimSpace(logText+"\n"+webImageLogPrefix+string(encoded)) + "\n"
	}
	logText = strings.TrimSpace(logText+"\n[web] 已添加 "+itoa(len(added))+" 个附件") + "\n"
	if err := c.fs.updateRecord(id, map[string]any{FLog: logText}); err != nil {
		cleanup()
		writeWebError(w, http.StatusBadGateway, "关联 Bug 附件失败："+err.Error())
		return
	}
	rec.Fields[FLog] = logText
	writeWebJSON(w, http.StatusOK, map[string]any{"bug": webBugFromRecord(rec)})
}

func (c *bugConsole) serveBugImage(w http.ResponseWriter, id, fileName string) {
	if !safeWebRecordID(id) || !safeWebFileName(fileName) {
		http.NotFound(w, nil)
		return
	}
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, "附件不存在")
		return
	}
	var found *storedWebImage
	for _, image := range storedWebImages(fieldText(rec.Fields[FLog])) {
		if image.File == fileName {
			item := image
			found = &item
			break
		}
	}
	if found == nil {
		writeWebError(w, http.StatusNotFound, "附件不存在")
		return
	}
	file, err := os.Open(filepath.Join(c.stateDir, "bug-images", id, fileName))
	if err != nil {
		writeWebError(w, http.StatusNotFound, "附件不存在")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", found.MIME)
	disposition := "attachment"
	if strings.HasPrefix(found.MIME, "image/") || found.MIME == "application/pdf" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": found.Name}))
	io.Copy(w, file)
}

func webAttachmentType(fileName string, body []byte) (string, string) {
	if ext, mimeType := webImageType(body); ext != "" {
		return ext, mimeType
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".pdf":
		if http.DetectContentType(body) == "application/pdf" {
			return ".pdf", "application/pdf"
		}
	case ".xlsx":
		if isXLSX(body) {
			return ".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		}
	case ".xls":
		if bytes.HasPrefix(body, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
			return ".xls", "application/vnd.ms-excel"
		}
	case ".csv":
		if bytes.IndexByte(body, 0) < 0 {
			return ".csv", "text/csv"
		}
	}
	return "", ""
}

func isXLSX(body []byte) bool {
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return false
	}
	hasContentTypes := false
	hasWorkbook := false
	for _, file := range reader.File {
		switch file.Name {
		case "[Content_Types].xml":
			hasContentTypes = true
		case "xl/workbook.xml":
			hasWorkbook = true
		}
	}
	return hasContentTypes && hasWorkbook
}

func webImageType(body []byte) (string, string) {
	switch http.DetectContentType(body) {
	case "image/jpeg":
		return ".jpg", "image/jpeg"
	case "image/png":
		return ".png", "image/png"
	case "image/gif":
		return ".gif", "image/gif"
	case "image/webp":
		return ".webp", "image/webp"
	}
	if len(body) >= 12 && string(body[4:8]) == "ftyp" {
		brand := string(body[8:12])
		if strings.HasPrefix(brand, "hei") || brand == "mif1" || brand == "msf1" {
			return ".heic", "image/heic"
		}
	}
	return "", ""
}

func safeWebRecordID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func safeWebFileName(value string) bool {
	if value == "" || filepath.Base(value) != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' {
			return false
		}
	}
	return true
}

func webBugEditable(status string) bool {
	return status == SSetup || status == SAnswer || status == SBlocked
}

func (c *bugConsole) createBug(w http.ResponseWriter, r *http.Request) {
	input, ok := decodeWebBugInput(w, r)
	if !ok {
		return
	}
	if msg := validateWebBugInput(input); msg != "" {
		writeWebError(w, http.StatusBadRequest, msg)
		return
	}
	fix, review := bugAgentPair(input.FixAgent, cfg.EngineBugFix, cfg.EngineBugReview)
	status := SSetup
	if input.Start {
		status = SBug
	}
	logLine := "[web] Bug 已录入"
	if reporter := singleLine(input.Reporter, 60); reporter != "" {
		logLine += "，提交人=" + reporter
	}
	fields := map[string]any{
		FTitle:       singleLine(input.Title, 120),
		FDesc:        strings.TrimSpace(input.Description),
		FClarify:     strings.TrimSpace(input.Clarification),
		FStatus:      status,
		FTaskType:    TaskBug,
		FWorkspace:   input.Workspace,
		FAgentCode:   fix,
		FAgentReview: review,
		FLog:         logLine + "\n",
	}
	created, err := c.fs.createRecord(fields)
	if err != nil {
		writeWebError(w, http.StatusBadGateway, "创建飞书 Bug 失败："+err.Error())
		return
	}
	if input.Start && c.fire != nil {
		c.fire()
	}
	created.Fields = fields
	writeWebJSON(w, http.StatusCreated, map[string]any{"bug": webBugFromRecord(created)})
}

func (c *bugConsole) updateBug(w http.ResponseWriter, r *http.Request, id string) {
	input, ok := decodeWebBugInput(w, r)
	if !ok {
		return
	}
	if msg := validateWebBugInput(input); msg != "" {
		writeWebError(w, http.StatusBadRequest, msg)
		return
	}
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	status := fieldText(rec.Fields[FStatus])
	if !webBugEditable(status) {
		writeWebError(w, http.StatusConflict, "当前状态「"+status+"」正在处理或已经收尾，不能修改")
		return
	}
	fix, review := bugAgentPair(input.FixAgent, cfg.EngineBugFix, cfg.EngineBugReview)
	fields := map[string]any{
		FTitle:       singleLine(input.Title, 120),
		FDesc:        strings.TrimSpace(input.Description),
		FClarify:     strings.TrimSpace(input.Clarification),
		FWorkspace:   input.Workspace,
		FAgentCode:   fix,
		FAgentReview: review,
		FLog:         appendWebLog(rec, "[web] Bug 内容已修改"),
	}
	if err := c.fs.updateRecord(id, fields); err != nil {
		writeWebError(w, http.StatusBadGateway, "更新飞书 Bug 失败："+err.Error())
		return
	}
	for key, value := range fields {
		rec.Fields[key] = value
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"bug": webBugFromRecord(rec)})
}

func (c *bugConsole) startBug(w http.ResponseWriter, id string) {
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	status := fieldText(rec.Fields[FStatus])
	if !webBugEditable(status) {
		writeWebError(w, http.StatusConflict, "当前状态「"+status+"」不能开始修复")
		return
	}
	fields := map[string]any{
		FStatus: SBug,
		FFails:  0,
		FLog:    appendWebLog(rec, "[web] 已确认开始调查并修复"),
	}
	if err := c.fs.updateRecord(id, fields); err != nil {
		writeWebError(w, http.StatusBadGateway, "启动 Bug 修复失败："+err.Error())
		return
	}
	if c.clearRun != nil {
		c.clearRun(id)
	}
	if c.fire != nil {
		c.fire()
	}
	for key, value := range fields {
		rec.Fields[key] = value
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"bug": webBugFromRecord(rec)})
}

func (c *bugConsole) findBug(id string) (*Record, error) {
	records, err := c.fs.listRecords()
	if err != nil {
		return nil, errf("读取飞书 Bug 失败：%v", err)
	}
	rec := findByID(records, id)
	if rec == nil || !isBugRecord(rec) {
		return nil, errf("Bug 不存在")
	}
	return rec, nil
}

func validateWebBugInput(input webBugInput) string {
	if strings.TrimSpace(input.Title) == "" {
		return "请填写 Bug 标题"
	}
	if len([]rune(strings.TrimSpace(input.Title))) > 120 {
		return "Bug 标题不能超过 120 个字"
	}
	if strings.TrimSpace(input.Description) == "" {
		return "请填写复现现象和期望结果"
	}
	if len([]rune(strings.TrimSpace(input.Description))) > 12000 {
		return "Bug 描述不能超过 12000 个字"
	}
	if strings.TrimSpace(input.Workspace) == "" {
		return "请选择代码工作区"
	}
	if _, err := workspaceGet(input.Workspace); err != nil {
		return "工作区无效：" + err.Error()
	}
	if !validBugAgent(input.FixAgent) {
		return "修复 Agent 只能选择 codex 或 cursor"
	}
	return ""
}

func decodeWebBugInput(w http.ResponseWriter, r *http.Request) (webBugInput, bool) {
	var input webBugInput
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeWebError(w, http.StatusBadRequest, "请求内容无效")
		return webBugInput{}, false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeWebError(w, http.StatusBadRequest, "请求只能包含一个 JSON 对象")
		return webBugInput{}, false
	}
	return input, true
}

func appendWebLog(rec *Record, line string) string {
	return strings.TrimSpace(fieldText(rec.Fields[FLog])+"\n"+line) + "\n"
}

func singleLine(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func writeWebJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeWebError(w http.ResponseWriter, status int, message string) {
	writeWebJSON(w, status, map[string]string{"error": message})
}
