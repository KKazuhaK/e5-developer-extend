# 通知渠道

账号出问题时，e5extend 可以把告警发到你常看的地方：Telegram、邮件、企业微信、飞书、钉钉、Discord、Slack、Bark、ntfy、Server 酱，
或者你自己的 Webhook。可以同时设置多个渠道，每个渠道有自己的最低级别和事件列表。

- 令牌、密码、Webhook 地址、签名密钥这些**敏感信息只存在加密的 vault 里**（键名 `notify/<渠道名>/<字段>`），`config.toml` 里只有 chat id、SMTP 服务器这类普通设置。
- 命令行、桌面版、日志、错误信息和发出去的消息里**都不会出现**这些敏感信息；桌面版只显示“已设置”。
- 出站请求走和 Graph 调用相同的网络设置（代理、超时、不跟随重定向）。邮件走 SMTP，不使用 HTTP 代理。
- 除邮件外一律要求 `https://`；Bark 和 ntfy 的**自建服务器**可以用 `http://`（`e5extend doctor` 会警告令牌是明文传输）。另外，指向本机（`localhost`、`127.0.0.1`）的 `http://` 地址也接受，方便本机中转和调试。

## 五分钟设好一个渠道

```bash
e5extend notify channel add tg --type telegram --chat-id 123456789   # 然后按提示粘贴 Bot Token（输入时不回显）
e5extend notify test tg                                              # 发一条测试消息
e5extend notify preview failure_spike --type telegram                # 不发送，只看消息长什么样
```

桌面版：设置 → 高级设置 → 告警 → “添加渠道”，选类型，按页面上的提示填写，保存后点“发送测试”。

渠道名（上面的 `tg`）自己起，只能用小写字母、数字和 `-`。敏感字段从终端读取，也可以从标准输入读一行（脚本里用，每个敏感字段一行，顺序见下面各渠道）：

```bash
printf '%s\n' "$TOKEN" | e5extend notify channel add tg --type telegram --chat-id 123456789
```

## 各渠道怎么设置

### Telegram

