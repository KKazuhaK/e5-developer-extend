# 五分钟上手

## 它能做什么，不能保证什么

e5renew 在后台定时、随机地调用 Microsoft Graph 的**只读**接口（读邮件列表、日历、文件信息等），
让 Microsoft 365 E5 开发者租户保持有开发活动。

- 微软没有公开续期的判定标准，**不保证**订阅一定能续期。
- 电脑关机或睡眠期间不会发出调用。长期使用建议放在常开的机器或服务器上。
- 它只读取，不写入、不发送、不删除任何数据；响应内容不会被保存。

## 你需要准备

1. 一个 E5 开发者租户，以及租户里的一个应用注册（Client ID）。步骤见 [app-registration.md](app-registration.md)。
2. 安装好的 e5renew，见 [install.md](install.md)。桌面版的安装包已经把命令行程序一起装好了；
   只用命令行时，把 `e5renew`（Windows 上还有不弹控制台窗口的 `e5renew-bg.exe`，两者放同一目录）放在一个固定的位置，
   例如 `~/.local/bin/` 或 `C:\Users\你\bin\`。**不要放在临时目录或“下载”文件夹**，后台服务会拒绝安装。

## 最快的方式：向导

```bash
e5renew init
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
9. 运行 `e5renew doctor`

## 手动步骤

```bash
e5renew config init
e5renew account add main --auth device_code --tenant organizations --client-id <你的应用 ID>
e5renew account login main          # 按提示打开网址、输入设备码
e5renew account test main           # 静默取令牌，列出已授予的权限
e5renew run-now main                # 立即跑一轮，打印每个调用的结果
e5renew service install             # 安装后台服务，见 background.md
e5renew doctor                      # 体检
```

`account add` 的 `--auth` 可以是 `device_code`、`browser`、`client_secret`、`password`。
`client_secret` 和 `password` 会提示输入密钥（不回显），存进加密的 vault，配置文件里没有任何密钥。

## 之后怎么看它在不在工作

```bash
e5renew status        # 引擎是否在运行、每个账号的状态、下次运行时间
e5renew logs -n 20    # 最近的调用记录
e5renew doctor        # 全面检查，退出码 0 正常、1 有警告、2 有失败
```

建议再设置告警，出问题时会推送到你的手机或聊天工具：

```bash
e5renew notify set-webhook <webhook 地址>
e5renew notify test
```

遇到问题先看 [troubleshooting.md](troubleshooting.md) 和 [faq.md](faq.md)。
