# 五分钟上手

## 它能做什么，不能保证什么

e5extend 在后台定时、随机地调用 Microsoft Graph 的**只读**接口（读邮件列表、日历、文件信息等），
让 Microsoft 365 E5 开发者租户保持有开发活动。

- 微软没有公开续期的判定标准，**不保证**订阅一定能续期。
- 电脑关机或睡眠期间不会发出调用。长期使用建议放在常开的机器或服务器上。
- 它只读取，不写入、不发送、不删除任何数据；响应内容不会被保存。

## 你需要准备

1. 一个 E5 开发者租户，以及租户里的一个应用注册（Client ID）。可以让 e5extend 自动创建（用租户管理员账号登录一次），也可以自己手动注册；步骤见 [app-registration.md](app-registration.md)。
2. 安装好的 e5extend，见 [install.md](install.md)。桌面版的安装包已经把命令行程序一起装好了；
   只用命令行时，把 `e5extend`（Windows 上还有不弹控制台窗口的 `e5extend-bg.exe`，两者放同一目录）放在一个固定的位置，
   例如 `~/.local/bin/` 或 `C:\Users\你\bin\`。**不要放在临时目录或“下载”文件夹**，后台服务会拒绝安装。

## 最快的方式：向导

```bash
e5extend init
```

向导需要在交互式终端里运行，每一步都可以跳过，已经做完的步骤会自动跳过。它依次做：

1. 生成默认配置
2. 选择主密钥的保存方式（系统钥匙串；没有桌面的服务器选“生成密钥并显示”）
3. 添加账号（登录方式、租户、Client ID）
4. 登录
5. 静默取令牌并统计可用的 API 数量
6. 立即跑一轮
7. 设置 Webhook 告警（可跳过）
8. 安装后台服务
9. 运行 `e5extend doctor`

## 手动步骤

```bash
e5extend config init
e5extend account add main --auth device_code --tenant organizations --client-id <你的应用 ID>
e5extend account login main          # 按提示打开网址、输入设备码
e5extend account test main           # 静默取令牌，列出已授予的权限
e5extend run-now main                # 立即跑一轮，打印每个调用的结果
e5extend service install             # 安装后台服务，见 background.md
e5extend doctor                      # 体检
```

`account add` 的 `--auth` 可以是 `device_code`、`browser`、`client_secret`、`password`。
`client_secret` 和 `password` 会提示输入密钥（不回显），存进加密的 vault，配置文件里没有任何密钥。

## 选对 API 分类

不是每个账号都有邮箱、OneDrive 或 Teams：没有 Exchange 邮箱的账号调邮件、日历、联系人、待办只会得到 404 / 400，每一轮都白白失败。
登录之后让 e5extend 查一下这个账号实际能用什么：

```bash
e5extend account test main --recommend            # 列出每个分类：recommended / unavailable / maybe，以及原因，并给出建议的 categories
e5extend account test main --recommend --apply    # 把推荐的分类写进账号（会先问你；--yes 跳过确认）
```

检查只发几个只读请求（邮箱设置、OneDrive、Teams、SharePoint 根站点），不会写入任何东西。
`maybe` 表示暂时确认不了（网络错误、需要管理员角色的报告）。`--apply` 不会因为 `maybe` 去掉账号已有的分类；只要有探测暂时失败，它会拒绝写入（稍后再试，或加 `--force`）。

如果建议的分类正好是默认的那一组，账号的 `categories` 会保持为空，以后新增的默认分类会自动生效；否则会写成明确的列表，
这种列表**不会**自动带上以后新增的分类，更新 e5extend 之后可以再运行一次 `--recommend --apply`。

桌面版的添加账号向导把“API 分类”放在登录之后：每个分类带“推荐 / 不可用 / 可能可用”的标记，并默认勾选推荐的；
编辑已有账号时也一样（还没登录就只列出分类并提示先登录）。
运行一段时间后，如果某个分类连续 3 轮都是 404，`e5extend doctor` 会提醒你再检查一次。

## 看订阅什么时候到期

E5 开发者订阅到期前会有提醒，状态变化和续期也会通知你。引擎每天（启动并跑完第一轮之后）读一次租户的订阅信息
（`GET /directory/subscriptions`），同一租户的多个账号只读一次。命令行：

```bash
e5extend subscription            # 立即读取并列出：SKU、状态、到期日、剩余天数、是否试用、续期记录
e5extend subscription --cached   # 不联网，只看引擎上次保存的结果
```

桌面版概览页有“E5 订阅”卡片：每个租户一行，展开可以看续期记录。只有 SKU 以 `DEVELOPERPACK` 开头的订阅会提醒，想关注别的 SKU 在 `config.toml` 里写
`[subscription] skus = ["DEVELOPERPACK", "ENTERPRISEPREMIUM"]`（每项按前缀匹配）。

**需要权限 `Organization.Read.All`**（委托和应用身份都是，需要管理员同意；应用身份的推荐权限里已有，委托身份的推荐权限新加了它）。
没有这个权限时卡片和命令行会说明原因，不算错误也不告警。补齐的办法：重新运行 `e5extend app setup`（自动模式会补齐权限并授予同意），
或在 Entra 的“API 权限”里手动添加后授予管理员同意；桌面版卡片上的“补齐权限”按钮会打开自动创建向导。委托账号登录的用户还需要能读目录，开发者租户的管理员满足。

暂时补不了权限时，可以手动填写到期日：卡片上的“手动填写到期日”，或 `e5extend subscription <账号> --set-expires 2027-02-14`
（配置里是 `subscription_expires = "2027-02-14"`，属于租户，写在该租户的任一账号上）。有手动日期时照常提醒；读到真实数据后以真实数据为准，并提示你可以删掉它。

这只是报告事实（日期、状态、续期记录）；微软没有公开“会不会续期”的判定，见 [常见问题](faq.md#会不会保证续期)。

## 之后怎么看它在不在工作

```bash
e5extend status        # 引擎是否在运行、每个账号的状态、下次运行时间
e5extend logs -n 20    # 最近的调用记录
e5extend doctor        # 全面检查，退出码 0 正常、1 有警告、2 有失败
```

建议再设置告警，出问题时会推送到你的手机或聊天工具：

```bash
e5extend notify channel add tg --type telegram --chat-id <你的 chat id>   # Telegram、邮件、企业微信、飞书等都可以
e5extend notify test
```

各渠道怎么设置、怎么降噪见 [notifications.md](notifications.md)。

遇到问题先看 [troubleshooting.md](troubleshooting.md) 和 [faq.md](faq.md)。
