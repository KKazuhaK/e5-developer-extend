# 设计：图形界面（GUI）

技术栈：Wails v3.1.0 + React + TypeScript + Vite，组件库用 shadcn/ui（Tailwind）。
三个平台（Windows、macOS、Linux）用同一套代码。

## 1. 进程模型

依据：[architecture.md](../architecture.md) 第 2 节的实测——
- 只有托盘、从不创建 WebView 时约 60 MB；
- GTK4 下关窗口后 WebKit 进程不会退出，见 wailsapp/wails#6505。

一个可执行文件 `e5renew-gui`，按参数分成两种模式：

| 模式 | 启动方式 | 做什么 | 是否创建 WebView |
|---|---|---|---|
| 托盘（默认，或 `--tray`） | 开机自启、用户双击 | 托盘图标和菜单；**如果没有别的引擎在运行，就在本进程里运行引擎**，否则只做监视 | **从不** |
| 窗口（`--window`） | 托盘菜单“打开主窗口”，或再次启动程序时 | 主界面；关闭窗口时**整个进程退出** | 是 |

- 单实例：
  - 托盘模式用 Wails 的 SingleInstance（ID 为 `io.github.kkazuhak.e5renew.tray`）。重复启动时，由已运行的托盘实例打开窗口。
  - 窗口模式用另一个 ID（`…​.window`）。重复打开时，让已有窗口获得焦点。
- 托盘通过 `os.Executable() --window` 启动窗口进程，并且不等待它退出。
- 窗口进程**不运行引擎**。它读写的是同一份配置、state 和日志，向引擎发指令用已有的 trigger 文件（`run-now`、`.resume`、`.reload`）。
  所以不需要新的 IPC。
- 托盘进程里的引擎：用 `engine.Run`，带同样的单实例锁。拿不到锁说明 CLI 服务已经在跑，此时托盘只做监视，菜单里显示“由后台服务运行”。
- 托盘每 30 秒读一次 state 来更新图标和提示：
  - 全部正常：普通图标；
  - 有账号需要处理或已暂停：警告图标，提示里写上数量；
  - 引擎没在运行：灰色图标。
- 托盘菜单：
  - 打开主窗口
  - 立即运行一轮（全部账号）
  - 暂停全部 / 恢复全部
  - 状态行（只读，例如“2 个账号正常，下次运行 14:20”）
  - 开机启动（勾选项）
  - 退出（停止本进程里的引擎）

## 2. 共享业务层 `internal/ops`

现在 CLI 的逻辑写在 `cmd/e5renew`（`main` 包）里，GUI 没法复用。把这些逻辑抽到 `internal/ops`：
- 返回带类型的结果，不直接打印；
- CLI 负责把结果格式化成文本；GUI 的服务直接把结果返回给前端；
- **CLI 的行为和输出保持不变**，现有的 CLI 测试全部继续通过。

需要抽出的操作：
- 配置：`ConfigInit`、`LoadSettings`、`SaveSettings`（调度、网络、告警等非账号部分），保存前先校验
- 账号：`ListAccounts`、`AddAccount`、`UpdateAccount`、`RemoveAccount`、`SetSecret`、`Login`（带 UI 回调）、`TestAccount`、`Pause`、`Resume`
- 运行：`RunNow(names)`（引擎在跑就写 trigger，不在跑就在本进程里跑一轮）、`Snapshot()`（引擎是否在运行、心跳、每个账号的状态）
- 日志：`ReadLogs(filter)`
- 体检：`Doctor(offline)`，返回检查项列表
- 告警：`SetWebhook`、`ClearWebhook`、`TestWebhook`、`WebhookConfigured`
- 后台服务：`ServiceStatus`、`ServiceInstall`、`ServiceUninstall`
- API 库：`Catalog(account)`，返回每个 API 能不能用

## 3. 前后端接口（契约）

Wails 服务放在 `gui/services/`，每个服务只是对 `internal/ops` 的薄封装。前端通过 `wails3 generate bindings` 生成的 TypeScript 调用。
下面的类型就是契约：前端的模拟后端也按它来实现。

