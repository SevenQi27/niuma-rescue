# Windows 安装与运行（Go 版）

Niuma 的 Go 控制面是单个 `niuma.exe`。普通需求和 Web 管理不需要 Python；Bug 调查/修复流程仍需部署 `buggraph/`、Python 3.10+ 和虚拟环境。

## 1. 构建

需要 Go 1.25.5+：

```cmd
cd agent-pipeline\go
set GOPROXY=https://goproxy.cn,direct
go build -o niuma.exe .
```

> 也可以在别的机器上交叉编译，拷贝单文件过去运行：
> ```bash
> GOOS=windows GOARCH=amd64 go build -o niuma.exe .
> ```

## 2. 配置 `.env`

放在仓库根（`go\` 的上一层），见 [`.env.example`](../.env.example)。纯本地模式只需配置目标仓库路径（例如 `PIPELINE_REPO_PATH=C:\Users\you\repo`），或提供有效的 `workspaces.json`。飞书凭据和 Base 标识都是可选项；字段和状态配置见 [feishu-app-setup.md](feishu-app-setup.md)。

前台验证一下：

```cmd
cd agent-pipeline\go
niuma.exe
```

打开 `http://localhost:8787` 检查任务中心和管理控制台。当前页面无鉴权，只能用于可信局域网。

如果要运行 Bug 流程，在仓库根执行：

```cmd
cd buggraph
py -3.10 -m venv .venv
.venv\Scripts\python.exe -m pip install -e .
.venv\Scripts\python.exe -m unittest discover -s tests -v
```

## 3. 后台常驻（避免桌面一直挂一个 cmd 黑窗）

`niuma.exe` 是控制台程序，直接双击/在 cmd 里跑会**一直留一个控制台窗口**。三种干净做法：

### 方案 A（推荐）：装成 Windows 服务（NSSM）

后台运行、无窗口、开机自启、崩溃自动重启——等价于 macOS 的 launchd / Linux 的 systemd。
下载 [NSSM](https://nssm.cc/)，管理员 cmd 执行：

```cmd
nssm install niuma "C:\path\agent-pipeline\go\niuma.exe"
nssm set niuma AppDirectory "C:\path\agent-pipeline\go"
nssm set niuma AppStdout "C:\path\agent-pipeline\logs\niuma.log"
nssm set niuma AppStderr "C:\path\agent-pipeline\logs\niuma.log"
nssm set niuma AppEnvironmentExtra "PATH=C:\path\to\agent-cli;%PATH%"
nssm start niuma
```

常用操作：

```cmd
nssm restart niuma     :: 重新 build 后重启
nssm stop niuma
nssm remove niuma confirm
```

> `AppEnvironmentExtra` 里的 PATH 必须包含 Agent CLI（如 `cursor-agent`）所在目录，
> 否则服务进程找不到命令。

### 方案 B：编译成无控制台窗口的程序

加 `-H=windowsgui`，生成的 exe **不弹任何窗口**：

```cmd
go build -ldflags="-H=windowsgui" -o niuma.exe .
```

再把它的快捷方式放进「启动」文件夹（`shell:startup`）即可开机自启。
代价：没有控制台输出，日志只能看文件——把 stdout/stderr 重定向到文件，或直接用方案 A。

### 方案 C：任务计划程序

「任务计划程序」→ 创建任务：

- 触发器：登录时
- 操作：启动程序 `C:\path\agent-pipeline\go\niuma.exe`，起始于 `...\go`
- 勾选「不管用户是否登录都要运行」+「隐藏」

也能无窗口后台运行。

## 注意事项

- **一个飞书 app 只能有一个长连接消费者**，别同时再跑别的 listener（旧 Python 版 / Hermes
  gateway 等）共用同一个 app，否则两边抢事件。
- Windows 上 Agent CLI 可能是 `.cmd` / `.exe`，确保它在服务进程的 `PATH` 里。
- 日志默认打到 stdout/stderr；用方案 A 时由 NSSM 落到 `logs\niuma.log`。
- 状态/锁存在 `state\niuma.sqlite3`；Agent 调用产物在 `state\agent-runs\`。
