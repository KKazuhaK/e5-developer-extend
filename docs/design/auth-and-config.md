# 设计：认证与配置

范围：`internal/paths`、`internal/config`、`internal/vault`、`internal/auth`，以及配套的 CLI 命令。
调度引擎、Graph 调用和 GUI 不在这次范围内。

## 1. 目录 `internal/paths`

| 用途 | Linux | macOS | Windows |
|---|---|---|---|
| 配置 `ConfigDir()` | `$XDG_CONFIG_HOME/e5extend`（默认 `~/.config/e5extend`） | `~/Library/Application Support/e5extend` | `%APPDATA%\e5extend` |
| 数据 `DataDir()` | `$XDG_DATA_HOME/e5extend`（默认 `~/.local/share/e5extend`） | `~/Library/Application Support/e5extend` | `%LOCALAPPDATA%\e5extend` |

- 环境变量 `E5EXTEND_CONFIG_DIR`、`E5EXTEND_DATA_DIR` 可以覆盖（测试、Docker、Windows 服务模式用）。
- 目录按需创建，权限 0700；目录已存在且不是 0700 时（非 Windows）会收紧为 0700。
- 配置文件：`ConfigDir()/config.toml`。

## 2. 配置 `internal/config`

TOML 格式（`github.com/pelletier/go-toml/v2`）。**配置文件里不放任何密钥**：
Client Secret 和密码都存在 vault 里，按账号名索引。

```toml
version = 1                      # 格式版本，以后迁移用

[schedule]
interval_min = "40m"             # 两轮之间的随机间隔，下限
interval_max = "2h"              # 上限
apis_per_round_min = 3
apis_per_round_max = 8
random_order = true
work_days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]   # 默认每天；只在工作日调用写 ["mon", "tue", "wed", "thu", "fri"]
work_start = "00:00"             # 本地时间；work_start == work_end 表示全天（默认）
work_end = "00:00"
# period_start = 2026-12-01      # 可选，运行期限
# period_end = 2027-12-01

[network]
proxy = ""                       # 空表示用环境变量（HTTPS_PROXY 等）

[notify]
webhook_url = ""

[vault]
backend = "auto"                 # auto | keyring | file，见第 3 节

[[accounts]]
name = "main"                    # 唯一；1–64 个小写字母、数字、- 或 _；不能是 Windows 保留名（con、prn、aux、nul、com1–9、lpt1–9）
auth = "device_code"             # device_code | browser | client_secret | password
tenant = "contoso.onmicrosoft.com"   # 租户 ID 或域名；委托身份可以用 organizations
client_id = "00000000-0000-0000-0000-000000000000"
username = ""                    # password 模式的登录名；client_secret 模式下作为目标用户（{user}）
categories = []                  # 空表示全部分类
paused = false
secret_expires = ""              # client_secret 模式可选，YYYY-MM-DD，用于到期提醒
home_account_id = ""             # 委托身份登录成功后由程序写入，用于静默刷新
```

- `Load(path)`：读取文件，补上默认值，然后 `Validate()`。文件不存在时返回 `ErrNotFound`。
  **先只读 `version`**：大于当前支持的版本时直接报 `config version N is newer than this e5extend supports; upgrade`，
  不会再给出一堆“未知字段”错误；通过后才做严格解码（未知字段会报错）。
- `Save(path)`：原子写入（同目录临时文件 → `fsync` → `rename`），权限 0600。
  **文件是整体重写的，原有注释不会保留。** 每次保存都会在文件开头写一段短注释，
  指向 `e5extend config init --print-template`（打印带注释的模板）。
- `Validate()`：把所有问题一次性返回（`errors.Join`），每条带字段路径，例如 `accounts[1].client_id: not a GUID`。
  校验内容：
  - 时长解析；`interval_min ≤ interval_max`；`apis_per_round_min` 在 1 到 `apis_per_round_max` 之间
  - 星期名合法；`HH:MM` 格式合法
  - 账号名唯一（因为只允许小写，所以不区分大小写也唯一）且字符合法，不是 Windows 保留名
  - `auth` 取值合法；`client_id` 是 GUID
  - `tenant` 非空；`client_secret` 模式下 `tenant` 不能是 `common` 或 `organizations`
  - `password` 模式下 `username` 必填
