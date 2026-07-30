package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 飞书字段名（系统契约，必须与 Base 列名一致；默认与 Python 版相同）。
const (
	FTitle        = "需求标题"
	FStatus       = "状态"
	FDesc         = "需求描述"
	FClarify      = "澄清记录"
	FPRD          = "PRD"
	FLink         = "分支PR链接"
	FLog          = "执行日志"
	FFails        = "失败次数"
	FOwner        = "提需求人"
	FChat         = "会话ID"
	FWorkspace    = "工作区"
	FAgent        = "执行Agent"
	FAgentClarify = "澄清Agent"
	FAgentCode    = "开发Agent"
	FAgentReview  = "ReviewAgent"
	FTaskType     = "任务类型"
)

const (
	TaskRequirement = "需求"
	TaskBug         = "Bug"
)

// 状态值（单选选项）。
const (
	SSetup    = "待选择"
	SClarify  = "待澄清"
	SAnswer   = "待回答"
	SConfirm  = "待确认"
	SDevReady = "待开发" // 已确认、排队等整批开发的队列状态（不自动跑，等人点「开始开发本批」）
	SDev      = "开发中"
	SBug      = "Bug处理中"
	SReview   = "Review中"
	SMerge    = "待合并"
	SDone     = "完成"
	SBlocked  = "已阻塞"
)

// dispatcher 只处理这些状态；其余是人工卡点或终态。
var Actionable = map[string]bool{SClarify: true, SDev: true, SBug: true, SReview: true}

// 等待人在飞书里输入的状态。
var HumanInput = map[string]bool{SAnswer: true, SConfirm: true}

// dispatcher 允许自动推进的状态边。
var ValidTransitions = map[string]map[string]bool{
	SSetup:    {SClarify: true, SBug: true},
	SClarify:  {SAnswer: true, SConfirm: true, SBlocked: true},
	SConfirm:  {SDevReady: true, SBlocked: true},             // 确认 → 进「待开发」队列
	SDevReady: {SDev: true, SBlocked: true},                  // 人点「开始开发本批」→ 开发中
	SDev:      {SReview: true, SMerge: true, SBlocked: true}, // SMerge：inline 开发完停靠，待人决定 Review
	SBug:      {SAnswer: true, SMerge: true, SBlocked: true},
	SReview:   {SDev: true, SMerge: true, SBlocked: true},
	SMerge:    {SReview: true}, // 人点「做 Review」从待合并回到 Review
}

var IntakePrefixes = []string{"需求"}
var ConfirmWords = []string{"确认", "ok", "确定", "可以开发"}

// 各引擎 headless 命令模板，prompt 走 stdin。
var AgentCmds = map[string][]string{
	"claude": {"claude", "-p", "--output-format", "text"},
	"codex":  {"codex", "exec", "-"},
	"gemini": {"gemini", "--skip-trust", "--approval-mode", "yolo", "-p", " "},
	"cursor": {"cursor-agent", "--print", "--force", "--trust", "--model", "auto", "--output-format", "text"},
}

// 中文/大小写/别名 → AGENT_CMDS 的 key。
var AgentAliases = map[string]string{
	"claude": "claude", "claude code": "claude",
	"codex": "codex", "openai codex": "codex",
	"gemini": "gemini",
	"cursor": "cursor", "cursor-agent": "cursor", "cursor agent": "cursor",
}

// 启动 agent 子进程前剔除这些污染认证的环境变量。
var ScrubEnvKeys = map[string]bool{"ANTHROPIC_BASE_URL": true, "CLAUDECODE": true, "ANTHROPIC_AUTH_TOKEN": true}
var ScrubEnvPrefixes = []string{"CLAUDE_CODE_", "CLAUDE_PLUGIN", "AI_AGENT"}

// 验收门：从输出里数"错误行"。
var GateErrorRe = regexp.MustCompile(`(?i)\berror\b|✖|\bfailed\b`)
var CodeExts = []string{".ts", ".tsx", ".js", ".jsx", ".py", ".java", ".go", ".rb", ".rs", ".php", ".cs", ".kt", ".c", ".cpp", ".h"}

