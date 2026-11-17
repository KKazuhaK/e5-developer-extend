# 设计：后台服务、健康检查、告警与引导

范围：
- `internal/service`：三个平台的后台服务安装、卸载和状态查询
- `internal/sdnotify`：systemd 的就绪和看门狗通知
- `internal/doctor`：健康检查
- `internal/notify`：告警
- CLI：`service`、`doctor`、`notify`、`init`
- 用户文档 `docs/user/`
- `Dockerfile`

依赖前两个阶段（[auth-and-config.md](auth-and-config.md)、[engine.md](engine.md)）。GUI 不在本阶段范围内。

## 1. 后台服务 `internal/service`

统一接口：

```go
type Manager interface {
    Install(opts InstallOptions) (Result, error)   // 写配置文件，注册，启用并启动
    Uninstall() (Result, error)                    // 停止，注销，删除文件
    Status() (Status, error)                       // installed / enabled / running，外加平台细节
}
```

- 服务文件内容（unit、plist、任务 XML）都由**纯函数**生成，用 golden 文件测试。
- 外部命令（`systemctl`、`launchctl`、`schtasks`、`loginctl`）通过可替换的 `Runner` 执行，测试时用假的 Runner 只记录命令。**测试里绝不调用真实的系统命令。**
- 可执行文件路径取 `os.Executable()` 并解析符号链接。如果路径看起来是临时的（`go run` 的构建缓存、`/tmp`），拒绝安装并说明原因。
- `Result` 里列出做了哪些操作、写了哪些文件、还需要用户手动做什么（例如需要 sudo 的命令）。CLI 原样打印。

### 1.1 Linux：systemd 用户服务

`~/.config/systemd/user/e5extend.service`：

```ini
[Unit]
Description=e5extend - keep a Microsoft 365 E5 developer tenant active
# 用户级 systemd 里没有 network-online.target，所以不写 After/Wants；断网时引擎自己重试

[Service]
Type=notify
ExecStart=<绝对路径> run --log-format json
Restart=on-failure
RestartSec=30
WatchdogSec=120
TimeoutStopSec=30
# 只在使用 --key-file 时加：
LoadCredential=e5extend-master-key:<ConfigDir>/master.key
Environment=E5EXTEND_CONFIG_DIR=<ConfigDir>
Environment=E5EXTEND_DATA_DIR=<DataDir>

[Install]
WantedBy=default.target
```

- 安装：写文件，然后执行 `systemctl --user daemon-reload`、`systemctl --user enable --now e5extend.service`；重新安装时再 `systemctl --user restart e5extend.service`。
- **linger**：用 `loginctl show-user $USER -p Linger --value` 检查。如果是 `no`：
  - 带 `--enable-linger` 参数时执行 `loginctl enable-linger $USER`（大多数发行版允许用户自己开）；失败的话给出 sudo 命令。
  - 不带这个参数时，在结果里**醒目提示**：不开 linger 的话，用户退出登录后服务会被停掉。
- **主密钥**：systemd 用户服务在无桌面的服务器上通常访问不到 Secret Service。
  - `--key-file`：把当前主密钥写到 `ConfigDir/master.key`（权限 0600），并在 unit 里加上 `LoadCredential`。
  - 没指定时：如果当前主密钥来自钥匙串，而且没有 `DISPLAY` 和 `WAYLAND_DISPLAY`（看起来是服务器），就提示加 `--key-file`，并说明安全上的取舍：密钥文件只靠文件权限保护。
- `--system` 模式这次不实现：只打印一份系统级 unit 示例和安装说明。

### 1.2 Linux：sd_notify `internal/sdnotify`

- 纯 Go 实现：如果有 `NOTIFY_SOCKET` 环境变量，就向这个 unixgram socket 发消息（支持抽象命名空间 `@` 前缀）；没有就什么都不做。
- 引擎启动并拿到锁后发 `READY=1`；每次 tick 发 `WATCHDOG=1`；退出前发 `STOPPING=1`。
- 引擎通过一个可选的回调接收这些钩子，避免引擎直接依赖平台代码。非 Linux 平台是空实现。

### 1.3 macOS：LaunchAgent

