package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type AgentResult struct {
	OK           bool
	Output       string
	Duration     float64
	ArtifactsDir string
	SessionID    string
}

var baseErrorMarkers = []string{
	"invalid authentication", "failed to authenticate", "authentication failed",
	"not authenticated", "api error", "401", "rate limit", "quota",
}
var engineErrorMarkers = map[string][]string{
	"claude": {"claude code is not authenticated", "please run /login", "anthropic_auth_token"},
	"codex":  {"not logged in", "please login", "approval denied"},
	"gemini": {"please login", "google api key", "permission denied"},
	"cursor": {"cursor agent is not authenticated", "login required", "workspace is not trusted"},
}

// sink 累积 agent 输出：cursor 用 stream-json 事件，其余引擎用裸文本。
type sink interface {
	feed(tag, line string)
	progress() map[string]any
	finalText() string
	errorBlob() string
	isError() bool
}

type rawSink struct{ out, err []string }

func (s *rawSink) feed(tag, line string) {
	if tag == "err" {
		s.err = append(s.err, line)
	} else {
		s.out = append(s.out, line)
	}
}
func (s *rawSink) progress() map[string]any {
	n := 0
	for _, l := range s.out {
		n += len(l)
	}
	return map[string]any{"output_len": n}
}
func (s *rawSink) finalText() string { return strings.TrimSpace(strings.Join(s.out, "\n")) }
func (s *rawSink) errorBlob() string {
	return strings.Join(s.out, "\n") + "\n" + strings.Join(s.err, "\n")
}
func (s *rawSink) isError() bool { return false }

type cursorSink struct {
	resultText string
	hasResult  bool
	isErr      bool
	assistant  []string
	errLines   []string
	events     int
	thinking   int
	toolCalls  int
}

func (s *cursorSink) feed(tag, line string) {
	if tag == "err" {
		if t := strings.TrimSpace(line); t != "" {
			s.errLines = append(s.errLines, t)
		}
		return
	}
	t := strings.TrimSpace(line)
	if t == "" {
		return
	}
	if !strings.HasPrefix(t, "{") {
		s.assistant = append(s.assistant, line)
		return
	}
	var d map[string]any
	if json.Unmarshal([]byte(t), &d) != nil {
		s.assistant = append(s.assistant, line)
		return
	}
	s.events++
	switch d["type"] {
	case "thinking":
		s.thinking++
	case "tool_call":
		if d["subtype"] == "started" {
			s.toolCalls++
		}
	case "assistant":
		if msg, ok := d["message"].(map[string]any); ok {
			if content, ok := msg["content"].([]any); ok {
				for _, b := range content {
					if blk, ok := b.(map[string]any); ok && blk["type"] == "text" {
						if txt, _ := blk["text"].(string); strings.TrimSpace(txt) != "" {
							s.assistant = append(s.assistant, txt)
						}
					}
				}
			}
		}
	case "result":
		if rt, ok := d["result"].(string); ok {
			s.resultText = rt
		}
		s.hasResult = true
		s.isErr, _ = d["is_error"].(bool)
	}
}
func (s *cursorSink) progress() map[string]any {
	return map[string]any{"events": s.events, "thinking": s.thinking, "tool_calls": s.toolCalls}
}
func (s *cursorSink) finalText() string {
	if strings.TrimSpace(s.resultText) != "" {
		return s.resultText
	}
	return strings.TrimSpace(strings.Join(s.assistant, "\n"))
}
func (s *cursorSink) errorBlob() string { return strings.Join(s.errLines, "\n") }
func (s *cursorSink) isError() bool     { return s.hasResult && s.isErr }

func agentArgv(engine, sessionID string) []string {
	return agentArgvWithAccess(engine, sessionID, false)
}