```ts
type Level = "ok" | "warn" | "fail";
type AuthKind = "device_code" | "browser" | "client_secret" | "password";
type AccountStatus = "ok" | "needs_attention" | "paused" | "period_ended" | "not_signed_in";

interface Snapshot {
  engine: { running: boolean; heartbeatAgeSec: number | null; host: "gui" | "service" | "none" };
  accounts: AccountSummary[];
  generatedAt: string;          // RFC3339
}
interface AccountSummary {
  name: string; auth: AuthKind; tenant: string; status: AccountStatus;
  reason: string;               // 给人看的原因，可以为空
  fix: string;                  // 建议执行的操作，可以为空
  nextRun: string | null; lastRun: string | null; lastSuccess: string | null;
  todayOk: number; todayFailed: number; usableApis: number; totalApis: number;
  secretExpires: string | null; paused: boolean;
}
interface AccountInput {        // 新增和编辑共用
  name: string; auth: AuthKind; tenant: string; clientId: string;
  username: string; categories: string[]; secretExpires: string;
  secret?: string;              // 只在新增或更换时传；后端永远不会把它返回给前端
}
interface LoginEvent {          // 通过 Wails 事件 "login:<name>" 推送
  kind: "device_code" | "browser_opened" | "done" | "error";
  message: string; userCode?: string; verificationUri?: string;
}
interface LogEntry { ts: string; account: string; api: string; status: number; class: string; latencyMs: number; event?: string; detail?: string; }
interface Settings {
  schedule: { intervalMin: string; intervalMax: string; apisPerRoundMin: number; apisPerRoundMax: number;
              randomOrder: boolean; workDays: string[]; workStart: string; workEnd: string;
              periodStart: string; periodEnd: string; };
  network: { proxy: string };
  notify: { staleAfter: string; events: string[]; webhookConfigured: boolean };
  ui: { language: "auto" | "zh" | "en"; theme: "system" | "light" | "dark"; startAtLogin: boolean };
}
interface ValidationError { field: string; message: string }   // 保存失败时返回
interface DoctorCheck { id: string; title: string; level: Level; detail: string; fix: string }
interface CatalogEntry { id: string; category: string; name: string; usable: boolean; scopes: string[] }
interface ServiceInfo { platform: string; installed: boolean; enabled: string; running: string; details: string[] }
```

服务和方法：

| 服务 | 方法 |
|---|---|
| `StatusService` | `Snapshot()`、`RunNow(names: string[])` |
| `AccountService` | `Add(in)`、`Update(in)`、`Remove(name)`、`SetSecret(name, secret)`、`Login(name)`（结果通过事件推送）、`CancelLogin(name)`、`Test(name)`、`Pause(name)`、`Resume(name)`、`Get(name): AccountInput`（不含 secret） |
| `LogService` | `Read(account, sinceHours, limit)` |
| `SettingsService` | `Get()`、`Save(s): ValidationError[]`、`SetWebhook(url)`、`ClearWebhook()`、`TestWebhook()` |
| `HealthService` | `Doctor(offline)`、`ServiceInfo()`、`InstallService()`、`UninstallService()` |
| `CatalogService` | `List(account)`、`Categories()` |

- 出错时 Go 返回 error，前端得到 rejected promise。错误信息里带上 ops 给出的修复建议。
- **安全**：Secret、令牌、Webhook 地址都不会出现在任何返回值、事件或日志里。`SetWebhook` 只返回成功或失败。
- `ui` 这部分设置存在 `ConfigDir/gui.json` 里，不放进 `config.toml`，因为它们只和界面有关。

## 4. 界面

整体：左侧导航，右侧内容；支持浅色、深色、跟随系统；语言分中文和英文，默认跟随系统，中文为主。
窗口默认 1100×720，最小 900×600。产品显示名暂用常量 `AppName = "e5renew"`（最终名称待定，只改这一处）。

- **概览**（首页）
  - 顶部：引擎状态（运行中 / 未运行 / 由后台服务运行），下次运行时间，以及“立即运行全部”按钮。
  - 账号卡片：状态徽标、原因和建议、上次成功时间、今日成功/失败、可用 API 数，操作按钮有立即运行、暂停/恢复、编辑。
  - 有账号需要处理时，最上方显示醒目的提示条，带“去处理”按钮。
  - 一个账号都没有时，显示引导：一个按钮进入添加账号向导，再给一个应用注册文档的链接。
- **账号**：列表，可按“出错 / 暂停”筛选，也能搜索。
  - 添加和编辑是一个分步向导：
    1. 选登录方式（每种方式附一句说明：什么情况适合用它）；
    2. 填租户、Client ID，需要时填 Secret 或密码；
    3. 选 API 分类；
    4. 登录：设备码模式显示代码和链接，带“复制”和“打开浏览器”按钮；浏览器模式显示“请在浏览器中完成登录”；
    5. 测试：显示已授予的权限和可用的 API 数量；
    6. 完成。
  - 删除账号要二次确认。“更换 Secret” 是单独的对话框。