`~/Library/LaunchAgents/io.github.kkazuhak.e5extend.plist`：
- `ProgramArguments`：`[<路径>, "run", "--log-format", "json"]`
- `RunAtLoad`：true
- `KeepAlive`：`{SuccessfulExit: false}`
- `ThrottleInterval`：30
- `ProcessType`：`Standard`（不用 `Background`：Background 会让定时器被系统节流）
- `StandardOutPath` / `StandardErrorPath`：`~/Library/Logs/e5extend/engine.log`

安装命令：`launchctl enable gui/<uid>/io.github.kkazuhak.e5extend`，然后 `launchctl bootstrap gui/<uid> <plist>`（被禁用的 label 不能 bootstrap，所以 enable 在前）。
已经装过的话，先 `bootout` 再 `bootstrap`。用户登录会话里可以访问钥匙串，不需要密钥文件。

### 1.4 Windows：登录时运行的计划任务

**不用 Windows 服务作为默认方式**：服务以 LocalSystem 运行时读不到用户的凭据管理器和用户目录，需要整套单独的数据位置，等以后有需要再做（作为 `--windows-service` 的 TODO 记在文档里）。

计划任务通过 XML 导入：`schtasks /Create /TN e5extend /XML <file> /F`。XML 关键设置：
- 触发器：当前用户登录时（`LogonTrigger`，限定 `UserId`）
- `RunLevel`：`LeastPrivilege`
- **`ExecutionTimeLimit`：`PT0S`**（默认是 72 小时，到时间会被系统杀掉）
- `DisallowStartIfOnBatteries` 和 `StopIfGoingOnBatteries`：都是 false
- `RestartOnFailure`：间隔 1 分钟，最多 999 次
- `MultipleInstancesPolicy`：`IgnoreNew`
- `StartWhenAvailable`：true

**控制台窗口问题**：普通的命令行程序由计划任务启动时会弹出控制台窗口。解决办法：
- 发布时额外编译一个窗口子系统版本 `e5extend-bg.exe`（`-ldflags -H=windowsgui`），代码和普通版完全相同。
- `service install` 优先使用同目录下的 `e5extend-bg.exe`；找不到就用 `e5extend.exe`，并警告会出现控制台窗口。

安装后立即执行一次 `schtasks /Run /TN e5extend`。查询状态用 `schtasks /Query /TN e5extend /XML`，再结合引擎的心跳判断是否在运行。

#### 1.4.1 `--headless`：无人登录也运行

默认任务是 `InteractiveToken`，用户注销后停止，开机无人登录时不会启动（Windows Server / Server Core 的常态）。Linux 用 `--key-file --enable-linger` 解决同一个问题；Windows 对应 `service install --headless`（其他平台报错，Linux 上的消息指向 `--key-file`/`--enable-linger`）。

**任务定义**：`Principal/LogonType` 改成 `S4U`（“不论用户是否登录都运行”，不保存密码），仍是当前用户、`LeastPrivilege`；`Triggers` 在原有的 `LogonTrigger`、`TimeTrigger`（`PT5M`）之外加 `BootTrigger`（`Delay` 为 `PT30S`）。`Settings`（失败重启 1 分钟 999 次、不限时、电池、`IgnoreNew`、`StartWhenAvailable`）不变。`Actions` 的参数多一个 `--dpapi-key-file <路径>`。非 headless 的 XML 与以前逐字节相同（golden 测试保证）。

**要求（调研结论）**：
- S4U 需要账户有 `SeBatchLogonRight`（作为批处理作业登录）；Administrators 默认有。被“拒绝作为批处理作业登录”挡住时注册/启动失败，安装失败的消息会提到这项权利。
- 注册带开机触发器或 S4U 的任务通常需要管理员权限。安装失败时：错误含 “denied”（英文消息）或 `Env.Elevated()` 报告不是提升的进程，就在 `Result.Manual` 里给出完整命令（用 `e5extend.exe`，不是没有控制台的 `e5extend-bg.exe`；自定义目录时带 `--config-dir`/`--data-dir`），并强调要用同一个用户。其他错误不猜测，只在含 1385 / 0x80070569 / logon failure / logon type / batch job 时追加权利提示；`/Create` 成功而 `/Run` 失败时保留密钥文件并给出启动命令；拒绝访问的提示在两种模式下都有。（schtasks 的消息是本地化的，不能依赖更多文字）。
- S4U 令牌没有网络凭据：访问不了需要用户 Windows 凭据的网络资源；出站 HTTPS（只调用 Microsoft Graph 和通知渠道）不受影响。安装成功时在结果里写明。
- S4U 会话没有口令，系统不会为它解开用户的 DPAPI 主密钥，用户的凭据管理器也读不到。所以不能依赖钥匙串。