func agentArgvWithAccess(engine, sessionID string, writeAccess bool) []string {
	base, ok := AgentCmds[engine]
	if !ok {
		return nil
	}
	base = append([]string{}, base...)
	if engine == "claude" && writeAccess {
		base = append(base, "--permission-mode", "acceptEdits")
	}
	if engine == "claude" && strings.TrimSpace(sessionID) != "" {
		return append(base, "--resume", strings.TrimSpace(sessionID))
	}
	if engine == "codex" && strings.TrimSpace(sessionID) != "" {
		return []string{base[0], "exec", "resume", strings.TrimSpace(sessionID), "-"}
	}
	// 尊重配置里的 --output-format：stream-json 走事件解析 + 活跃度看门狗；text 走裸文本。
	// 注意 cursor 的 composer-2.5（非 fast）只在 text 模式可用，stream-json 会被拒。
	return base
}

func newAgentSessionID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexID := hex.EncodeToString(raw[:])
	return hexID[:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:]
}

func hasStreamJSON(argv []string) bool {
	for _, a := range argv {
		if a == "stream-json" {
			return true
		}
	}
	return false
}

func scrubbedEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if ScrubEnvKeys[k] {
			continue
		}
		skip := false
		for _, p := range ScrubEnvPrefixes {
			if strings.HasPrefix(k, p) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

func validate(engine string, rc int, output string) AgentResult {
	if rc != 0 {
		return AgentResult{OK: false, Output: output}
	}
	if strings.TrimSpace(output) == "" {
		return AgentResult{OK: false, Output: engine + " 返回空输出"}
	}
	lower := strings.ToLower(output)
	markers := append(append([]string{}, baseErrorMarkers...), engineErrorMarkers[engine]...)
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return AgentResult{OK: false, Output: engine + " 输出疑似错误: " + trunc(output, 300)}
		}
	}
	return AgentResult{OK: true, Output: output}
}

type tagLine struct{ tag, line string }

// 瞬时错误（多为 cursor 的网络/TLS 抖动）——立即重试即可，不该判失败更不该计入熔断。
var transientRe = regexp.MustCompile(`(?i)aborted|socket disconnected|retriableerror|secure tls|econnreset|connection reset|broken pipe|bad gateway|service unavailable|temporarily unavailable|i/o timeout|\b50[234]\b`)
var codexSessionIDRe = regexp.MustCompile(`(?im)^session id:\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\s*$`)

func extractCodexSessionID(lines ...[]string) string {
	for _, group := range lines {
		match := codexSessionIDRe.FindStringSubmatch(strings.Join(group, "\n"))
		if len(match) == 2 {
			return strings.ToLower(match[1])
		}
	}
	return ""
}

// runAgent：在 runAgentOnce 外套一层"瞬时错误自动重试"。
// 网络/TLS 类报错立即重试 AgentRetries 次（短退避），把大多数 cursor 抖动悄悄吞掉。
func runAgent(engine, prompt, cwd string, timeout int, lg func(string), onProgress func(map[string]any)) AgentResult {
	return runAgentInSession(engine, prompt, cwd, timeout, "", lg, onProgress)
}

// runAgentInSession 允许 Claude/Codex 继续同一个明确的 CLI session；其他引擎保持原有一次性调用。
func runAgentInSession(engine, prompt, cwd string, timeout int, sessionID string, lg func(string), onProgress func(map[string]any)) AgentResult {
	return runAgentInSessionWithAccess(engine, prompt, cwd, timeout, sessionID, false, lg, onProgress)
}

// runAgentInSessionWithAccess 只在明确的写代码阶段为 Agent 开启非交互写权限。
// 调查、澄清和 Review 保持默认只读权限，避免全局绕过权限检查。
func runAgentInSessionWithAccess(engine, prompt, cwd string, timeout int, sessionID string, writeAccess bool, lg func(string), onProgress func(map[string]any)) AgentResult {
	tries := cfg.AgentRetries + 1
	if tries < 1 {
		tries = 1
	}
	var res AgentResult
	activeSessionID := strings.TrimSpace(sessionID)
	for try := 1; try <= tries; try++ {
		res = runAgentOnce(engine, prompt, cwd, timeout, activeSessionID, writeAccess, lg, onProgress)
		if res.SessionID != "" {
			activeSessionID = res.SessionID
		}
		if res.OK || !transientRe.MatchString(res.Output) {
			return res
		}
		if try < tries {
			backoff := time.Duration(3*try) * time.Second
			emit("dispatcher", "agent_transient_retry", map[string]any{"engine": engine, "try": try})
			if lg != nil {
				lg("  " + engine + " 瞬时错误(第" + itoa(try) + "次)，" + itoa(3*try) + "s 后重试：" + trunc(res.Output, 100))
			}
			time.Sleep(backoff)
		}
	}
	return res
}