// Config 持有部署相关、可由 .env 覆盖的值。
type Config struct {
	AppID, AppSecret                        string
	BaseToken, TableID                      string
	RepoPath, BaseRef                       string
	GHRepo, TestCmd                         string
	EngineClarify, EngineCode, EngineReview string
	EngineBugFix, EngineBugReview           string

	TimeoutClarify, TimeoutCode, TimeoutReview          int
	TimeoutBug, BugRepairLimit                          int
	Inactivity, ProgressInterval, StaleAfter, RetryBase int
	FailureLimit, PollInterval, MaxConcurrency          int
	AgentRetries, AgentRunsKeep                         int
	SetupGate, GateRelative, PushEnabled, PREnabled     bool
	BatchDevelop, BatchClarify, InlineSkipGate          bool

	Root, StateDir, WorktreeBase, WorkspacesFile string
	BugGraphPython, BugGraphDir                  string
}

var cfg *Config

const dossierDir = ".pipeline"

func loadConfig() *Config {
	root := loadDotenv()
	c := &Config{
		AppID:     os.Getenv("FEISHU_APP_ID"),
		AppSecret: os.Getenv("FEISHU_APP_SECRET"),
		BaseToken: os.Getenv("PIPELINE_BASE_TOKEN"),
		TableID:   os.Getenv("PIPELINE_TABLE_ID"),
		RepoPath:  os.Getenv("PIPELINE_REPO_PATH"),
		BaseRef:   envText("PIPELINE_BASE_REF", "origin/main"),
		GHRepo:    os.Getenv("PIPELINE_GH_REPO"),
		TestCmd:   os.Getenv("PIPELINE_TEST_CMD"),

		EngineClarify:   envText("PIPELINE_ENGINE_CLARIFY", "cursor"),
		EngineCode:      envText("PIPELINE_ENGINE_CODE", "cursor"),
		EngineReview:    envText("PIPELINE_ENGINE_REVIEW", "gemini"),
		EngineBugFix:    envText("PIPELINE_ENGINE_BUG_FIX", "codex"),
		EngineBugReview: envText("PIPELINE_ENGINE_BUG_REVIEW", "cursor"),

		TimeoutClarify:   envInt("PIPELINE_TIMEOUT_CLARIFY", 600),
		TimeoutCode:      envInt("PIPELINE_TIMEOUT_CODE", 1800),
		TimeoutReview:    envInt("PIPELINE_TIMEOUT_REVIEW", 900),
		TimeoutBug:       envInt("PIPELINE_TIMEOUT_BUG", 10800),
		BugRepairLimit:   envInt("PIPELINE_BUG_REPAIR_LIMIT", 2),
		Inactivity:       envInt("PIPELINE_INACTIVITY_TIMEOUT", 120),
		ProgressInterval: envInt("PIPELINE_PROGRESS_INTERVAL", 20),
		StaleAfter:       envInt("PIPELINE_EXECUTION_STALE_AFTER", 600),
		RetryBase:        envInt("PIPELINE_RETRY_BASE_DELAY", 60),
		FailureLimit:     envInt("PIPELINE_FAILURE_LIMIT", 2),
		PollInterval:     envInt("PIPELINE_POLL_INTERVAL", 900),
		MaxConcurrency:   envInt("PIPELINE_MAX_CONCURRENCY", 2),
		AgentRetries:     envInt("PIPELINE_AGENT_RETRIES", 2),
		AgentRunsKeep:    envInt("PIPELINE_AGENT_RUNS_KEEP", 200),

		SetupGate:    envBool("PIPELINE_SETUP_GATE", true),
		GateRelative: envBool("PIPELINE_GATE_RELATIVE", true),
		PushEnabled:  envBool("PIPELINE_PUSH_ENABLED", false),
		PREnabled:    envBool("PIPELINE_PR_ENABLED", false),
		// inline 默认：多需求合批成一次 agent 调用，跳过自动测试门，开发完停在「待合并」由人决定 Review。
		BatchDevelop:   envBool("PIPELINE_BATCH_DEVELOP", true),
		BatchClarify:   envBool("PIPELINE_BATCH_CLARIFY", true),
		InlineSkipGate: envBool("PIPELINE_INLINE_SKIP_GATE", true),

		Root: root,
	}
	c.StateDir = envText("PIPELINE_STATE_DIR", filepath.Join(root, "state"))
	c.WorktreeBase = envText("PIPELINE_WORKTREE_BASE", filepath.Join(root, "worktrees"))
	c.WorkspacesFile = envText("PIPELINE_WORKSPACES_FILE", filepath.Join(root, "workspaces.json"))
	c.BugGraphPython = envText("PIPELINE_BUG_GRAPH_PYTHON", defaultBugGraphPython(root))
	c.BugGraphDir = envText("PIPELINE_BUG_GRAPH_DIR", filepath.Join(root, "buggraph"))
	if ce := os.Getenv("PIPELINE_CODE_EXTS"); strings.TrimSpace(ce) != "" {
		var exts []string
		for _, e := range strings.Split(ce, ",") {
			if e = strings.TrimSpace(e); e != "" {
				exts = append(exts, e)
			}
		}
		if len(exts) > 0 {
			CodeExts = exts
		}
	}
	return c
}