**主密钥文件（`internal/vault/keyfile*.go`）**：
- 位置 `<ConfigDir>\master.key.dpapi`。内容：一行头 `E5EXTEND-DPAPI-KEY v1 machine`，下一行是 DPAPI 密文的 base64。密文由 `CryptProtectData` 以 `CRYPTPROTECT_LOCAL_MACHINE | CRYPTPROTECT_UI_FORBIDDEN` 生成，附带一个固定的 entropy 字符串。
- **为什么是本机范围**：用户范围的 DPAPI 在 S4U 会话里解不开（没有密码就没有用户主密钥）。代价是本机任何账户的进程都能解密这份密文，所以不能把它当成加密边界；entropy 也只是防止通用工具照单全收，不是秘密。
- **ACL 才是保护**：用 `CreateFile` 的 `SECURITY_ATTRIBUTES` 创建临时文件，描述符里带 `D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;<当前用户 SID>)`（`P` = 不继承），共享模式为 0，通过这个句柄写入，再原子改名（改名保留文件的 ACL）；文件从诞生起就是受限的，不存在“先创建再按路径收紧”的窗口。本程序新建配置目录（含缺的父目录）时同样用 `CreateDirectory` + 受保护 DACL（`OICI` 继承）；已存在的目录不动，审计时若有其他账户拥有写入、删除、改权限的允许项（忽略仅继承项）就在 `doctor` 里警告并写出目录。当前用户是明确授予的 SID，因为在 UAC 下低权限令牌里的 Administrators 只是“仅拒绝”，只靠 BA 会把任务自己挡在外面。
- **加载**：`fromEnvOrCredential` 在 `E5EXTEND_MASTER_KEY`、`E5EXTEND_MASTER_KEY_FILE` 之后读 `E5EXTEND_MASTER_KEY_DPAPI_FILE`（来源名 `DPAPI key file`），优先于钥匙串。全局隐藏参数 `--dpapi-key-file` 只是设置这个环境变量（和 `--config-dir` 一样，任务动作不能设环境变量）。错误信息里不带路径和密钥。
- **平台隔离**：`keyFileOS` 接口（`protect`/`unprotect`/`create`/`readACL`）在 Windows 上用 `golang.org/x/sys/windows`，其他平台是“不支持”的桩；单元测试在 Linux 上用假实现覆盖格式、原子写、审计和环境变量来源，`keyfile_windows_test.go` 在 Windows CI 上测真实的 DPAPI 和 ACL。
- **审计**（`vault.AuditDPAPIKeyFile`）：文件不存在 / 读不了或解不开 / ACL 里除了当前用户、SYSTEM、Administrators 以外还有允许项（没有 DACL 视为 Everyone）。拒绝项不算。

**状态与 doctor**：`Status` 增加 `Headless`（任务 XML 的 `LogonType` 是 S4U）和从 `--dpapi-key-file` 参数解析出的 `KeyFile`、`KeyFileMissing`、`KeyFileErr`（不含路径）、`KeyFileBroad`。`service status` 多一行 `headless:`，key 文件缺失、读不了或 ACL 过宽时给出提示。`doctor` 在 Windows 上对 headless 任务多一项 `headless`：文件缺失、读不了或用它的密钥解不开保险库里的数据（密钥已换） → fail，文件或所在目录的 ACL 过宽、ACL 读不出来 → warn，否则 ok；消息有代码（`headless.*`、`fix.headlessInstall`），桌面版有中英文文本。`Running` 仍是“未知”，设置页和健康页用 `lib/service.ts` 的引擎心跳判断，S4U 任务与普通任务走同一条路径。

**卸载**：先 `schtasks /Query /TN e5extend /XML` 读任务定义；`master.key.dpapi` 只会由 `--headless` 创建，所以不论任务当前的登录类型，只要它在配置目录里（或任务参数指向的、文件名正好是 `master.key.dpapi` 的）就删，任务不存在时也删；普通安装覆盖 headless 任务时同样删除它。只删文件名正好是 `master.key.dpapi` 的（不会按任务里写的任意路径删文件），并说明主密钥本身没有动。`/Create` 失败时，本次新建的密钥文件会被删掉，不留下一份没有任务使用的密钥副本。

