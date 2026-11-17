# 异常检测与多渠道通知

在现有告警（`internal/notify`：`needs_attention`、`recovered`、`auto_paused`、`stale`、`secret_expiring`、`period_ended`，
一个通用 Webhook）的基础上：

1. 增加“真正的异常”判断，偶发失败和已知不可用的分类不打扰用户；
2. 支持多个通知渠道（Telegram、邮件、企业微信、飞书、钉钉、Discord、Slack、Bark、ntfy、Server 酱、通用 Webhook），
   每个渠道有内置的中英文模板，也可以自定义；
3. 降噪：冷却时间、恢复通知、免打扰时段。

## 1. 新的事件

| 事件 | 条件（默认值，可在配置里改） | 级别 |
|---|---|---|
| `failure_spike` | 某账号最近 `spike_rounds`（3）轮里，调用失败率 ≥ `spike_rate`（50%）且失败次数 ≥ `spike_min_failures`（5）。**排除** `not_found`/400 这类“已知不可用”的分类（与 `doctor` 的 `account.<name>.apis` 检查同一判定，只看当前启用的分类）；只统计 429、5xx、401/403、网络错误等“真异常” | warning |
| `all_failing` | 所有未暂停的账号同时处于 `failure_spike` 或 `needs_attention`，且至少 2 个账号；单账号时不发（与 `failure_spike` 重复） | critical |
| 现有事件 | 不变；`needs_attention`、`auto_paused` 为 critical，`stale`、`secret_expiring` 为 warning，`recovered`、`period_ended` 为 info | — |

`failure_spike` 解除（最近 `spike_rounds` 轮恢复到阈值以下）时发 `recovered`（附带原事件名）。

## 2. 降噪

- **冷却**：同一账号的同一事件在 `cooldown`（默认 6h）内不重复发送；`critical` 事件的冷却为 `cooldown / 2`。
  状态记在 `state.json` 的 notices 里（沿用现有 `store.Notice`）。
- **免打扰**：`quiet_hours = "23:00-08:00"`（可选，空表示关闭）。期间只发送 critical；被压下的 warning/info 在免打扰结束后**汇总成一条**发送。
- **按渠道过滤**：每个渠道可设最低级别（info / warning / critical）和事件列表（空 = 全部）。

## 3. 渠道

配置（`config.toml`）只存非敏感字段；所有密钥（Token、密码、Webhook 地址、签名密钥）存 vault，键名 `notify/<channel-id>/<field>`。
现有的 `notify/webhook` 迁移为一个 id 为 `webhook` 的通用 Webhook 渠道（读取兼容，写入新格式）。

```toml
[[notify.channels]]
id = "tg"                 # 用户起的名字，[a-z0-9-]
type = "telegram"
min_level = "warning"
events = []
language = "zh"           # 模板语言：zh / en；空 = 跟随界面
template = ""             # 空 = 内置模板；否则为自定义 Go text/template
# 非敏感字段，例如 telegram 的 chat_id、邮件的 smtp_host/port/from/to
chat_id = "123456789"
```

| type | 敏感字段（vault） | 普通字段 | 发送方式 |
|---|---|---|---|
| `telegram` | `bot_token` | `chat_id`、`api_base`（可选，自建反代） | `POST https://api.telegram.org/bot<token>/sendMessage`，`parse_mode=HTML` |
| `email` | `password` | `smtp_host`、`smtp_port`、`security`（`starttls`/`tls`/`none`）、`username`、`from`、`to`（多个） | SMTP，`multipart/alternative`（纯文本 + HTML）。预设：Gmail、Outlook、QQ、163 |
| `wecom` | `url` | — | 企业微信群机器人，`msgtype=markdown` |
| `feishu` | `url`、`secret`（可选签名） | — | 飞书机器人，`msg_type=post`，带签名时加 `timestamp`+`sign` |
| `dingtalk` | `url`、`secret`（可选加签） | — | 钉钉机器人，`msgtype=markdown`，加签放在 URL 上 |
| `discord` | `url` | — | `{"content": …}` 或 embeds |
| `slack` | `url` | — | Incoming Webhook，`{"text": …}`（mrkdwn） |
| `bark` | `device_key` | `server`（默认 `https://api.day.app`） | `POST /push`，`title`、`body`、`level`（critical→`critical`） |
| `ntfy` | `token`（可选） | `server`（默认 `https://ntfy.sh`）、`topic` | `POST /<topic>`，`Title`/`Priority`/`Tags` 头 |
| `serverchan` | `sendkey` | — | `POST https://sctapi.ftqq.com/<sendkey>.send`，`title`、`desp` |
| `webhook` | `url` | `format`（`json` 默认 / `template`） | 现有 JSON；`template` 时按自定义模板生成请求体 |

- 所有出站请求复用现有的 HTTP 客户端（代理设置、超时、`DeliveryError` 的重试分类）；不跟随重定向（现有规则）。
- 只允许 https（`email` 除外；`ntfy`/`bark` 自建服务器允许 http 但 `doctor` 警告）。
- 失败重试沿用现有退避；某渠道连续失败不影响其他渠道。