func defaultBugGraphPython(root string) string {
	for _, candidate := range []string{
		filepath.Join(root, "buggraph", ".venv", "bin", "python"),
		filepath.Join(root, "buggraph", ".venv", "Scripts", "python.exe"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "python3"
}

func (c *Config) agentTimeout() int {
	m := c.TimeoutClarify
	if c.TimeoutCode > m {
		m = c.TimeoutCode
	}
	if c.TimeoutReview > m {
		m = c.TimeoutReview
	}
	if c.TimeoutBug > m {
		m = c.TimeoutBug
	}
	return m
}

func (c *Config) validate() error {
	var missing []string
	if c.BaseToken == "" {
		missing = append(missing, "PIPELINE_BASE_TOKEN")
	}
	if c.TableID == "" {
		missing = append(missing, "PIPELINE_TABLE_ID")
	}
	if c.RepoPath == "" {
		missing = append(missing, "PIPELINE_REPO_PATH")
	}
	if len(missing) > 0 {
		return errf("缺少必填配置 %v：在 .env 设置（参考 .env.example）", missing)
	}
	return nil
}

func (c *Config) validateSchemaCommand() error {
	var missing []string
	for name, value := range map[string]string{
		"FEISHU_APP_ID": c.AppID, "FEISHU_APP_SECRET": c.AppSecret,
		"PIPELINE_BASE_TOKEN": c.BaseToken, "PIPELINE_TABLE_ID": c.TableID,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return errf("缺少 Base schema 配置 %v", missing)
	}
	return nil
}

// loadDotenv 在 NIUMA_ENV / ./.env / ../.env / ../../.env 里找 .env 灌进环境，
// 返回 .env 所在目录作为项目根（state/worktrees/workspaces 都锚在这）。
func loadDotenv() string {
	candidates := []string{}
	if p := os.Getenv("NIUMA_ENV"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, ".env", filepath.Join("..", ".env"), filepath.Join("..", "..", ".env"))
	for _, p := range candidates {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		f, err := os.Open(abs)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
				continue
			}
			kv := strings.SplitN(line, "=", 2)
			k := strings.TrimSpace(kv[0])
			if k != "" {
				if _, ok := os.LookupEnv(k); !ok {
					v := strings.TrimSpace(kv[1])
					v = strings.Trim(v, `"'`)
					os.Setenv(k, v)
				}
			}
		}
		f.Close()
		return filepath.Dir(abs)
	}
	wd, _ := os.Getwd()
	return wd
}

func envText(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(name string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// normalizeAgent 把别名归一到 AGENT_CMDS 的 key（找不到返回原值小写）。
func normalizeAgent(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if e, ok := AgentAliases[raw]; ok {
		return e
	}
	if e, ok := AgentAliases[strings.ToLower(raw)]; ok {
		return e
	}
	return strings.ToLower(raw)
}