测试：任务 XML 的 golden（`task_headless*.golden`）、`FakeRunner` 下的安装/卸载/状态/拒绝访问、vault 的密钥文件来源（假 DPAPI）、doctor 的 headless 项；Windows 专用测试在 CI 的 windows-latest 上跑。`scripts/service-e2e-headless.sh`（`service-e2e.yml` 的 Windows 步骤）装 headless 任务，检查 `LogonType`、触发器、ACL、doctor 和 `e5extend-bg.exe` 是否启动，再卸载。

### 1.5 Docker

- 仓库根目录放 `Dockerfile`：多阶段构建，最终镜像基于 `gcr.io/distroless/static`，以非 root 用户运行。
  - `ENV E5EXTEND_CONFIG_DIR=/data/config E5EXTEND_DATA_DIR=/data/state`
  - `ENTRYPOINT ["/e5extend"]`，`CMD ["run", "--log-format", "json"]`
- 主密钥通过环境变量 `E5EXTEND_MASTER_KEY` 或 Docker secret 传入（用 `/run/secrets/...` 时设置 `CREDENTIALS_DIRECTORY=/run/secrets`）。
- `docs/user/docker.md` 里给出 `compose.yaml` 示例，带 `restart: unless-stopped`、数据卷，**不暴露任何端口**。
- 镜像发布到 GHCR 的 CI 留到发布阶段再做。

### 1.6 CLI

```
e5extend service install [--key-file] [--enable-linger] [--headless]
e5extend service uninstall
e5extend service status
```

## 2. 健康检查 `internal/doctor`

`e5extend doctor [--offline] [--json]`。每项检查返回 `{id, title, level: ok|warn|fail, detail, fix}`。

| id | 检查内容 | 级别规则 |
|---|---|---|
| config | 配置能加载并通过校验 | 失败 → fail |
| vault | 能拿到主密钥，能解密每个账号的 secret（只尝试解密，不输出内容） | 失败 → fail |
| engine | 引擎是否在运行（锁和心跳）；心跳在 2 分钟内 | 没运行 → fail；心跳过期 → warn |
| service | 后台服务是否已安装并启用（`service.Status`） | 没安装 → warn（也可能是 GUI 在负责运行） |
| linger | 仅 Linux：已安装用户服务但没开 linger | warn |
| headless | 仅 Windows 且任务是 `--headless` 装的：主密钥文件 `master.key.dpapi` 存在、能解密、ACL 不比“用户 + SYSTEM + Administrators”更宽 | 缺失或读不了 → fail；ACL 过宽 → warn |
| account.<n>.token | 静默取令牌（`--offline` 时跳过） | 需要登录、凭据错误 → fail；网络问题 → warn |
| account.<n>.scopes | 令牌里的权限能用上几个 API | 0 个 → fail；少于 5 个 → warn |
| account.<n>.activity | `last_success` 的时间 | 超过 72 小时 → warn；超过 7 天 → fail；从来没成功过 → warn |
| account.<n>.state | `needs_attention`、`paused` 等状态 | 有原因就显示原因和修复建议 |
| account.<n>.secret | `secret_expires` 还剩多少天 | ≤ 30 天 → warn；≤ 7 天或已过期 → fail |
| clock | 本机时间和 Graph 响应头 `Date` 的差（`--offline` 时跳过） | 超过 2 分钟 → warn；超过 5 分钟 → fail |
| network | 能否连到 `https://graph.microsoft.com/v1.0/`（返回 401 也算通） | 连不上 → fail，提示检查代理 |
| notify | 设置了告警吗；`webhook_url` 还放在配置文件里（明文） | 没设置 → warn（建议设置）；在明文配置里 → warn |

- 输出：每项一行，`✓` / `!` / `✗` 加标题和详情，有修复命令时缩进一行显示。最后一行是汇总。
- 退出码：全部 ok 为 0，有 warn 为 1，有 fail 为 2。
- `--json` 输出数组，给以后的 GUI 用。
- 检查项可以并行执行，整体超时 30 秒。

## 3. 告警 `internal/notify`

### 3.1 通道

