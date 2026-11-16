# 让它一直在后台跑

`e5renew service install` 在当前用户下注册一个后台任务：登录后自动启动，崩溃后自动重启。

```text
e5renew service install [--key-file] [--enable-linger]
e5renew service uninstall
e5renew service status
```

加 `--print` 只显示将要写入的文件和执行的命令，不做任何修改。

可执行文件必须在固定位置：`go run` 的构建目录、`/tmp` 等临时目录会被拒绝。
安装后不要移动或删除这个文件；升级时覆盖同一路径，再重启服务。

## Linux 桌面

```bash
e5renew service install
e5renew service status
e5renew doctor
```

写入 `~/.config/systemd/user/e5renew.service`（`Type=notify`、`Restart=on-failure`、`WatchdogSec=120`），
然后执行 `systemctl --user daemon-reload` 和 `systemctl --user enable --now e5renew.service`。
重新安装时还会执行 `systemctl --user restart e5renew.service`，让新的 unit 生效。
引擎卡死时，systemd 的看门狗会自动重启它。unit 里没有 `network-online.target`（用户级 systemd 里没有这个 target），
断网时引擎自己重试。

登录桌面后服务可以访问系统钥匙串，不需要 `--key-file`。

## Linux 服务器（无桌面）

服务器上有两件事要处理：

1. **linger**：默认情况下用户退出登录后，用户服务会被停掉。需要执行 `loginctl enable-linger $USER`。
   `service install --enable-linger` 会替你执行；失败时会打印需要 sudo 的命令。不开的话安装结果里会有醒目的警告，
   `doctor` 的 `linger` 项也会报警。
2. **主密钥**：systemd 用户服务通常访问不到钥匙串（Secret Service）。用 `--key-file`：

```bash
e5renew service install --key-file --enable-linger
```

`--key-file` 把当前主密钥写到 `<配置目录>/master.key`（权限 0600），并在 unit 里用 `LoadCredential=` 交给服务。
`LoadCredential=` 在用户服务里的支持：systemd NEWS 里最早明确提到用户服务可以用凭据的是 v258（加密凭据）；
在 systemd 259 上实测 `systemd-run --user --property=LoadCredential=...` 可用，`$CREDENTIALS_DIRECTORY` 里的文件权限为 0400。
安装时 `systemctl --version` 低于 258 的话，改为在 unit 里写 `Environment=E5RENEW_MASTER_KEY_FILE=<配置目录>/master.key`，
程序直接读这个文件（同样只靠文件权限保护），并给出提示。`--print` 不查询 systemd，总是显示 `LoadCredential=` 的形式。
如果向导里的主密钥来自环境变量，`init` 会自动为服务加上 `--key-file`。
**取舍**：密钥文件只靠文件权限保护，能读到你家目录的人（包括 root）就能解开 vault。
如果 `vault.backend` 是 `keyring`，key 文件不会被读取，需要先改成 `auto` 或 `file`。
shell 里设置的 `E5RENEW_MASTER_KEY` 不会传给服务，同样要用 `--key-file`。

卸载（`service uninstall`）不会删除 `master.key`，因为它可能是 vault 密钥的唯一副本；不需要时请自己删除。

验证：

```bash
e5renew service status      # installed / enabled / running / linger 都应该是 yes
systemctl --user status e5renew
journalctl --user -u e5renew -f
e5renew doctor
```

SSH 登录时如果报 `Failed to connect to bus`，执行 `export XDG_RUNTIME_DIR=/run/user/$(id -u)` 后重试。
系统级服务（`--system`）暂未实现。

## macOS

```bash
e5renew service install
```

写入 `~/Library/LaunchAgents/io.github.kkazuhak.e5renew.plist`（`RunAtLoad`、`KeepAlive`、`ThrottleInterval 30`，
`ProcessType` 为 `Standard`），然后依次执行 `launchctl enable gui/<uid>/io.github.kkazuhak.e5renew` 和
`launchctl bootstrap gui/<uid> <plist>`（被禁用的 label 不能 bootstrap，所以先 enable）。
已经装过时会先 `launchctl bootout`，稍等后再 bootstrap；bootstrap 失败会再试一次（旧进程可能还没退出）。
运行日志（带轮转）在 `~/Library/Application Support/e5renew/engine.log`（`run --log-file`）；
`~/Library/Logs/e5renew/engine.log` 只接收启动早期的崩溃输出。

要在 Mac 本机的终端里执行（需要已登录的桌面会话）；通过 SSH 执行可能会失败。
登录会话里可以访问钥匙串，不需要 `--key-file`。合上盖子睡眠时程序不会运行。

验证：`e5renew service status`、`launchctl print gui/$(id -u)/io.github.kkazuhak.e5renew`、`e5renew doctor`。

## Windows

```powershell
e5renew service install
```

注册一个名为 `e5renew` 的**计划任务**：当前用户登录时运行，没有 72 小时的执行时限，使用电池时也不停止，
失败后每分钟重启一次（最多 999 次）。另外每 5 分钟（无限期）触发一次，已在运行时忽略（`IgnoreNew`，引擎自己的单实例锁也会拒绝重复启动），
所以进程崩溃后最迟 5 分钟内会被重新拉起。重新安装时先结束旧实例再启动。

- 优先使用同目录下的 `e5renew-bg.exe`（窗口子系统版本，不弹控制台窗口）。找不到时会改用 `e5renew.exe`，
  并警告每次启动都会出现一个控制台窗口。请把两个文件放在同一目录。发布包出来之前，自己编译：
  `GOOS=windows go build -ldflags -H=windowsgui -o e5renew-bg.exe ./cmd/e5renew`（在 Windows 上用 PowerShell 设置 `$env:GOOS` 可省略）。
- 任务没有控制台，运行日志写到 `%LOCALAPPDATA%\e5renew\engine.log`（超过约 5 MB 时转存为 `engine.log.1`）；程序崩溃时的堆栈另外写到同目录的 `engine-crash.log`。
- 任务定义的 XML 保存在 `%LOCALAPPDATA%\e5renew\service\e5renew-task.xml`。
- 任务以你自己的身份运行，可以读取凭据管理器里的主密钥。`--key-file` 不适用于 Windows。
- 计划任务没法设置环境变量。如果你在安装时用了 `E5RENEW_CONFIG_DIR` / `E5RENEW_DATA_DIR`，安装程序会把这两个目录改成 `--config-dir` / `--data-dir` 参数写进任务，效果相同。

验证：`e5renew service status`、`schtasks /Query /TN e5renew`、`e5renew doctor`。
Windows 服务（LocalSystem）形式暂未实现。

## 桌面应用（托盘）

桌面应用的托盘模式在没有后台服务时，会在托盘进程里运行引擎。**一旦安装了后台服务，托盘就只监视**：它显示状态，但不运行引擎，也不会去抢引擎的锁。
如果服务已安装但引擎没有在运行，托盘图标会变成警告，菜单里有“检查问题…”可以打开健康检查页面；修好服务（`e5renew service status`、`e5renew doctor`）后警告会自行消失。

## Docker

见 [docker.md](docker.md)。验证：`docker compose logs -f`、`docker compose exec` 不可用（镜像没有 shell），
可以用 `docker compose run --rm e5renew status` 查看状态。
