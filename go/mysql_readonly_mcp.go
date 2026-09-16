package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxReadonlySQLBytes = 64 << 10
	maxMySQLOutputBytes = 512 << 10
)

var (
	readonlySQLStart = regexp.MustCompile(`(?i)^\s*(SELECT|SHOW|DESCRIBE|DESC|EXPLAIN|WITH)\b`)
	unsafeSQLToken   = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|REPLACE|MERGE|UPSERT|ALTER|CREATE|DROP|TRUNCATE|RENAME|GRANT|REVOKE|CALL|DO|HANDLER|LOAD|LOCK|UNLOCK|SET|START|BEGIN|COMMIT|ROLLBACK|SAVEPOINT|RELEASE|KILL|RESET|FLUSH|INSTALL|UNINSTALL|OPTIMIZE|REPAIR)\b`)
	unsafeSQLClause  = regexp.MustCompile(`(?i)\b(INTO\s+(OUTFILE|DUMPFILE)|FOR\s+UPDATE|LOCK\s+IN\s+SHARE\s+MODE|GET_LOCK\s*\(|RELEASE_LOCK\s*\(|SLEEP\s*\(|BENCHMARK\s*\(|LOAD_FILE\s*\()`)
	mysqlIdentifier  = regexp.MustCompile(`^[A-Za-z0-9_$]+$`)
)

type readonlyMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type readonlyMCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

func runReadonlyMySQLMCP(in io.Reader, out io.Writer) int {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var request readonlyMCPRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		result, rpcErr := handleReadonlyMCPRequest(request)
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		if rpcErr != nil {
			response["error"] = rpcErr
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return 1
		}
	}
	if err := scanner.Err(); err != nil {
		return 1
	}
	return 0
}

func handleReadonlyMCPRequest(request readonlyMCPRequest) (any, map[string]any) {
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		if params.ProtocolVersion == "" {
			params.ProtocolVersion = "2024-11-05"
		}
		return map[string]any{
			"protocolVersion": params.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "niuma-mysql-prod-readonly", "version": "1"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": readonlyMySQLTools()}, nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, mcpRPCError(-32602, "工具参数无效")
		}
		text, err := callReadonlyMySQLTool(params.Name, params.Arguments)
		content := []map[string]any{{"type": "text", "text": text}}
		return map[string]any{"content": content, "isError": err != nil}, nil
	default:
		return nil, mcpRPCError(-32601, "不支持的 MCP 方法")
	}
}

func mcpRPCError(code int, message string) map[string]any {
	return map[string]any{"code": code, "message": message}
}

func readonlyMySQLTools() []readonlyMCPTool {
	annotations := map[string]any{"title": "生产数据库只读查询", "readOnlyHint": true, "destructiveHint": false}
	return []readonlyMCPTool{
		{
			Name: "execute_sql", Description: "在生产 MySQL 的 READ ONLY 事务中执行一条只读 SQL；仅允许 SELECT、SHOW、DESCRIBE、EXPLAIN 和只读 WITH。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}},
			Annotations: annotations,
		},
		{
			Name: "get_schema_info", Description: "只读查询指定表的字段信息；表名可使用 database.table。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"table_name": map[string]any{"type": "string"}}, "required": []string{"table_name"}},
			Annotations: annotations,
		},
		{
			Name: "get_table_sample", Description: "只读抽样指定表，最多返回 20 行；表名可使用 database.table。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"table_name": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}}, "required": []string{"table_name"}},
			Annotations: annotations,
		},
	}
}

func callReadonlyMySQLTool(name string, arguments map[string]any) (string, error) {
	var query string
	switch name {
	case "execute_sql":
		query, _ = arguments["query"].(string)
	case "get_schema_info":
		database, table, err := parseReadonlyTableName(arguments["table_name"])
		if err != nil {
			return err.Error(), err
		}
		schema := "DATABASE()"
		if database != "" {
			schema = quoteSQLString(database)
		}
		query = "SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_COMMENT " +
			"FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = " + schema +
			" AND TABLE_NAME = " + quoteSQLString(table) + " ORDER BY ORDINAL_POSITION"
	case "get_table_sample":
		database, table, err := parseReadonlyTableName(arguments["table_name"])
		if err != nil {
			return err.Error(), err
		}
		limit := 5
		if raw, ok := arguments["limit"].(float64); ok {
			limit = int(raw)
		}
		if limit < 1 {
			limit = 1
		}
		if limit > 20 {
			limit = 20
		}
		tableRef := quoteMySQLIdentifier(table)
		if database != "" {
			tableRef = quoteMySQLIdentifier(database) + "." + tableRef
		}
		query = "SELECT * FROM " + tableRef + " LIMIT " + strconv.Itoa(limit)
	default:
		err := fmt.Errorf("不支持的只读数据库工具：%s", name)
		return err.Error(), err
	}
	query, err := validateReadonlySQL(query)
	if err != nil {
		return "已拒绝执行：" + err.Error(), err
	}
	result, err := executeReadonlyMySQL(query)
	if err != nil {
		return "生产只读查询失败：" + err.Error(), err
	}
	return result, nil
}

func validateReadonlySQL(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("SQL 不能为空")
	}
	if len(query) > maxReadonlySQLBytes {
		return "", errors.New("SQL 超过长度限制")
	}
	if strings.ContainsRune(query, 0) {
		return "", errors.New("SQL 包含非法字符")
	}
	query = strings.TrimSpace(strings.TrimSuffix(query, ";"))
	if strings.Contains(query, ";") {
		return "", errors.New("只允许执行一条 SQL")
	}
	if strings.Contains(query, "--") || strings.Contains(query, "/*") || strings.Contains(query, "*/") || strings.Contains(query, "#") {
		return "", errors.New("只读查询不允许 SQL 注释")
	}
	if !readonlySQLStart.MatchString(query) {
		return "", errors.New("只允许 SELECT、SHOW、DESCRIBE、EXPLAIN 或只读 WITH 查询")
	}
	scrubbed, err := scrubSQLLiterals(query)
	if err != nil {
		return "", err
	}
	if unsafeSQLToken.MatchString(scrubbed) || unsafeSQLClause.MatchString(scrubbed) {
		return "", errors.New("SQL 包含写入、锁定、文件访问或其他非只读操作")
	}
	return query, nil
}

func scrubSQLLiterals(query string) (string, error) {
	var result strings.Builder
	var quote byte
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if quote == 0 {
			if ch == '\'' || ch == '"' || ch == '`' {
				quote = ch
				result.WriteByte(' ')
			} else {
				result.WriteByte(ch)
			}
			continue
		}
		if ch == '\\' && quote != '`' {
			i++
			result.WriteString("  ")
			continue
		}
		if ch == quote {
			if i+1 < len(query) && query[i+1] == quote {
				i++
				result.WriteString("  ")
				continue
			}
			quote = 0
		}
		result.WriteByte(' ')
	}
	if quote != 0 {
		return "", errors.New("SQL 引号未闭合")
	}
	return result.String(), nil
}

func parseReadonlyTableName(value any) (string, string, error) {
	name, _ := value.(string)
	parts := strings.Split(strings.TrimSpace(name), ".")
	if len(parts) < 1 || len(parts) > 2 {
		return "", "", errors.New("表名格式无效")
	}
	for _, part := range parts {
		if !mysqlIdentifier.MatchString(part) {
			return "", "", errors.New("表名只能包含字母、数字、下划线或美元符号")
		}
	}
	if len(parts) == 1 {
		return "", parts[0], nil
	}
	return parts[0], parts[1], nil
}

func quoteMySQLIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func quoteSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func executeReadonlyMySQL(query string) (string, error) {
	host, user, password := os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD")
	port, database := os.Getenv("MYSQL_PORT"), os.Getenv("MYSQL_DATABASE")
	if host == "" || user == "" || password == "" {
		return "", errors.New("生产数据库连接配置不完整")
	}
	if port == "" {
		port = "3306"
	}
	mysql, err := resolveMySQLClient()
	if err != nil {
		return "", err
	}
	args := []string{"--batch", "--raw", "--default-character-set=utf8mb4", "--connect-timeout=10", "--host", host, "--port", port, "--user", user}
	if database != "" {
		args = append(args, "--database", database)
	}
	args = append(args, "--execute", "START TRANSACTION READ ONLY;\n"+query+";\nROLLBACK;")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, mysql, args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	stdout := &limitedBuffer{limit: maxMySQLOutputBytes}
	stderr := &limitedBuffer{limit: 32 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("查询超过 60 秒，已终止")
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	result := strings.TrimSpace(stdout.String())
	if result == "" {
		result = "查询成功，无结果"
	}
	if stdout.truncated {
		result += "\n\n[结果超过 512 KiB，已截断]"
	}
	return result, nil
}

func resolveMySQLClient() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("MYSQL_CLIENT_PATH")); configured != "" {
		if isExecutableFile(configured) {
			return configured, nil
		}
		return "", fmt.Errorf("MYSQL_CLIENT_PATH 指向的 mysql 客户端不可执行：%s", configured)
	}
	if found, err := exec.LookPath("mysql"); err == nil {
		return found, nil
	}
	for _, candidate := range []string{
		"/opt/homebrew/opt/mysql-client/bin/mysql",
		"/usr/local/opt/mysql-client/bin/mysql",
		"/opt/homebrew/bin/mysql",
		"/usr/local/bin/mysql",
		"/usr/bin/mysql",
	} {
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("未找到 mysql 客户端；可通过 MYSQL_CLIENT_PATH 显式配置")
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}