- **Webhook**：用 `POST` 发 JSON：

  ```json
  {"event":"needs_attention","account":"main","message":"…","time":"RFC3339",
   "text":"[e5extend] main: …","content":"[e5extend] main: …"}
  ```

  `text` 兼容 Slack 和企业微信、飞书类机器人的常见格式，`content` 兼容 Discord。其他服务可以按结构化字段自己处理。
  - 超时 10 秒；失败按指数退避重试 3 次；仍失败只记日志，不影响引擎。
- Webhook 地址可能带令牌，所以**存进 vault**（key 为 `notify/webhook`）：
  - `e5extend notify set-webhook <url>`：保存
  - `e5extend notify clear`：删除
  - `e5extend notify test`：发一条测试消息
- 配置里原来的 `notify.webhook_url` 继续支持，但 `doctor` 会建议迁移；`set-webhook` 会顺带把配置里的明文删掉。
- 桌面通知留给 GUI 阶段做。

### 3.2 事件（只在**状态变化**时发，并且去重）

| event | 什么时候发 |
|---|---|
| `needs_attention` | 账号进入 needs_attention，带原因和建议 |
| `recovered` | 从 needs_attention 或 paused 恢复到 ok |
| `auto_paused` | 连续失败被自动暂停 |
| `stale` | 超过 `notify.stale_after`（默认 72 小时）没有成功调用；之后每 24 小时最多再提醒一次 |
| `secret_expiring` | `secret_expires` 前 30、7、1 天各一次，过期当天一次 |
| `period_ended` | 运行期限结束 |

- 去重状态存在 `state.json` 的 `notified` 字段里（每个账号每种事件上次发送的时间和关键值），只由引擎写入。
- 引擎在每次状态变化和每天第一次 tick 时评估这些事件。发送是异步的，不能阻塞 tick。
- 新配置项：`notify.stale_after`（默认 `"72h"`）、`notify.events`（空表示全部）。

## 4. 引导 `e5extend init`

交互式向导，每一步都可以跳过，已经完成的步骤自动跳过：
1. 没有配置就生成默认配置
2. 选择主密钥方式：钥匙串，或者生成密钥并打印给用户（无桌面环境时推荐后者）
3. 添加账号：问登录方式、租户、Client ID；需要密钥时提示输入；然后显示应用注册的要点和文档链接
4. 登录（`account login`）
5. 测试：静默取令牌，列出可用的 API 数量
6. 立即跑一轮（`run-now`），显示结果
7. 设置告警（可跳过）
8. 安装后台服务（`service install`，必要时提示 `--key-file` 和 linger）
9. 运行 `doctor`，展示结果

- 不是交互式终端时直接拒绝运行，并提示改用各个子命令。
- 向导里不另外实现逻辑，全部调用已有的子命令函数。

## 5. 用户文档 `docs/user/`（中文）

- `getting-started.md`：五分钟上手；先说明能做什么、不能保证什么。
- `app-registration.md`：Entra 应用注册步骤，委托身份和应用身份各一节，附推荐权限清单（引用 graph-api-catalog）。
- `background.md`：各平台“怎样让它一直在后台跑”：Linux 桌面、Linux 服务器（linger、`--key-file`）、macOS、Windows、Docker；每节都有验证方法（`service status`、`doctor`）。
- `docker.md`：compose 示例和密钥传入方式。
- `faq.md`：电脑睡眠时会不会跑、关机多久会出问题、令牌过期怎么办、怎么确认它真的在工作、为什么有的 API 一直是 403、不保证续期。
- `troubleshooting.md`：按 `doctor` 的检查项逐个说明原因和修复方法。
- README 增加“快速开始”一节，链接到这些文档。

文档中出现的命令和输出必须和实现一致；用户能看到的命令名和参数，以 CLI 的 `--help` 为准。

## 6. 测试

- 服务文件：unit、plist、任务 XML 的 golden 测试，包括带和不带 `--key-file`、`e5extend-bg.exe` 存在和不存在两种情况。
- 用假 Runner 测试 install、uninstall、status 依次执行的命令。
- sd_notify：在临时目录里起一个 unixgram 监听，检查收到的消息（仅 Linux 测试，用 build tag 或 `runtime.GOOS` 跳过）。
- doctor：每项检查的 ok、warn、fail 都要覆盖；`--offline` 跳过联网检查；退出码；JSON 输出。
- notify：httptest 接收端；事件去重（同一状态不重复发）；stale 每 24 小时最多一次；secret 提醒 30、7、1 天各一次；发送失败的重试与放弃；**日志和事件内容里不出现 webhook 地址**。
- init：不是终端时拒绝运行；用脚本化的输入测试主流程（通过注入 reader 和 writer）。
- 一年模拟里加上告警：断言事件数量合理，没有重复轰炸。