- 校验信息里的代理地址会隐藏用户名和密码；无法按 `scheme://host` 解析的值不会回显。
- `Default()`：返回带默认值、没有账号的配置。
- `webhook_url` 仍被读取，但推荐用 `e5extend notify set-webhook` 把地址存进 vault（见 service-doctor-notify.md）。

## 3. 密钥 `internal/vault`

**一个主密钥加上磁盘上的加密文件。** 这样可以绕开几个问题：
- 不受 Windows 凭据管理器单条 2.5 KB 的限制；
- Windows 服务模式读不到用户凭据管理器时，也有替代方案；
- 三个平台的行为一致。

- 主密钥：32 字节随机数，按以下顺序查找：
  1. 环境变量 `E5EXTEND_MASTER_KEY`（base64）
  2. systemd 凭据：`$CREDENTIALS_DIRECTORY/e5extend-master-key`
  3. 系统钥匙串（`github.com/zalando/go-keyring`，服务名 `e5extend`、用户名 `master-key`；
     全平台无 CGO：macOS 调 `/usr/bin/security`，Linux 走 DBus Secret Service，Windows 用凭据管理器）。
     每次钥匙串调用有 10 秒超时（在 goroutine 里调用），避免被锁住的 Secret Service 卡死进程；
     超时会报错并建议设置 `E5EXTEND_MASTER_KEY`。
- `backend = "auto"`：按 1、2、3 的顺序找；都没有时，如果钥匙串可用就生成一个新的存进去，否则报错，并给出操作建议：
  设置环境变量，或用 `e5extend vault init --print-key` 生成一个。
  **如果 `vault/` 下已经有 `*.bin` 文件，绝不生成新密钥**（新密钥解不开旧数据）：返回 `ErrNoMasterKey`，
  提示“恢复旧密钥（`E5EXTEND_MASTER_KEY` 或钥匙串），或运行 `e5extend vault reset` 清空”。`vault init` 同样遵守。
  写入钥匙串后会重新读取，并以钥匙串里实际存的值为准（两个进程同时初始化时不会各用各的）。
- `e5extend vault reset [--yes]`：删除整个 vault 目录（所有 secret 和令牌缓存）。账号仍留在配置里，之后需要
  `account set-secret` 重新输入密钥并重新登录。
- `backend = "keyring"`：只用 3。`backend = "file"`：只用 1 和 2。
- 数据：`DataDir()/vault/<key>.bin`，AES-256-GCM，随机 nonce，以 key 名作为附加认证数据（AAD），防止把文件互换。
  文件格式：版本字节 + nonce + 密文。原子写入，权限 0600。
- key 名：`secret/<account>`（Client Secret 或密码）、`tokencache/<account>`（MSAL 令牌缓存）。
- 接口：`Get(key) ([]byte, error)`、`Put(key, []byte) error`、`Delete(key) error`；不存在时返回 `ErrNotFound`。
  另有 `Has(key)`（只看文件是否存在，不需要主密钥）和 `Lock(ctx, key)`（跨进程咨询锁，锁文件是 `<key>.lock`）。
- 主密钥错误时（解密失败）返回 `ErrWrongKey`，并提示可能换了机器或钥匙串被清空。

## 4. 认证 `internal/auth`

基于 `msal-go`。每个账号一个 `Authenticator`：

```go
type Authenticator interface {
    // Token 返回一个有效的访问令牌。只做静默获取，绝不弹出交互。
    Token(ctx context.Context) (Token, error)
    // Login 执行交互式登录（设备码、浏览器）或首次验证（client_secret、password）。
    Login(ctx context.Context, ui LoginUI) (Token, error)
    // Logout 清掉这个账号的令牌缓存。
    Logout(ctx context.Context) error
}

type Token struct {
    AccessToken string
    ExpiresOn   time.Time
    Scopes      []string // 从 JWT 的 scp（委托）或 roles（应用）解析，不做签名校验
    Account     string   // UPN 或应用 ID，用于显示
    HomeAccountID string // 委托身份，登录后要写回配置
}

type LoginUI interface {
    DeviceCode(message string)           // 把设备码提示展示给用户
    OpenBrowser(url string) error        // GUI 可以覆盖；CLI 默认调用系统浏览器
}
```