- **日志**：表格，列为时间、账号、API、状态码、耗时；可按账号和时间范围筛选；失败的行标红。每 5 秒自动刷新，可以关闭。
- **健康检查**：按 `doctor` 的检查项逐条显示（✓ / ! / ✗、详情、修复建议），有“重新检查”按钮。
  - 后台服务区：显示是否安装；提供“安装为系统后台服务”和“卸载”按钮，旁边说明它和“开机启动托盘”有什么区别。
- **设置**：分组显示——
  - 调度：间隔、每轮 API 数、工作日（多选）、工作时段、运行期限；
  - 网络：代理；
  - 告警：设置 Webhook（输入框保存后清空，只显示“已设置”）、发送测试、清除；
  - 界面：语言、主题、开机启动。
  - 保存时有错误，就在对应字段下面显示后端返回的 `ValidationError`。
- **关于**：版本号、项目链接、免责声明（“不保证续期”、“与 Microsoft 无关”）。

数据刷新：页面可见时每 3 秒拉一次 `Snapshot()`，切到后台时停止。

## 5. 前端工程

- 目录：`gui/frontend/`。样式用 Tailwind 加 shadcn/ui 组件（组件源码直接放进仓库，不在构建时从网上拉）。
- 路由：用简单的状态切换，或者 `react-router` 的内存路由（不依赖 URL）。
- 国际化：自己写一个轻量的字典（`zh.ts`、`en.ts`），不引入大型 i18n 库。
- **模拟后端**：`VITE_MOCK=1` 时用 `src/api/mock.ts` 里实现了第 3 节契约的假数据，覆盖正常、需要处理、暂停、无账号等状态，
  可以在普通浏览器里独立开发和截图审查。
- 检查：`tsc --noEmit`、`vite build`，以及用 `vitest` 测工具函数（时间格式化、状态到徽标的映射、表单校验）。

## 6. 构建与 CI

- GUI 的 Go 代码放在 `gui/`（`package main`），和 CLI 用同一个 Go module。
  CLI（`cmd/e5renew`）不导入 Wails，**继续保持无 CGO**。
- 构建步骤：先 `wails3 generate bindings -d gui/frontend/bindings -ts ./gui`，再在 `gui/frontend` 里 `npm ci && npm run build`，最后 `go build -tags frontend ./gui`（Windows 上还要加 `-ldflags -H=windowsgui`；见第 8 节：不带 `frontend` 标签时只嵌入一个占位页）。
  Linux 上需要 CGO 和 GTK4、WebKitGTK 6.0 开发包。
- CI 新增 `GUI build` 任务，在三个平台上编译。ubuntu 先用 apt 装 `libgtk-4-dev libwebkitgtk-6.0-dev`；
  另外跑前端的类型检查、vitest 和构建。
- 打包（AppImage、dmg、NSIS）留到发布阶段。

## 7. 测试

- `internal/ops`：单元测试。现有 CLI 测试改为通过 ops 运行后，必须全部通过。
- `gui/services`：用临时目录、MockInit 的钥匙串和假的认证器做单元测试，并且检查返回值和事件里不含 Secret 或 Webhook 地址。
- 托盘模式：测试“没有其他引擎时，托盘进程运行引擎；有 CLI 服务时只做监视”的判断逻辑（不启动真实托盘）。
- 手动验证（在开发机的桌面上）：
  1. 只开托盘时内存约 60 MB，没有 WebKit 进程；
  2. 打开窗口后有 WebKit 进程；关闭窗口后窗口进程和它的 WebKit 进程全部退出；
  3. 截图审查各个页面。

## 8. 实现说明（与上文不同或补充的地方）

**包结构**
- `internal/ops`：业务层。`Ops` 结构体带可替换的依赖（认证器工厂、Graph 地址、service 的 Runner 等），零值就是真实环境。
  CLI 的 `app` 保留原来的测试用字段，每次调用时用 `a.ops()` 组装出 `*ops.Ops`，所以 CLI 测试不用改。
- `gui/services`：六个 Wails 服务，只依赖 `internal/ops`，**不导入 Wails**（事件通过 `Emitter` 回调发出），所以普通 `go test` 就能测。
- `gui/trayhost`：托盘逻辑（要不要在本进程里跑引擎、图标、菜单文字），同样不导入 Wails。
- `gui`（`package main`）：只有 Wails 接线、图标、资源嵌入。它是唯一需要 CGO 的包，CI 的 CLI 任务用 `go list ./... | grep -v '/gui$'` 把它排除。