## 7. 实现时与上文的差异（以代码为准）

- **unit 的 Environment**：两个变量各占一行；路径含空格、`%` 时按 systemd 规则加引号或转义。
- **`service install --print`**：只显示将要写的文件和执行的命令，不写不跑。隐藏参数 `--platform linux|darwin|windows` 只配合 `--print`，
  跨平台打印时用虚构的用户名和路径。可执行文件在临时目录时，`--print` 只警告，真正安装仍然拒绝。
- **Windows 任务**：参数是 `run --log-format json --log-file <DataDir>\engine.log`。窗口子系统版本没有控制台，运行日志写到这个文件
  （`run --log-file` 是新增参数，约 5 MB 转存为 `.1`）。任务 XML 以 UTF-16 保存在 `<DataDir>\service\e5extend-task.xml`，卸载时删除。
  `Status.Running` 在 Windows 上是“未知”，由 `service status` 用引擎的锁和心跳补充。任务不继承 `E5EXTEND_*_DIR` 环境变量，安装时会警告。
- **macOS**：只有 `E5EXTEND_CONFIG_DIR` 或 `E5EXTEND_DATA_DIR` 被显式设置时，plist 才带 `EnvironmentVariables`。`--key-file` 在 macOS、Windows 上报错。
  `bootout` 只在 plist 已存在时执行。
- **卸载**：不删除 `master.key`（它可能是 vault 密钥的唯一副本），只提醒。
- **密钥提示**：`service install` 不带 `--key-file` 时，Linux 上如果 `E5EXTEND_MASTER_KEY` 设置在当前 shell，或者既没有 `CREDENTIALS_DIRECTORY` 也没有
  `DISPLAY`/`WAYLAND_DISPLAY`，会提示加 `--key-file`。`vault.backend = "keyring"` 时拒绝 `--key-file`（那个后端不读文件）。
  新增 `vault.LookupMasterKey`（只查找，从不创建密钥），供 `init` 判断步骤是否已完成。
- **Webhook**：重定向不跟随（3xx 算失败）；除 408、429、5xx 和网络错误外的 4xx 不重试。错误、日志和消息里用 `<webhook>` 代替地址、主机和路径。
  地址每次发送时重新读取（vault 优先，其次配置里的旧字段），所以 `notify set-webhook` 对运行中的引擎立即生效。没有设置地址时引擎不评估也不记录告警，
  之后设置地址会补发仍然存在的问题。`notify test` 不重试。
- **告警评估**：每个 tick 和每轮结束后都评估（比“状态变化和每天第一次 tick”更频繁，靠去重保证不重复）。`recovered` 也在 `stale` 解除后发送；
  配置里手动暂停的账号不发 `recovered` 和 `stale`。`stale` 要求引擎已运行超过 1 小时（停机后刚启动时不误报），从未成功过的账号不发 `stale`。
  `notify.events` 只过滤发送，去重记录照常写。`notified` 里除各事件外还有一个 `unhealthy` 记录，标记“已经通知过、等待 recovered”的一段问题期。
  `notify.stale_after` 至少 1 小时；`notify.events` 的值必须是上表里的事件名。
- **doctor**：`--offline` 时联网的检查（令牌、clock、network）不出现在输出里，文本末尾有说明；离线时 `scopes` 用上一轮记录的权限。
  配置加载失败时只运行 config、engine、service。vault 检查只解密已存在的数据，从不创建主密钥。账号已暂停（配置）时 `activity` 显示正常。
- **init**：向导通过在进程内执行已有子命令完成工作；选择“生成密钥并显示”时，密钥只设置到向导进程的 `E5EXTEND_MASTER_KEY`，
  之后的步骤可用，退出后需要自己保存。

## 8. 评审后的修改（覆盖上文相应说明）

- **launchd**：`enable` 在 `bootstrap` 之前；已安装时先 `bootout`，稍等（1 秒），`bootstrap` 失败时再试一次（间隔 2 秒）。
  `ProgramArguments` 增加 `--log-file <DataDir>/engine.log`（带轮转，和 Windows 一样）；`StandardOut/ErrorPath` 保留，只接收启动早期的崩溃输出。
