# 故障排除

先运行 `e5extend doctor`。每一行是一项检查：`✓` 正常，`!` 警告，`✗` 失败，下面缩进的 `fix:` 是修复命令。
退出码：全部正常为 0，有警告为 1，有失败为 2。`doctor --json` 输出 JSON 数组，`doctor --offline` 跳过联网检查。
（Windows 经典控制台里标记显示为 `ok` / `!!` / `xx`。）

## config

配置不存在或无效。`e5extend config init` 生成默认配置；`e5extend config validate` 列出所有问题。

## vault

读不出加密数据。
- **通过 SSH 等远程会话操作时**看到 “may not be reachable from this remote (SSH) session”：
  主密钥多半没丢，只是这个会话读不到系统钥匙串（Windows 用密钥方式登录 SSH 时不会加载凭据管理器；
  Linux 没有桌面会话时没有 Secret Service）。到桌面会话里执行，或者只在当前 shell 里设置 `E5EXTEND_MASTER_KEY`。
  **不要因此执行 `vault reset`。** 远程会话里程序也不会自动生成新密钥，以免和桌面会话里的那把冲突。
- “wrong master key”：换了机器或钥匙串被清空。恢复原来的主密钥（`E5EXTEND_MASTER_KEY` 或钥匙串）；
  实在找不回来，最后才用 `e5extend vault reset`（会删除所有已保存的密钥），然后重新输入密钥并登录。
- “no master key”：设置 `E5EXTEND_MASTER_KEY`（`e5extend vault init --print-key` 生成），或让服务读 key 文件
  （`service install --key-file`）。
- “the secret is missing”：`e5extend account set-secret <名称>`。

## engine

引擎没在运行（失败），或者在运行但心跳超过 2 分钟（警告，可能卡住）。
`e5extend service install` 安装后台服务，或在前台 `e5extend run`。看 `e5extend logs` 和服务日志。

## service

后台服务没安装（警告；如果你用别的方式启动 e5extend，可以忽略）、被禁用或没在运行。
`e5extend service install`，之后 `e5extend service status`。

## linger

仅 Linux。用户服务装了但没开 linger，退出登录后服务会停。`loginctl enable-linger $USER`。

## account.<名称>.token

静默取令牌失败。
- 需要登录（失败）：`e5extend account login <名称>`。
- `sign-in is cut off by a Conditional Access sign-in frequency policy (AADSTS70043)`（失败）：租户的条件访问“登录频率”策略把刷新令牌限制在几个小时内，
  重新登录只能再撑几个小时。按顺序：改用应用身份 `e5extend account add <名称>-app --auto --auth client_secret`（不受用户登录策略影响）；
  账号确实没有 MFA 时才用账号密码（登录频率策略常和 MFA 一起出现，多半不行，优先用应用身份）；
  或让管理员在条件访问里排除本应用注册，再 `e5extend account login <名称>`。详见 [常见问题](faq.md)。
- 凭据错误（失败）：Client Secret 错或过期，`e5extend account set-secret <名称>`；也检查 Client ID。
- 需要管理员同意（失败）：在应用注册的“API 权限”里点“授予管理员同意”。
- 网络问题（警告）：检查网络和代理（`network.proxy` 或 `HTTPS_PROXY`），通常会自己恢复。
- `sign-in failed (AADSTSnnnn)`：按错误码在微软文档里查。

## account.<名称>.scopes

令牌里的权限能调用的接口太少。0 个是失败，少于 5 个是警告。在应用注册里授予更多只读权限并同意，
用 `e5extend catalog list --account <名称>` 查看缺什么。

## account.<名称>.activity

距离上次成功调用超过 72 小时（警告）或 7 天（失败），或者从来没成功过（警告）。
`e5extend status`、`e5extend logs <名称>` 看原因，修好后 `e5extend run-now <名称>`。
电脑关机或睡眠、不在工作日或工作时段内都会让它变长。

## account.<名称>.state

显示账号的状态和原因：`needs_attention`（需要你处理，原因里有命令）、`paused`（连续 3 轮失败后自动暂停）、
配置里手动暂停、运行期限已结束。处理后 `e5extend account resume <名称>`。

## account.<名称>.apis

某个分类最近 3 轮的调用全部是 404（或 400）：通常是这个账号没有对应的服务（没有邮箱、没有 OneDrive、没有 Teams 许可）。
`e5extend account test <名称> --recommend` 看哪些分类可用，`--apply` 写进账号。这一项只读本地调用日志，`--offline` 时也会运行。

## account.<名称>.secret

Client Secret 还剩 30 天以内（警告）、7 天以内或已过期（失败）。在应用注册里创建新密钥，
然后 `e5extend account set-secret <名称> --secret-expires YYYY-MM-DD`。

## clock

本机时间和 Graph 响应头的 `Date` 相差超过 2 分钟（警告）或 5 分钟（失败）。打开系统的自动校时（NTP），检查时区。
时间不准会让登录令牌验证失败。

## network

连不上 `https://graph.microsoft.com/v1.0/`（返回 401 也算通）。检查网络、防火墙和代理。
需要代理时在配置里设置 `[network] proxy = "http://主机:端口"`，或设置 `HTTPS_PROXY`。
更换代理设置要重启引擎。

## notify

没有设置告警（警告）、Webhook 地址还以明文放在 `config.toml` 里（警告），或者 vault 里存的地址解不开（警告，通常是主密钥变了；重新 `notify set-webhook`）。
`e5extend notify set-webhook <地址>` 会把它存进加密的 vault，并清掉配置里的明文；`e5extend notify test` 发一条测试消息。

### 告警的类型

| 事件 | 什么时候发 |
|---|---|
| `needs_attention` | 账号进入需要处理的状态，带原因和建议 |
| `recovered` | 之后恢复正常 |
| `auto_paused` | 连续失败被自动暂停 |
| `stale` | 超过 `notify.stale_after`（默认 72 小时）没有成功调用，之后每 24 小时最多再提醒一次 |
| `secret_expiring` | Client Secret 到期前 30、7、1 天和到期当天各一次 |
| `period_ended` | 运行期限结束 |

只在状态变化时发送，同一个状态不会重复发。`notify.events` 可以限定只发其中几种（空表示全部）。
告警发送失败（地址不可读、网络不通）时不会算作已发送：每个未送达的告警最多每小时重试一次，送达后才不再重复。
`notify` 是否已设置每 5 分钟检查一次，所以刚设置完 Webhook 后，运行中的引擎最多 5 分钟才开始发送。
消息是 JSON：`{"event","account","message","time","text","content"}`，`text` 兼容 Slack、企业微信、飞书类机器人，
`content` 兼容 Discord。发送超时 10 秒，失败按指数退避重试 3 次，仍失败只记日志，不影响引擎。