- **Scope**：一律请求 `https://graph.microsoft.com/.default`。委托身份下，MSAL 会自动加上 `openid profile offline_access`。
  这样拿到的是应用注册里已经同意的全部权限，正好用来自动筛选 API。
- **令牌缓存**：实现 `msal-go` 的 `cache.ExportReplace`，读写 vault 里的 `tokencache/<account>`。
  - 缓存不存在时，`Replace` 会把 msal 内存里的缓存重置为空（`{}`），所以长时间运行的进程不会把已删除的缓存“复活”。
  - 缓存读不出来（`ErrWrongKey` / `ErrCorrupt`）：`Login` 里会删掉这份坏数据、记一条警告并当作空缓存，登录可以恢复；
    静默的 `Token()` 返回 `ErrLoginRequired`（同时包着原始错误），CLI 提示“运行 `account login`（会丢弃读不出来的缓存）”。
    client_secret 账号的缓存只是优化，读不出来时总是直接重建。
  - 读写同时持有进程内互斥锁和 vault 里的按账号文件锁（`tokencache/<account>.lock`）。
- **各模式**：
  - `device_code`：`public.Client.AcquireTokenByDeviceCode`，通过 `ui.DeviceCode` 展示提示。
  - `browser`：`public.Client.AcquireTokenInteractive`，重定向地址用 `http://localhost`（端口由 MSAL 选）。
  - `password`：`public.Client.AcquireTokenByUsernamePassword`，密码从 vault 读取。
    注意：该函数在 msal-go v1.10.1 中已标记为 **deprecated**（ROPC 不安全，也不支持 MFA）；
    这里仍然使用它，因为这是需求；以后 msal-go 删除该 API 时要改。
  - `client_secret`：`confidential.Client.AcquireTokenByCredential`，Secret 从 vault 读取。
    应用身份没有 refresh token；`Token()` 每次直接用 Secret 换取（MSAL 会缓存访问令牌）。
- **静默获取**（委托身份）：用 `home_account_id` 在 `client.Accounts()` 里找到对应账号，再 `AcquireTokenSilent`。
  找不到账号就返回 `ErrLoginRequired`。
- **错误分类**（给调度引擎和健康检查用），用 `errors.Is` 判断。AADSTS 代码优先从响应 JSON 的 `error_codes` /
  `error_description` 里解析，找不到时才退回到对整段错误文本的搜索；
  已知代码没命中时才看 `invalid_grant` 等通用错误名（因为 `AADSTS65001` 也带 `invalid_grant`）：
  - `ErrLoginRequired`：没有缓存、`invalid_grant`、`interaction_required`、`AADSTS50173`（凭据已更改）、`AADSTS700082`（refresh token 过期）等
  - `ErrInvalidClient`：Secret 错误或过期（`AADSTS7000215`、`AADSTS7000222`），应用不存在（`AADSTS700016`）
  - `ErrConsentRequired`：`AADSTS65001`
  - `ErrTransient`：网络错误、超时、5xx、429
  - 其余原样返回，并包一层上下文
- 额外的提示（CLI 根据错误里的代码给出）：`AADSTS50126`（密码错误）→ 运行 `account set-secret`；
  `AADSTS50076` / `AADSTS50079`（需要 MFA）→ 改用 device_code 或 browser；`AADSTS7000218` → 打开“允许公共客户端流”。
- **可注入的端点**：`auth.NewWithOptions` 接受 `HTTPClient`、`AuthorityHost`、`DisableInstanceDiscovery`，测试里指向本地假服务。
- **HTTP**：用同一个 `*http.Client`，支持配置里的代理，再回退到环境变量；不设全局超时，靠 context 控制。

