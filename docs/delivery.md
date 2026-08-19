# 交付与发布

当前仓库没有旧版 Python `tools/package_release.py` 打包链路。交付以 Git 提交为基线，可选附带预编译的 Go 二进制；Bug 流程还必须交付 `buggraph/` 并在目标机器安装 Python 依赖。

## 建议交付内容

```text
README.md
LICENSE
.env.example
workspaces.example.json
go/                  Go 控制面源码
buggraph/            Bug LangGraph sidecar
docs/                配置、运维和页面截图
```

可选二进制：

```text
niuma-darwin-arm64
niuma-linux-amd64
niuma-windows-amd64.exe
```

## 不应交付

```text
.env
workspaces.json
state/
logs/
worktrees/
buggraph/.venv/
go/niuma
.DS_Store
真实任务附件
连接器 Token 或 Agent 凭据
```

`workspaces.json` 通常包含本机绝对路径和工具链位置，也不应作为通用发行配置提交。

## 构建

当前平台：

```bash
cd go
go build -trimpath -o niuma .
```

交叉编译：

```bash
cd go
GOOS=linux GOARCH=amd64 go build -trimpath -o ../dist/niuma-linux-amd64 .
GOOS=darwin GOARCH=arm64 go build -trimpath -o ../dist/niuma-darwin-arm64 .
GOOS=windows GOARCH=amd64 go build -trimpath -o ../dist/niuma-windows-amd64.exe .
```

Go 二进制包含 Web 静态资源，但不包含 Python BugGraph 包和虚拟环境。

## 发布前验证

```bash
cd go
go test ./...
go build ./...

cd ../buggraph
.venv/bin/python -m unittest discover -s tests -v
```

然后使用临时 `PIPELINE_STATE_DIR`、关闭飞书，在非生产目标仓库验证：

1. 页面可打开且没有控制台错误；
2. Bug 和需求可以保存但不会自动跳过人工门；
3. 管理控制台可以读取工作区和 Agent 设置；
4. 任务 worktree 分支不跟踪 `main`；
5. 共享会话可以并行创建任务、串行集成并安全冻结；
6. 集成冲突只影响当前任务，会话 worktree 保持干净；
7. `test_cmd` 真实执行并保留输出；
8. 所有截图和文档不包含真实任务、账号、Token 或本机私人路径。

## 升级

1. 备份当前 Git 提交、`.env`、`workspaces.json` 和停止后的 `state/`；
2. 拉取或切换到明确版本；
3. 重新构建 Go 二进制；
4. 如 `buggraph/pyproject.toml` 改动，更新虚拟环境；
5. 重启服务；
6. 检查启动日志、页面、数据库迁移和一条低风险任务。

不要在仍有任务执行时替换二进制或运行时数据库。

## 交付边界

- Niuma 可以创建任务/会话分支、按配置 push 或创建 PR/MR；
- Niuma 不负责把会话自动合并到最终产品分支；
- `delivery_target: user_choose` 表示最终目标完全由用户决定；
- “Agent 返回成功”不等于业务验收，通过真实测试和人工检查后才能交付。