// runAgentOnce 单次执行：Popen + 读取 goroutine + 看门狗。
// 总超时兜底；无输出超 Inactivity 即判卡死杀掉——但出过首行输出后才武装（沉默到底的引擎不误杀）。
// 按时间触发 onProgress 心跳。
func runAgentOnce(engine, prompt, cwd string, timeout int, sessionID string, writeAccess bool, lg func(string), onProgress func(map[string]any)) AgentResult {
	resolvedSessionID := strings.TrimSpace(sessionID)
	argv := agentArgvWithAccess(engine, resolvedSessionID, writeAccess)
	if engine == "claude" && resolvedSessionID == "" {
		resolvedSessionID = newAgentSessionID()
		if resolvedSessionID != "" {
			argv = append(argv, "--session-id", resolvedSessionID)
		}
	}
	if argv == nil {
		return AgentResult{OK: false, Output: "unknown agent: " + engine}
	}
	if exe, err := exec.LookPath(argv[0]); err == nil {
		argv[0] = exe
	}
	inactivity := cfg.Inactivity
	emit("dispatcher", "agent_start", map[string]any{"engine": engine, "cwd": cwd, "timeout": timeout, "inactivity": inactivity})
	if lg != nil {
		lg("  调用 " + engine + ": " + strings.Join(argv, " ") + " (timeout=" + itoa(timeout) + "s)…")
	}

	artDir := agentArtifactDir(engine)
	_ = os.MkdirAll(artDir, 0o755)
	_ = os.WriteFile(filepath.Join(artDir, "prompt.md"), []byte(prompt), 0o644)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = workspaceToolEnvForDir(scrubbedEnv(), cwd)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		emit("dispatcher", "agent_command_missing", map[string]any{"engine": engine, "command": argv[0]})
		if lg != nil {
			lg("  找不到命令 `" + argv[0] + "` —— 该引擎 CLI 没装或不在 PATH")
		}
		writeAgentArtifacts(artDir, engine, cwd, argv, -1, 0, nil, []string{err.Error()}, "command not found: "+argv[0], resolvedSessionID)
		return AgentResult{OK: false, Output: "command not found: " + argv[0] + "\n日志: " + artDir, ArtifactsDir: artDir, SessionID: resolvedSessionID}
	}
	go func() { io.WriteString(stdin, prompt); stdin.Close() }()

	lines := make(chan tagLine, 512)
	reader := func(r io.Reader, tag string, done chan<- struct{}) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for sc.Scan() {
			lines <- tagLine{tag, sc.Text()}
		}
		done <- struct{}{}
	}
	rdone := make(chan struct{}, 2)
	go reader(stdout, "out", rdone)
	go reader(stderr, "err", rdone)
	go func() { <-rdone; <-rdone; close(lines) }()

	var sk sink
	var outLines, errLines []string
	if hasStreamJSON(argv) {
		sk = &cursorSink{} // stream-json 事件解析
	} else {
		sk = &rawSink{} // 裸文本（cursor 用 composer-2.5/text 时走这）
	}
	start := time.Now()
	lastActivity := start
	seenOutput := false
	var lastProg float64
	killed := ""
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