## 4. 模板

- 内置模板按 `type × language` 提供，放在 `internal/notify/templates/*.tmpl`（`embed`）。每个渠道的格式：
  Telegram HTML、企业微信/钉钉 markdown、飞书 post、Slack mrkdwn、邮件 HTML+纯文本、其余纯文本（标题 + 正文）。
- 变量：`.Event`、`.Level`、`.Account`、`.Tenant`（可选显示）、`.Message`、`.Time`（本地时间）、`.Host`（机器名）、
  `.FailRate`、`.Failures`、`.Rounds`（`failure_spike` 时有）、`.Hint`（下一步建议，来自 `ops.Hint`）、`.App`（产品名）、
  `.Digest`（免打扰汇总时的事件列表）。
- 自定义模板用 Go `text/template`（`html/template` 用于 HTML 邮件），保存前校验能否解析、能否用示例数据渲染。
- 消息里**永远不包含**密钥、令牌、Webhook 地址；租户 ID 默认不显示。

## 5. 命令行

```
e5extend notify channel list
e5extend notify channel add <id> --type telegram [--chat-id … --min-level … --events … --language …]
                                                  # 敏感字段从终端读取（不回显），或 stdin 一行
e5extend notify channel edit <id> …
e5extend notify channel remove <id>
e5extend notify test [<id>]                       # 发送测试消息（全部或指定渠道）
e5extend notify preview <event> [--type … --language …]   # 用示例数据打印渲染结果，不发送
```

`notify set-webhook` 保留为 `channel add webhook --type webhook` 的别名。

## 6. 桌面版

设置页“高级设置 → 告警”：

- 渠道列表（类型图标、名称、最低级别、最近一次发送结果），“添加渠道”按类型分组选择，表单按类型显示字段；敏感字段只显示“已设置”。
- 每个渠道：“发送测试”“预览消息”“编辑”“删除”。
- 降噪：冷却时间、免打扰时段、`failure_spike` 阈值。
- 基础设置页只在没有任何渠道时显示一行提示“还没有设置通知渠道”，带跳转按钮。

## 7. 测试

- 判定：偶发失败不触发；全是 404 的分类不触发；429/5xx 达到阈值触发；恢复发 `recovered`；`all_failing` 只在 ≥2 个账号全部异常时触发。
- 冷却、免打扰（含跨午夜）、汇总、按渠道级别/事件过滤。
- 每种渠道：用 `httptest` 假服务器校验请求方法、路径、头、请求体（含飞书/钉钉签名）；邮件用假 SMTP 服务器校验 MIME。
- 模板：每个内置模板都能用示例数据渲染；自定义模板错误在保存时报出；渲染结果不含密钥。
- 旧配置 `notify/webhook` 的兼容。

## 8. 实现时的取舍

- **“已知不可用”的判定**放在 `internal/callstat`，`doctor` 的 `account.<name>.apis` 和 `failure_spike` 共用 `IsMissing`、`CategoryIndex`、`EnabledCategories`。`failure_spike` 把这类调用从失败数和总数里都去掉；日志里不足 `spike_rounds` 轮时既不触发也不解除。
- **日志读取**：引擎在评估告警前读最近 48 小时的调用记录（`Input.Calls`）；读不出时传 nil，`failure_spike` 本次不判断。
- **冷却**只管会反复出现的事件：`needs_attention`、`auto_paused`、`failure_spike`、`all_failing`（严重级别减半）。`stale` 有自己的 24 小时规则，`secret_expiring` 按档位只发一次，`recovered`、`period_ended` 是一次性的。上次发送时间记在 `LastSent`（账号状态里，全局事件记在 `state.json` 顶层），条件消失后也保留；被冷却吞掉的问题不再发 `recovered`。
- **免打扰**：被压下的告警在 `Notice` 里带 `Held`，结束后合并成一个 `digest` 事件（`Items` 是原事件）；只压下一条时按原样发。每个渠道按自己的级别和事件过滤裁剪汇总。
- **多渠道的送达语义**：各渠道并发发送；至少一个渠道送达（或没有渠道需要这条）就算已发送，全部失败才在 `RetryAfter` 后重发，避免已送达的渠道收到重复消息。每个渠道最近一次结果记在数据目录的 `notify-status.json`，供渠道列表显示（只有状态和不含密钥的错误文字）。
- **https**：渠道地址要求 https；指向本机（`localhost`、回环地址）的 http 也接受。旧格式的 Webhook（`notify/webhook` 或 `config.toml` 里的明文）保持原来对 http 的宽容，`doctor` 警告。
- **租户 ID**：事件带 `Tenant`，只有自定义模板能用 `.Tenant`；内置模板从不显示。
- **`.Hint`** 由 `notify.Hint(事件, 账号, 语言)` 给出；`ops.Hint` 的输入是登录错误，不适用于事件，且 `ops` 依赖 `notify`，所以没有复用。
- **邮件**只用标准库：`net/smtp`（STARTTLS、隐式 TLS、AUTH PLAIN/LOGIN），`mime/quotedprintable`，`multipart/alternative`；SMTP 不走 HTTP 代理。