- **告警去重改为“送达后才算发送”**：`Evaluate` 把告警记为 pending（`state.json` 的 `notified.<kind>` 里有 `pending`、`tried`、`msg`），
  发送器（`Dispatcher.Send(ev, done)`）送达后回调引擎，引擎调用 `notify.MarkDelivered` 清掉 pending。没有确认的告警每小时最多重发一次（每个事件各一次，
  不会在 Webhook 恢复时成批涌出）。vault 读不出地址也算失败。`recovered` 也是 pending 记录，送达后删除，新的问题开始时作废。
- **启动宽限期**：`stale` 只依据 `last_success` 判断。宽限期（启动后 1 小时）内不新发 `stale`，但已有的 `stale` 记录和 `unhealthy` 标记原样保留，
  只有出现比阈值更新的成功才清除，所以重启不会产生 `recovered` 或重复的 `stale`。
- **`secret_expiring` 的记录带到期日**（`<档位>@<日期>`）：到期日变了（换了新密钥）就重置，重新按 30、7、1、0 天提醒。
- **引擎的 tick 里不碰 vault 和钥匙串**：`Dispatcher.Enabled()` 返回缓存值，由独立的 goroutine 每 5 分钟刷新（只查是否已设置地址，不解密）；
  地址只在发送时、在 dispatcher 的 goroutine 里读取。因此刚设置 Webhook 后，运行中的引擎最多 5 分钟才开始评估告警。
- **`run`**：用 `--log-file` 时，启动错误（配置无效、已有引擎在运行、vault 错误、`--log-format` 错误）也写进日志文件；`debug.SetCrashOutput` 把崩溃堆栈写进同目录单独的 `engine-crash.log`（指向会轮转的日志文件的句柄不会跟着轮转，在 Windows 上还会阻止轮转）。
- **重新安装要生效**：systemd 已安装时 `restart`；计划任务在 `/Run` 前先 `/End`。
- **Windows 计划任务**：除 `LogonTrigger` 外增加 `TimeTrigger`（`Repetition` 间隔 `PT5M`，不限期），配合 `IgnoreNew` 和引擎的单实例锁；
  `RestartOnFailure` 保留，但不依赖它——进程崩溃后最迟 5 分钟内被重新拉起。
  `service status`/`uninstall` 解码 UTF-16 输出；`schtasks /Query` 退出码为 1（且不是“拒绝访问”）视为未安装，其他错误如实报告（消息是本地化的，所以只用退出码）。
- **`init`**：“生成密钥并显示”只在 Linux 上提供（只有那里能用 `--key-file` 把它交给后台任务）；主密钥来自环境变量时，向导自动给 `service install` 加 `--key-file` 并说明原因。
- **`LoadCredential=` 的版本**：systemd NEWS 里最早明确提到用户服务可用凭据的是 v258（加密凭据；凭据本身自 v247 起用于系统服务），在 v259 上实测普通 `LoadCredential=`
  对用户服务有效。`systemctl --version` 低于 258 时，unit 改用 `Environment=E5EXTEND_MASTER_KEY_FILE=<path>`，vault 支持这个环境变量
  （顺序：`E5EXTEND_MASTER_KEY`、`E5EXTEND_MASTER_KEY_FILE`、`CREDENTIALS_DIRECTORY`、钥匙串）。
- **doctor**：`notify` 项会尝试解密 vault 里的地址（从不显示），解不开时警告。

### 复审后的小修正

- 告警送达回调按“尝试时间”匹配（`MarkDelivered(…, tried)`），较早一次尝试的迟到回调不会把新一次标记为已送达。
- 读不到 systemd 版本时，也改用 `E5EXTEND_MASTER_KEY_FILE` 方式（各版本都支持），并给出警告。
- 卸载时，unit 里有 `E5EXTEND_MASTER_KEY_FILE` 的情况同样提示 `master.key` 被保留。
- 通过 `E5EXTEND_MASTER_KEY_FILE` 读取的密钥文件如果对其他用户可读，会记一条警告并提示 `chmod 600`。
- Windows 计划任务没法设置环境变量：自定义目录改用新增的全局参数 `--config-dir` / `--data-dir` 传入（它们只是设置对应的环境变量）。macOS 用 plist 的 `EnvironmentVariables`，systemd 用 `Environment=`。