**资源嵌入**：`go:embed` 目标目录不存在会让编译失败，所以用构建标签 `frontend` 选择：
带 `-tags frontend` 嵌入 `gui/frontend/dist`；不带时嵌入 `gui/frontend-placeholder/index.html`（一页说明）。
CI 在前端构建成功后自动加 `-tags frontend`。发布构建必须带这个标签。

**绑定**：`wails3 generate bindings -d gui/frontend/bindings -ts ./gui`。生成路径是
`gui/frontend/bindings/github.com/KKazuhaK/e5-renew/gui/services/<服务名小写>.ts`（模型在同目录 `models.ts`，`ops` 包的模型在 `.../internal/ops/models.ts`）。

**对第 3 节契约的补充**
- 事件：`login:<name>`（`LoginEvent`，最后一条是 `done` 或 `error`）；`run:done`（`{ error: string }`，在本进程里跑完一轮后发出）。
  `browser_opened` 的 `message` 是固定文字，登录地址不放进事件（里面带请求状态）；`device_code` 事件的 `userCode`、`verificationUri` 是从微软给的提示文字里解析出来的，解析不到就为空，`message` 总是完整文字。
- `AccountService.Test(name)` 返回 `{ scopes, usableApis, totalApis, expiresOn }`，不含登录名。
- `HealthService.InstallService()` / `UninstallService()` 返回 `{ actions, warnings, manual }`（`manual` 是要用户自己执行的命令）。
- `LogService.Read` 按时间**从新到旧**返回；`sinceHours`、`limit` 为 0 表示不限。
- `SettingsService.Save` 返回的 `ValidationError.field` 是驼峰路径：`schedule.intervalMin`、`schedule.workDays`、`schedule.periodStart`、`network.proxy`、`notify.staleAfter`、`notify.events`、`ui.language`、`ui.theme`、`ui.startAtLogin`。
- `StatusService.RunNow`：引擎在跑就写 trigger 文件（最多约 30 秒后开始）；没有引擎时在窗口进程里异步跑一轮，立即返回，结束时发 `run:done`。
- 账号状态判定顺序：暂停（配置里暂停或引擎自动暂停）→ 运行期已结束 → `not_signed_in`（没有已存的 secret / 登录）→ `needs_attention`（引擎记录的原因，或 secret 已过期、超过 3 天没有成功）→ `ok`。
  `reason` 和 `fix` 是把引擎存的“原因; 建议”在第一个 `; ` 处拆开。
- `engine.host`：引擎由托盘进程运行时，托盘在数据目录写 `engine.host`（内容 `gui <pid>`），窗口进程据此区分 `gui` 和 `service`。进程已不存在的标记会被忽略。

**安装后台服务时的交接**：后台服务和托盘里的引擎抢同一把锁。窗口进程在安装前，如果引擎由托盘运行，就在数据目录写
`gui-handover`；托盘每 3 秒查一次，看到后停掉自己的引擎并在 2 分钟内不再抢锁，窗口等锁释放（最多 60 秒）再安装。
安装用的是和 GUI 放在同一目录的 `e5renew` 命令行程序（后台服务要运行的是 `e5renew run`），找不到就报错。

**配置改动后的重新加载**：窗口里改了账号或设置之后，服务会写 `.reload` trigger，引擎马上重新读配置，不用等定时检查。

**日志**：托盘进程把自己的日志写到数据目录的 `gui.log`（约 2 MB 后换文件）。为了不把租户名、登录名带进去，日志里所有 `err` 字段都换成固定文字。

## 9. 实测（2026-11-16）

| 场景 | Linux（XFCE / X11，无 GPU，GTK4） | Windows 11 IoT LTSC 2024（虚拟机，WebView2 157） |
|---|---|---|
| 只有托盘 | RSS 137 MB；PSS 120 MB，其中 **private dirty 32 MB**，其余主要是 GTK/WebKit 库的只读页（private clean 86 MB，系统内存紧张时可以回收）；没有子进程 | 工作集 59–63 MB，私有 52–53 MB；没有 WebView2 进程 |
| 打开主窗口 | 窗口进程树 PSS 约 420–450 MB | 窗口进程工作集 43 MB，另有 6 个 WebView2 进程 |
| 关闭主窗口 | 窗口进程和它的 WebKit 进程在 2 秒内全部退出；托盘不受影响 | 窗口进程退出，WebView2 进程数回到 0；托盘仍是 59 MB |

- 之前文档里写的“托盘约 60 MB”，是在窗口进程同时运行、共享内存页被分摊的情况下测得的 PSS，偏低。按上表理解：
  托盘真正独占的是 30 多 MB，另外映射着可回收的界面库。