## 5. 应用注册要求（写进用户文档）

本项目**不内置公共 Client ID**，用户用自己租户里的应用注册：
- 委托身份：平台选“移动和桌面应用程序”，重定向 URI 填 `http://localhost`；打开“允许公共客户端流”（设备码和账号密码登录需要）。
- 应用身份：创建 Client Secret，授予应用权限并点“代表租户授予管理员同意”。
- 推荐权限见 [graph-api-catalog.md](../graph-api-catalog.md)。

## 6. CLI 命令（本阶段）

```
e5extend config path                    # 打印配置和数据目录
e5extend config init [--print-template] # 生成默认 config.toml（已存在则不动）；--print-template 只打印带注释的模板
e5extend config validate                # 校验并列出所有问题

e5extend account add <name> --auth device_code|browser|client_secret|password \
        --tenant T --client-id ID [--username U]
                                       # 写配置；client_secret/password 会提示输入（不回显），存进 vault
e5extend account list                   # 名称、方式、租户、登录状态
e5extend account login <name>           # 交互式登录 / 首次验证，成功后写回 home_account_id
e5extend account test <name>            # 静默取令牌，打印账号、过期时间、已授予的权限
e5extend account set-secret <name>      # 重新输入并保存 client secret / 密码（可加 --secret-expires）
e5extend account remove <name> [--yes]  # 删除配置、vault 里的 secret 和 tokencache

e5extend vault init [--print-key]       # 生成主密钥：存进钥匙串，或打印出来用于环境变量 / systemd 凭据
e5extend vault reset [--yes]            # 删除所有 vault 数据（主密钥丢失时用）
```

- 密钥输入用 `golang.org/x/term` 不回显读取；非终端时从 stdin 读一行（方便脚本）。
- Ctrl+C 或 SIGTERM 会立即中止密钥和确认提示，什么都不保存（终端状态会恢复）；第一次信号之后恢复默认处理，再按一次直接结束进程。
- 账号名限制见第 2 节；`account add` 先单独校验账号并读取密钥，再加锁、重新读配置、写入。
- 出错时打印下一步怎么做，例如 `ErrLoginRequired` → `run: e5extend account login main`。

## 7. 长期运行相关

- 令牌缓存每次变化都原子写回；同一个账号的缓存读写加进程内互斥锁，并加跨进程的文件锁
  （`github.com/gofrs/flock`，`vault/tokencache/<account>.lock`）。
- 配置的“读取 → 修改 → 保存”由 `ConfigDir()/.config.lock` 文件锁保护（`config init`、`account add/remove/login` 写回、`set-secret`）。
  等锁最多 30 秒。锁是咨询锁，只对遵守它的 e5extend 进程有效。
- vault 和配置写入都是原子的，断电不会留下半个文件。
- 静默刷新失败时按错误类型区分：`ErrTransient` 只重试，其他错误标记为需要处理，交给健康检查和通知（后续阶段实现）。
- `secret_expires` 只记录到配置里，到期提醒在调度阶段实现。

## 8. 测试（不连真实的微软服务）

- `paths`：环境变量覆盖、各平台默认值（按 `runtime.GOOS` 分支测试）。
- `config`：默认值、往返读写、每条校验规则、原子写入后的文件权限（Windows 上跳过权限检查）。
- `vault`：加解密往返、错误主密钥、AAD 防互换、文件不存在；钥匙串用 `keyring.MockInit()`。
- `auth`：JWT `scp`/`roles` 解析、错误分类表驱动测试、令牌缓存的 Export/Replace 往返；
  另有用 `httptest` 起的假身份服务（OpenID 元数据、设备码、令牌端点）：登录、第二次 `Token()` 零 HTTP 请求、
  新 Authenticator 通过 vault 读回缓存、`home_account_id` 匹配、`invalid_grant` → `ErrLoginRequired`、5xx / 429 / 连接被拒 → `ErrTransient`、
  Logout 之后内存缓存不复活、缓存读不出来时 `Login` 恢复。
- CLI：`config init/validate`、`account add/list/remove` 在临时目录里跑。