1. 在 Telegram 里找 [@BotFather](https://t.me/BotFather)，发送 `/newbot`，按提示起名字，最后得到一串 **Bot Token**（形如 `123456:ABC-DEF...`）。
2. 找到你刚建的机器人，给它发一条任意消息（群里用就先把机器人拉进群，在群里发一条消息）。
3. 取 **chat id**：在浏览器里打开 `https://api.telegram.org/bot<你的Token>/getUpdates`，找到 `"chat":{"id":123456789,...}`，这个数字就是 chat id（群的 id 是负数，如 `-1001234567890`）。也可以对 [@userinfobot](https://t.me/userinfobot) 发消息查自己的 id。公开频道可以直接用 `@频道名`，机器人需要是频道管理员。
   这个地址里含有 Token，只在你自己的浏览器里打开，不要贴给别人。
4. 添加：

   ```bash
   e5extend notify channel add tg --type telegram --chat-id 123456789
   ```

在国内网络下 `api.telegram.org` 可能连不上：设置 `[network] proxy`，或者自己架一个 Bot API 的反向代理，用 `--api-base https://你的域名` 指向它。

敏感字段：`bot_token`。消息使用 HTML 格式（`parse_mode=HTML`），内容会自动转义。

### 邮件（SMTP）

```bash
e5extend notify channel add mail --type email --preset gmail \
  --username 你的地址@gmail.com --from 你的地址@gmail.com --to 收件地址@example.com
```

`--preset` 会填好服务器、端口和加密方式；之后可以用 `--smtp-host`、`--smtp-port`、`--security` 覆盖。敏感字段：`password`（提示时可以直接回车跳过，用于不需要登录的内网服务器）。
`--to` 可以重复，也可以用逗号分隔多个收件人。邮件是 `multipart/alternative`，同时带纯文本和 HTML 两个版本，主题和正文按 UTF-8 编码。

| 预设 | 服务器 | 端口 / 加密 | 密码填什么 |
|---|---|---|---|
| `gmail` | `smtp.gmail.com` | 587 / `starttls` | **应用专用密码**：Google 账号需要先开启两步验证，然后在 <https://myaccount.google.com/apppasswords> 生成 16 位密码；不是登录密码。用户名是完整的邮箱地址 |
| `outlook` | `smtp-mail.outlook.com` | 587 / `starttls` | 开启两步验证后生成的应用密码；单位的 Microsoft 365 邮箱默认可能关闭了 SMTP 验证，需要管理员开启。微软正在逐步取消基本验证方式的 SMTP，如果登录被拒绝，请改用其他渠道（这一点没有在真实账号上验证过） |
| `qq` | `smtp.qq.com` | 465 / `tls` | **授权码**：网页版 QQ 邮箱 → 设置 → 账号 → “POP3/IMAP/SMTP/Exchange/CardDAV 服务”→ 开启 SMTP 服务，按提示发短信，得到授权码；不是 QQ 密码。用户名是完整的 QQ 邮箱地址 |
| `163` | `smtp.163.com` | 465 / `tls` | **授权码**：网页版 163 邮箱 → 设置 → POP3/SMTP/IMAP → 开启 SMTP 服务，设置授权码。用户名是完整的 163 邮箱地址 |

加密方式：`starttls`（默认，先明文连接再升级，端口 587）、`tls`（一开始就加密，端口 465）、`none`（不加密，端口 25，`doctor` 会警告）。`none` 时，如果设置了用户名，密码只会发给本机；发给其他主机会被拒绝，除非你在 `config.toml` 的这个渠道里明确写 `allow_plain_auth = true`（不建议）。
`starttls` 要求服务器必须支持升级，不支持会报错，不会悄悄退回明文。

### 企业微信（群机器人）

1. 在企业微信群里：右上角 “…” → 添加群机器人 → 新建。
2. 复制机器人的 **Webhook 地址**（`https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...`）。
3. 添加：`e5extend notify channel add wecom --type wecom`，提示时粘贴地址。

消息用 `markdown` 类型；企业微信限制每个机器人每分钟 20 条、单条最长 4096 字节（超长会截断）。敏感字段：`url`。

### 飞书（自定义机器人）

1. 在飞书群里：设置 → 群机器人 → 添加机器人 → 自定义机器人。
2. 复制 **Webhook 地址**（`https://open.feishu.cn/open-apis/bot/v2/hook/...`）。
3. 安全设置建议选“签名校验”，复制**签名密钥**。不选签名也可以用（不填密钥即可），但任何知道地址的人都能往群里发消息。
4. 添加：`e5extend notify channel add feishu --type feishu`，依次粘贴地址和签名密钥（没有就回车跳过）。

消息用 `post`（富文本）类型。带签名时请求里会加上 `timestamp` 和 `sign`，所以**本机时间要准**（飞书只接受一小时内的时间戳；`doctor` 会检查时钟）。
“自定义关键词”和“IP 白名单”两种安全设置没有专门适配，建议用签名。敏感字段：`url`、`secret`（可选）。

### 钉钉（自定义机器人）

1. 在钉钉群里：群设置 → 智能群助手 → 添加机器人 → 自定义。
2. 安全设置选“**加签**”，复制 `SEC` 开头的密钥；复制 **Webhook 地址**（`https://oapi.dingtalk.com/robot/send?access_token=...`）。
3. 添加：`e5extend notify channel add dingtalk --type dingtalk`，依次粘贴地址和密钥。

签名会自动加在请求地址的查询参数里（`timestamp` 和 `sign`）。同样要求本机时间准确。消息用 `markdown` 类型。
如果你选的是“自定义关键词”，消息里必须包含这个关键词，否则会被拒绝；建议用加签。敏感字段：`url`、`secret`（可选）。

### Discord

频道设置 → 整合（Integrations）→ Webhooks → 新建 Webhook → 复制 Webhook URL。
`e5extend notify channel add dc --type discord`。消息发成普通文本（支持 Markdown），并且关闭了 `@everyone` / `@here` 提醒，所以消息文字里的 `@` 不会骚扰别人。敏感字段：`url`。

### Slack

1. 打开 <https://api.slack.com/apps> → Create New App → From scratch。
2. Features → Incoming Webhooks → 打开 → “Add New Webhook to Workspace”，选频道。
3. 复制 Webhook URL，`e5extend notify channel add sl --type slack`。

敏感字段：`url`。

### Bark（iOS 推送）

1. 在 iPhone 上安装 Bark，打开后首页给出形如 `https://api.day.app/<设备Key>/...` 的地址，中间那一段就是**设备 Key**。
2. `e5extend notify channel add bark --type bark`，提示时粘贴设备 Key。
3. 自建了 bark-server 的话加 `--server https://你的域名`（`http://` 可以用，但 `doctor` 会警告）。

严重级别（critical）的消息会以 Bark 的“重要警告”级别发送，其余是普通级别。Bark 的接口要求设备 Key 放在请求里，这是协议决定的；消息正文里不会出现它。敏感字段：`device_key`。

### ntfy

1. 选一个**不容易被猜到**的主题名（例如 `e5-8f3k29xq`）。公共服务器 `ntfy.sh` 上，知道主题名的人都能订阅，也能往里发。
2. 手机上安装 ntfy App，订阅这个主题。
3. `e5extend notify channel add ntfy --type ntfy --topic e5-8f3k29xq`；自建服务器加 `--server https://ntfy.example.com`，需要登录的话提示 `token` 时粘贴访问令牌（`tk_...`，没有就回车跳过）。

标题、优先级（严重=5、警告=4、其余=3）和图标标签通过请求头发送，中文标题按 RFC 2047 编码。敏感字段：`token`（可选）。

### Server 酱

1. 登录 <https://sct.ftqq.com>，在“SendKey”页面复制 **SendKey**（`SCT` 开头）。
2. `e5extend notify channel add sc --type serverchan`。

消息发送到 Server 酱（标题 + Markdown 正文），再由它转到你绑定的微信等通道。同时支持 Server 酱 Turbo 版（`SCT` 开头的 Key，发到 `sctapi.ftqq.com`）和 Server 酱³（`sctp<uid>t…` 形式的 Key，发到 `https://<uid>.push.ft07.com/send/<Key>.send`，uid 取自 Key 前缀）。敏感字段：`sendkey`。

### 通用 Webhook

把告警以 JSON 发到你自己的地址：`e5extend notify channel add hook --type webhook`。请求体：

```json
{"event":"failure_spike","account":"main","message":"…","time":"2026-11-16T20:44:00-08:00",
 "text":"[e5extend] main: …","content":"[e5extend] main: …","level":"warning"}
```

`text` 兼容 Slack、企业微信、飞书一类机器人，`content` 兼容 Discord；`level` 是 `info` / `warning` / `critical`；恢复通知有 `origin`（结束的事件名）；免打扰汇总有 `items` 数组。
订阅类事件（`subscription_*`）还有 `tenant`（租户 ID，读到之前是配置里写的租户）、`subscription`（订阅 ID，手动日期是 `manual`）、`sku`、`status`、`ends`（到期日，本机时区）；它们的 `account` 为空。
旧版本只有 `event`、`account`、`message`、`time`、`text`、`content` 六个字段，新字段都是追加的，原来的接收端不用改。

想自己决定请求体：加 `--format template --template-file body.tmpl`，模板的输出必须是合法的 JSON，例如：

```
{"msg": {{json .Message}}, "level": "{{.Level}}", "who": "{{.Account}}"}
```

`json` 函数把文本转成带引号、已转义的 JSON 字符串。敏感字段：`url`。

## 告警的事件和级别

| 事件 | 级别 | 什么时候发 |
|---|---|---|
| `needs_attention` | 严重 | 账号进入需要处理的状态（登录失效、权限被撤销等），带原因和建议 |
| `auto_paused` | 严重 | 连续失败被自动暂停 |
| `all_failing` | 严重 | 所有没暂停的账号同时处于 `failure_spike` 或 `needs_attention`，且至少 2 个账号；只有 1 个账号时不发（和它自己的告警重复） |
| `failure_spike` | 警告 | 调用失败率过高，见下 |
| `stale` | 警告 | 超过 `notify.stale_after`（默认 72 小时）没有成功调用，之后每 24 小时最多再提醒一次 |
| `secret_expiring` | 警告 | Client Secret 到期前 30、7、1 天和到期当天各一次 |
| `subscription_expiring` | 警告 / 严重 | 关注的订阅离到期（`nextLifecycleDateTime`）30 天提醒一次（警告），7 天和 1 天各一次（严重），到期当天还会再提醒；续期后重新计 |
| `subscription_status` | 严重 / 提示 | 订阅状态从 `Enabled` 变成 `Warning`、`Suspended`、`LockedOut` 或 `Deleted`（严重），或恢复为 `Enabled`（提示） |
| `subscription_renewed` | 提示 | 订阅的到期时间向后移动了，写明从哪天移到哪天 |
| `recovered` | 提示 | 之前的问题恢复了；`failure_spike` 解除时会写明“解除的是调用失败率过高” |
| `period_ended` | 提示 | 运行期限结束 |

订阅类事件属于租户而不是某个账号：消息和通用 Webhook 里的 `account` 为空，消息里写明 SKU 和租户（内置模板在租户 ID 与消息里的写法不同时会补上 ID；自定义模板可以用 `.Tenant`、`.SKU`、`.SubStatus`、`.EndDate`）。免打扰时段里严重的（7 天内到期、状态变坏）照常发，其余等到时段结束。详见 [订阅到期检测](getting-started.md#看订阅什么时候到期)。

### 什么算“调用失败率过高”（`failure_spike`）

看某个账号**最近 `spike_rounds` 轮**（默认 3）的调用，同时满足：

- 失败率 ≥ `spike_rate`（默认 50%）；
- 失败次数 ≥ `spike_min_failures`（默认 5）。

只算“真异常”：429 限流、5xx、401/403、网络错误等。**不算**：

- 返回 404、或表示“没有这项服务”的 400（比如账号没有邮箱、没有 OneDrive）：这是账号本身没有这项服务，不是故障，和 `e5extend doctor` 的 `account.<名称>.apis` 检查用的是同一个判断；
- 已经不在这个账号当前启用分类里的调用；
- 日志里还不够 `spike_rounds` 轮时（刚启动、刚改过配置）不判断，也不会误报恢复。

偶发的一两次失败不会触发。最近几轮都恢复到阈值以下，就发 `recovered`。账号整轮都失败时，更早会先触发既有的“连续 3 轮失败自动暂停”（`auto_paused`），所以 `failure_spike` 主要抓“每轮有一部分调用失败、账号还没被暂停”的情况。

## 降噪

在桌面版“告警 → 降噪”里设置，或写在 `config.toml` 的 `[notify]`：

| 设置 | 默认 | 说明 |
|---|---|---|
| `cooldown` | `6h` | 同一账号的同一告警在这段时间内不重复发送；严重告警冷却时间减半；留空表示默认值，只有 `0` 才关闭。管 `needs_attention`、`auto_paused`、`failure_spike`、`all_failing`、`subscription_status`；`stale`、`secret_expiring`、`subscription_expiring` 有各自的重复规则，`recovered`、`period_ended`、`subscription_renewed` 只发一次。被冷却压下的问题如果冷却结束时仍然存在，会在那时发出（时间显示的是它最初被发现的时间），之后恢复时正常发“已恢复”；冷却期间就已恢复的问题，既不发告警也不发“已恢复” |
| `quiet_hours` | 空 | 例如 `"23:00-08:00"`（本机时间，可以跨午夜）。期间只发严重告警；其余的暂存，免打扰结束后**合并成一条**发出（只暂存了一条就按原样发；免打扰期间已经恢复的问题，暂存的告警和它的“已恢复”都会丢弃，不会收到没发过的问题的“已恢复”） |
| `spike_rounds` / `spike_rate` / `spike_min_failures` | 3 / 0.5 / 5 | 见上 |
| `events` | 空（全部） | 全局只发这些事件 |

每个渠道还可以单独设置：**最低级别**（`--min-level info|warning|critical`）和**事件列表**（`--events failure_spike,recovered`，空表示全部）。
例如：手机推送（Bark）只要严重的，邮件收全部。汇总消息会按每个渠道的过滤条件分别裁剪。
`notify test` 总是发出，不受这些过滤条件影响。

### 消息太长时

各服务对单条消息有长度限制（Telegram 4096 字符、企业微信 4096 字节、Discord 2000 字符等）。免打扰汇总太长时，会在条目之间拆成几条各自完整的消息；单条消息本身太长则先截断原文再渲染，所以不会把 HTML 标签或实体切成两半。
消息里来自外部的文字（账号名、消息正文、机器名、建议）会按各渠道的格式转义：Slack 转义 `& < >`，Telegram 转义 HTML，企业微信、钉钉、Discord、Server 酱转义 Markdown 符号，`<!channel>`、`<url|x>`、`*粗体*` 都只会当作普通文字显示。

### 发送失败怎么办

- 每个渠道独立发送，一个渠道失败不影响其他渠道。
- 错误信息会带上服务返回的原因（例如 Telegram 的 `Bad Request: chat not found`），并去掉所有令牌和地址。
- `e5extend doctor` 会提醒“某个渠道最近连续 3 次发送失败”或“7 天内一次都没送达”，并写出渠道名。
- 每次发送失败后按 2、4、8 秒退避重试（只对临时性错误：网络错误、HTTP 429、5xx、SMTP 4xx）；密码错、地址不对这类永久性错误不重试。
- 一条告警只要**至少一个**渠道发出去就算已发送；全部渠道都失败，才会在一小时后重新发送。所以某个渠道一直失败时，请看桌面版渠道列表里的“最近一次发送结果”、`e5extend notify channel list` 或 `doctor`，它不会被自动补发。
- 对已有渠道的改动（换令牌、改过滤条件、改模板）立即被运行中的引擎看到，不用重启；从“没有任何渠道”到加上第一个渠道，引擎最多 5 分钟后才开始发送。

## 自定义消息模板

每种渠道、每种语言（`zh` / `en`）都有内置模板，按渠道的格式写好：Telegram 用 HTML，企业微信、钉钉、Server 酱用 Markdown，Slack 用 mrkdwn，飞书用富文本，邮件同时有纯文本和 HTML，其余是纯文本。语言默认跟随界面，也可以按渠道指定（`--language zh|en`）。

想改成自己的样子：用 `--template-file 文件` 指定一个 Go [text/template](https://pkg.go.dev/text/template)。保存前会检查**能否解析**、**能否用示例数据渲染所有事件**（中英文各一遍），有错直接拒绝保存，错误里会指出哪个事件、哪个字段。

可用变量：

| 变量 | 内容 |
|---|---|
| `.Event` / `.Level` | 事件名（如 `failure_spike`）/ 级别（`info` `warning` `critical`） |
| `.Account` / `.Tenant` | 账号名 / 租户 ID（**内置模板从不显示租户**；自定义模板里写 `.Tenant` 才会出现） |
| `.Message` | 发生了什么（英文原文，不含密钥） |
| `.Time` / `.Host` / `.App` | 本地时间 / 机器名 / 产品名 |
| `.FailRate` / `.Failures` / `.Rounds` | `failure_spike` 的失败率（如 `75%`）、失败次数、统计轮数 |
| `.Hint` | 下一步建议 |
| `.EventLabel` / `.LevelLabel` | 事件和级别的中文或英文名称 |
| `.Origin` / `.OriginLabel` | 恢复通知里，被解除的事件 |
| `.Digest` | 免打扰汇总时的事件列表，每项有 `.Event .EventLabel .Level .LevelLabel .Account .Message .Time .Hint`；单条消息时为空 |

模板可以定义三个块：`{{define "title"}}…{{end}}`（标题 / 邮件主题）、`{{define "body"}}…{{end}}`（正文）、`{{define "html"}}…{{end}}`（只用于邮件的 HTML 部分，按 `html/template` 自动转义）。
不写 `define` 时，整个文件就是正文；没写的块沿用内置模板（邮件没有 `html` 块时，用正文转义后放进 `<pre>`）。
Telegram 是 HTML 格式，自己写模板时用 `{{.Message | html}}` 转义；还有 `{{json x}}`、`{{trunc 100 .Message}}` 两个辅助函数。

```
{{define "title"}}[{{.Level}}] {{.Account}}{{end}}
{{define "body"}}{{.EventLabel}}：{{.Message}}
{{if .Rounds}}近 {{.Rounds}} 轮失败 {{.Failures}} 次（{{.FailRate}}）{{end}}{{end}}
```

先预览再保存：

```bash
e5extend notify preview failure_spike --type telegram --language zh --template-file my.tmpl
e5extend notify preview digest            # 用已设置渠道各自的模板预览；事件名可以是 test、digest 或任何告警事件
```

## 命令速查

```
e5extend notify channel list
e5extend notify channel add <名称> --type <类型> [--min-level … --events … --language … --template-file …] [类型自己的参数]
e5extend notify channel edit <名称> [只写要改的参数] [--secret <字段>] [--clear-secret <字段>] [--clear-template]
e5extend notify channel remove <名称>        # 同时从 vault 删除它的敏感信息
e5extend notify test [<名称>] [--event <事件> | --digest N]   # 不给名称就发给所有渠道，逐个报告结果；--event 发该事件的示例，--digest N 发 N 条的免打扰汇总示例（看长消息怎么拆）。示例带“[测试]”标记，走真实发送路径，不改变告警状态和冷却
e5extend notify preview <事件> [--type … --language … --template-file …]
e5extend notify set-webhook <地址>           # 旧命令，等价于设置名为 webhook 的通用 Webhook 渠道
```

类型自己的参数：`telegram`：`--chat-id`、`--api-base`；`email`：`--preset`、`--smtp-host`、`--smtp-port`、`--security`、`--username`、`--from`、`--to`；
`bark`：`--server`；`ntfy`：`--server`、`--topic`；`webhook`：`--format`。`edit` 默认保留已存的敏感信息，要换就加 `--secret 字段名` 重新输入。

## 从旧版本升级

以前只有一个 Webhook（`notify set-webhook`，地址存在 vault 的 `notify/webhook`，更早的版本是 `config.toml` 里的 `notify.webhook_url`）。升级后：

- 它**继续工作**，在渠道列表里显示为名为 `webhook` 的通用 Webhook 渠道（标有“旧格式”）；旧地址即使是 `http://` 也照常使用，`doctor` 会提醒。迁移成新格式时，如果旧地址是 `http://`，会在该渠道里写上 `allow_http = true`（仅通用 Webhook 渠道可用），之后改其他设置不会让它失效；`doctor` 会一直警告，换成 https 地址后可以删掉这一行。
- 在桌面版里编辑保存它一次，或再运行一次 `notify set-webhook`，就会迁移成新格式（vault 里变成 `notify/webhook/url`，旧条目和明文配置被清除）。
- 新添加的渠道只写新格式。备份导出、导入包含所有渠道的敏感信息；`--merge` 导入保留本机原有的渠道设置。

## 排错

| 现象 | 看哪里 |
|---|---|
| 不知道发没发出去 | `e5extend notify channel list` 的“最近一次发送”列；桌面版渠道列表同样显示 |
| `e5extend notify test` 报 `HTTP 401/403/404` | 令牌或地址不对，重新设置：`channel edit <名称> --secret <字段>` |
| 飞书、钉钉报签名错误或时间戳错误 | 本机时间不准，先校准（`e5extend doctor` 会检查） |
| 钉钉报“关键词不匹配” | 机器人安全设置选了关键词，改成加签 |
| 邮件报 `535` | 密码错了：Gmail、QQ、163 要用应用专用密码 / 授权码；用户名要写完整邮箱地址 |
| 邮件连不上 | 检查端口和加密方式（587 + `starttls`，465 + `tls`）；防火墙是否放行出站 SMTP |
| 收不到 `failure_spike` | 失败的是不是 404？日志里是否已有足够的轮数？见上面的判定；调低 `spike_min_failures` |
| 夜里没有收到警告级别的告警 | 免打扰时段内只发严重告警，其余在结束后合并发送 |