loop:
	for {
		select {
		case ln, ok := <-lines:
			if !ok {
				break loop // stdout+stderr 都 EOF → 输出结束
			}
			lastActivity = time.Now()
			seenOutput = true
			if ln.tag == "err" {
				errLines = append(errLines, ln.line)
			} else {
				outLines = append(outLines, ln.line)
			}
			sk.feed(ln.tag, ln.line)
		case <-ticker.C:
			now := time.Now()
			if now.Sub(start).Seconds() > float64(timeout) {
				killed = "timeout"
				break loop
			}
			if seenOutput && inactivity > 0 && now.Sub(lastActivity).Seconds() > float64(inactivity) {
				killed = "inactivity"
				break loop
			}
			if onProgress != nil && now.Sub(start).Seconds()-lastProg >= 1.0 {
				lastProg = now.Sub(start).Seconds()
				p := sk.progress()
				p["elapsed"] = int(now.Sub(start).Seconds())
				onProgress(p)
			}
		}
	}
	duration := time.Since(start).Seconds()
	if engine == "codex" {
		if detected := extractCodexSessionID(errLines, outLines); detected != "" {
			resolvedSessionID = detected
		}
	}

	if killed != "" {
		_ = cmd.Process.Kill()
		go cmd.Wait()
		event := "agent_timeout"
		msg := engine + " timed out after " + itoa(timeout) + "s"
		if killed == "inactivity" {
			event = "agent_inactive_kill"
			msg = engine + " 无输出超过 " + itoa(inactivity) + "s（疑似卡死），已终止"
		}
		emit("dispatcher", event, map[string]any{"engine": engine, "timeout": timeout, "inactivity": inactivity, "duration": round1(duration)})
		if lg != nil {
			lg("  " + msg)
		}
		writeAgentArtifacts(artDir, engine, cwd, argv, -1, duration, outLines, errLines, msg, resolvedSessionID)
		return AgentResult{OK: false, Output: msg + "\n日志: " + artDir, Duration: duration, ArtifactsDir: artDir, SessionID: resolvedSessionID}
	}

	cmd.Wait()
	rc := cmd.ProcessState.ExitCode()
	text := sk.finalText()
	blob := text
	if blob == "" {
		blob = sk.errorBlob()
	}
	emit("dispatcher", "agent_done", map[string]any{"engine": engine, "returncode": rc, "duration": round1(duration), "output_len": len(blob)})
	if lg != nil {
		lg("  " + engine + " 退出码=" + itoa(rc) + "，耗时 " + itoa(int(duration)) + "s，输出 " + itoa(len(blob)) + " 字")
	}
	var res AgentResult
	if sk.isError() {
		res = AgentResult{OK: false, Output: engine + " 报错: " + trunc(blob, 300)}
	} else {
		res = validate(engine, rc, blob)
	}
	res.Duration = duration
	res.ArtifactsDir = artDir
	res.SessionID = resolvedSessionID
	writeAgentArtifacts(artDir, engine, cwd, argv, rc, duration, outLines, errLines, res.Output, resolvedSessionID)
	if !res.OK {
		res.Output = strings.TrimSpace(res.Output) + "\n日志: " + artDir
	}
	return res
}

func agentArtifactDir(engine string) string {
	name := time.Now().Format("20060102-150405") + "-" + itoa(int(time.Now().UnixNano()%1e9)) + "-" + engine
	return filepath.Join(stateDir(), "agent-runs", name)
}

func writeAgentArtifacts(dir, engine, cwd string, argv []string, rc int, duration float64, stdout, stderr []string, result, sessionID string) {
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(strings.Join(stdout, "\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stderr.log"), []byte(strings.Join(stderr, "\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "result.txt"), []byte(result), 0o644)
	meta := map[string]any{
		"engine":   engine,
		"cwd":      cwd,
		"argv":     argv,
		"return":   rc,
		"duration": round1(duration),
		"time":     time.Now().Format("2006-01-02 15:04:05 -0700"),
	}
	if strings.TrimSpace(sessionID) != "" {
		meta["session_id"] = strings.TrimSpace(sessionID)
	}
	if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644)
	}
	pruneAgentRuns()
}

// pruneAgentRuns 保留 state/agent-runs/ 下最新 N 个 run（按目录名时间戳排序），删除更旧的。
// N = PIPELINE_AGENT_RUNS_KEEP（默认 200）；≤0 表示不清理。
func pruneAgentRuns() {
	keep := 200
	if cfg != nil {
		keep = cfg.AgentRunsKeep
	}
	if keep <= 0 {
		return
	}
	base := filepath.Join(stateDir(), "agent-runs")
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= keep {
		return
	}
	sort.Strings(dirs) // 名字以 yyyymmdd-hhmmss 前缀，字典序即时间序
	for _, name := range dirs[:len(dirs)-keep] {
		_ = os.RemoveAll(filepath.Join(base, name))
	}
}