- 要进一步压低：用一个不链接 WebKit 的单独托盘程序（纯 Go 的 StatusNotifier 托盘），作为以后的优化项。
- Windows 上：托盘第一次需要主密钥时，会自动在凭据管理器里创建；第二次启动程序会打开主窗口；后台运行时不出现控制台窗口。

**评审后的修改**
- 安装后台服务前先做不改动任何东西的检查（找得到命令行程序、安装选项合法），通过后才请托盘交出引擎。检查通过但安装失败时，窗口进程删除 `gui-handover` 并写 `gui-resume`，托盘（每 3 秒查一次）立即结束静默期，收回引擎。
- 托盘在后台服务**已安装**时只监视，不去拿引擎锁（开机时托盘先启动也不会把服务挡在外面）；如果托盘已经在跑引擎，发现服务已安装就放手。
  服务状态（已安装、是否在运行）最多每 5 分钟问一次平台，交接或恢复请求之后马上重新问。
- 托盘“立即运行一轮”在没有引擎时在托盘进程里跑，用托盘的可取消 context（退出时取消）；这期间托盘算作引擎的宿主，也不会同时去启动引擎。
- 托盘“退出”在后台协程里停引擎，完成后再退出应用，界面线程不阻塞。菜单只在状态或语言变化时才重画。
- `gui.log` 在写入时按大小轮换（2 MB，保留一份旧文件）；“engine did not start” 同一种原因只记一次；日志里去掉 `account`、`detail`，`err` 换成固定文字。
- `engine.host` 带进程号，托盘启动时先删掉遗留的标记，避免 Windows 上进程号被复用而误判。
- 窗口进程退出前，如果有安装、卸载或本进程里的一轮正在进行，最多再等 60 秒。
- `SettingsService.Save` 先校验 `ui`，有错就什么都不保存；“开机启动”设置失败作为 error 返回，不是 `ValidationError`。
- `AccountService.Get` 会返回账号配置里的用户名（编辑表单需要），不返回 secret。`Settings.network.proxy` 发给前端时隐藏用户信息里的密码（`http://user:xxxxx@host`）；保存时如果收到的正好是隐藏后的形式，就保留已存的值。
- Windows 的 GUI 构建必须加 `-ldflags -H=windowsgui`（发布和 CI 都是），否则会多出一个控制台窗口。Ubuntu 的 CI 还需要 `libx11-dev`。

**第二轮评审后的修改**
- Windows 上 `service.ExecRunner` 启动的每个外部命令都带 `HideWindow` 和 `CREATE_NO_WINDOW`（`internal/service/hidewindow_windows.go`，其他平台是空操作），托盘用 `-H=windowsgui` 构建，定期查询任务计划程序时不会闪控制台窗口。
- 关闭窗口时先取消登录和本进程里的一轮（`Backend.Close`），只对安装、卸载做有限时间的等待。Wails 把单实例锁留到整个关闭流程的最后才释放，没有提前释放的接口，所以等待期间再次启动会被忽略（窗口已标记为正在关闭）。
- 托盘菜单的“开机启动”勾选也参与“状态没变就不重画”的比较，在窗口里改了设置之后会同步。
- 托盘日志额外去掉 `panic` 字段。

**第三轮评审后的修改：后台服务已安装时，托盘只监视**
- 规则：只要后台服务已安装，托盘**从不**运行引擎；如果它正持有引擎锁，就放手。没有安装时，锁空闲就由托盘运行引擎。
- 不做“服务没运行时托盘代跑”。原因：systemd 的 `Type=notify` 要等引擎拿到锁并发出 READY 才算 active，托盘一占着锁，服务永远 READY 不了，被 systemd 每 30 秒重启一次，两边互相拖累；
  Windows 任务计划程序报告不出“是否在运行”，而宽限期又恰好等于任务的重复间隔。无法可靠区分“服务停了”和“服务正在重启”，所以不抢。
- 服务已安装但引擎没在运行（锁空闲，且心跳超过 2 分钟没有更新）：托盘图标变警告，状态行显示“后台服务已安装但没有运行 / Background service installed but not running”，
  菜单多一项“检查问题… / Diagnose…”，用 `--window --page health` 打开窗口。窗口把页面名放在地址里（`/?page=health`）；前端目前不读它，所以先打开窗口，之后前端想支持只需读这个参数。
- 查询服务状态出错：错误从不缓存。已有上一次的答案就沿用它，下次刷新再问；还没有任何答案（刚启动）时只监视、不拿锁、下次刷新重试；
  连续 3 次出错且始终没有答案，就当作“未安装”，托盘可以运行引擎。成功的答案缓存 5 分钟，交接或恢复请求之后立即失效。
