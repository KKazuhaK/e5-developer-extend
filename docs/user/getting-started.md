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

桌面版的添加账号向导把“API 分类”放在登录之后：每个分类带“推荐 / 不可用 / 可能可用”的标记，并默认勾选推荐的；
编辑已有账号时也一样（还没登录就只列出分类并提示先登录）。
运行一段时间后，如果某个分类连续 3 轮都是 404，`e5extend doctor` 会提醒你再检查一次。

## 之后怎么看它在不在工作

```bash
e5extend status        # 引擎是否在运行、每个账号的状态、下次运行时间
e5extend logs -n 20    # 最近的调用记录
e5extend doctor        # 全面检查，退出码 0 正常、1 有警告、2 有失败
```

建议再设置告警，出问题时会推送到你的手机或聊天工具：

```bash
e5extend notify set-webhook <webhook 地址>
e5extend notify test
```

遇到问题先看 [troubleshooting.md](troubleshooting.md) 和 [faq.md](faq.md)。
