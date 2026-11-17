# 常见问题

## 电脑睡眠时会跑吗？

不会。睡眠和关机期间不会发出任何调用，e5extend 也**不补跑**错过的轮次。唤醒后它会检测到时间跳跃，
等 1 到 3 分钟（让网络恢复）再继续。想要持续有活动，请用常开的机器或服务器。

## 关机多久会出问题？

两方面：

- **开发者订阅是否续期**：微软没有公开标准，无法给出确切的天数。`doctor` 在 72 小时没有成功调用时警告，7 天时报失败。
- **登录会过期**：委托身份的刷新令牌闲置 90 天后失效（每次使用会顺延）。停机超过 90 天就要重新 `account login`。

## 令牌过期怎么办？

访问令牌（约 1 小时）由程序自动刷新，你不用管。如果刷新失败，账号会进入 `needs_attention` 状态并发告警，
原因里带着下一步命令，通常是 `e5extend account login <名称>`。client_secret 的密钥有固定到期日，
记得用 `--secret-expires` 登记，到期前会提醒；换新密钥用 `e5extend account set-secret`。
修好之后 `e5extend account resume <名称>` 可以立刻恢复。

## 怎么确认它真的在工作？

```bash
e5extend status          # 每个账号的“上次成功”和今天的成功/失败次数
e5extend logs -f         # 实时查看调用记录
e5extend doctor          # 全面检查
```

数据目录里还有 `heartbeat` 文件，引擎每 30 秒更新一次，可以接到 Uptime Kuma 之类的外部监控。
再配上 Webhook 告警（`e5extend notify set-webhook`），出问题时会主动通知你。

## 为什么有的 API 一直是 403？

权限不足、账号没有对应的许可证或功能没开。同一个接口连续 3 次返回 403，e5extend 会跳过它 7 天，之后再试。
这不影响其他接口。`e5extend catalog list --account <名称>` 显示每个接口是否可用，
需要更多接口就在应用注册里授予更多只读权限并同意。

## 为什么邮件、日历、OneDrive 一直是 404？

账号没有对应的服务：没分配 Exchange Online 许可就没有邮箱（邮件、日历、联系人、待办都会 404），没有 OneDrive 许可则文件和 OneNote 会 404 / 400。
这和 403（权限不够）不同，加权限也没用。运行 `e5extend account test <名称> --recommend` 看哪些分类可用，
加 `--apply` 把可用的分类写进账号；桌面版在编辑账号的“API 分类”一步里有同样的检查。

## 会不会保证续期？

不会。这个工具只是让租户有规律的、看起来自然的开发活动，是否续期由微软决定。

## 我的数据安全吗？

- 配置文件里不放 Client Secret、密码和令牌；它们和令牌缓存、Webhook 地址一起放在 AES-256-GCM 加密的 vault 里。唯一的例外是旧版本的 `notify.webhook_url`：它可能还以明文留在 `config.toml` 里，`doctor` 会警告，`notify set-webhook` 会把它迁进 vault 并清掉。
- 只调用只读接口，不保存响应内容；日志里只有接口名、状态码和耗时。
- 主密钥丢失后 vault 无法读取，需要 `e5extend vault reset` 并重新输入密钥、重新登录。
