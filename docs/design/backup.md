# 备份与迁移（单文件）

目标：把一台机器上的 e5extend 整体搬到另一台机器（可以跨平台），只用**一个文件**，
导入后不需要重新登录、不需要重新输入 secret。

## 1. 为什么不是把存储改成数据库

真正挡住迁移的是**主密钥**：vault 里的东西用主密钥加密，主密钥在钥匙串 / 凭据管理器 / key file 里，
不跟着数据文件走。把存储换成 SQLite 只是把多个文件变成一个，拷过去仍然解不开。
所以做法是**导出时用主密钥解开，再用用户设定的备份密码整体加密**；导入时反过来，用新机器自己的主密钥重新加密。
现有存储格式不变。

## 2. 命令

```
e5extend backup export <file> [--with-logs] [--force]
e5extend backup import <file> [--merge | --replace]
```

- 备份密码用 `readSecret` 读取（终端不回显；非终端时读 stdin 一行，方便脚本）。导出时在终端上要求输入两次。
  **不支持**从环境变量读密码（容易泄露到进程列表和 shell 历史）。
- 备份密码至少 8 个字符。
- 导出的文件已存在时拒绝，除非 `--force`。原子写入（`paths.WriteFileAtomicInPlace`：和 `WriteFileAtomic` 一样先写同目录的临时文件再改名，
  但目录是用户选的，所以不创建它、也不改它的权限）。Unix 上文件权限 0600；**Windows 上文件继承所在文件夹的权限**
  （Go 的 0600 在 Windows 上不起作用），所以建议导出到只有自己能访问的文件夹。
- 建议扩展名 `.e5backup`，但不强制。

## 3. 文件格式（版本 1）

```
magic      8 字节   "E5XTDBAK"
version    1 字节   1
kdf        1 字节   1 = PBKDF2-HMAC-SHA256（Go 标准库 crypto/pbkdf2）
iterations 4 字节   大端 uint32，导出时用 600000
salt       16 字节  随机
nonce      12 字节  随机
ciphertext 其余     AES-256-GCM(key = PBKDF2(密码, salt, iterations, 32), nonce, payload, AAD = 前面全部头部字节)
```

- 导入时：magic 不对 → “不是 e5extend 备份文件”；version / kdf 不认识 → “备份来自更新的版本，请先升级”；
  iterations 不在 [100000, 10000000] → 当作损坏（防止恶意文件让导入卡死）；文件大于 64 MiB → 拒绝。
- **解压后的 payload 上限 96 MiB**（常量 `backup.MaxPayload`，导出和导入共用）。导入时解压最多读 96 MiB + 1 字节，
  超过就报 `ErrPayloadTooLarge`（“备份内容超过 96 MiB 上限，请不带调用记录重新导出”），所以一个很小的恶意文件不能在内存里膨胀。
  导出时在压缩前就检查未压缩 payload 的大小，超过同样报这个错并建议不带调用记录，**不会**写出一个导入时会被当作“已损坏”的文件。
- GCM 校验失败统一报“备份密码不对，或文件已损坏”（两者无法区分，也不应区分）。
- payload 是 gzip 压缩的 JSON：

```json
{
  "format": 1,
  "created": "2026-11-16T17:00:00-08:00",
  "app_version": "0.2.0",
  "source_os": "linux",
  "config": "<config.toml 原文>",
  "vault": { "secret/main": "<base64 明文>", "tokencache/main": "<base64>", "notify/webhook": "<base64>" },
  "state": { ... state.json 原样 ... } 或 null,
  "gui": { ... gui.json 原样 ... } 或 null,
  "logs": { "calls-2026-11-16.jsonl": "<原文>" } 或 null
}
```

## 4. 导出

1. 读配置（`LoadConfig`），打开 vault。
2. 枚举 vault 里的**全部**条目（给 `vault.Vault` 加 `List() ([]string, error)`：遍历目录下的 `*.bin`，返回键名，
   不需要主密钥），逐个 `Get`。任何一项解不开 → 整体失败，列出解不开的键名，附带现有的 `Hint`（主密钥变了之类）。
   不做“跳过解不开的继续导出”。
3. 加上 `state.json`、`gui.json`（存在才加），`--with-logs` 时加 `logs/` 下的调用记录文件。
4. **不导出**：主密钥、`master.key`、锁文件、`heartbeat`、`engine.lock`、触发文件、`gui.log`、systemd / launchd / 计划任务文件。
5. 返回摘要：账号名列表、vault 条目数、是否包含记录。摘要和错误里**永远不出现** secret、令牌、webhook 地址。

## 5. 导入

先全部在内存里解密、解压、校验，确认没有问题后才开始写盘。

### 5.1 校验

- 解析 `config`，解析 `state` / `gui`。任何一项不合法 → 整体失败。
- **`[vault]` 段是本机设置，永远不导入**：目标机已有配置就保留它的 `[vault]`，没有就用默认值（`auto`）。
  这个替换发生在**校验之前**（`config.ParseWithVault`），所以备份里 `[vault] backend` 写成什么（哪怕是这台机器不认识的值）都不影响导入。
  本机的 `[vault]` 用 `config.VaultSetting` 宽松地读取：整份 config 无效时只要 `[vault]` 段还能解析就用它，否则用 `auto`。
- 目标机的主密钥：能解析就用；`ErrNoMasterKey` 且 vault 目录里没有数据时，按 `vault init` 同样的方式创建一个（存进钥匙串），
  并在摘要里说明；其他主密钥错误（远程会话拿不到钥匙串等）直接失败，带 `Hint`。

### 5.2 三种模式

| 模式 | 目标机已有账号或 vault 数据时 | 行为 |
|---|---|---|
| 默认 | 拒绝，提示用 `--merge` 或 `--replace` | 目标为空时：写入全部内容（包括 `[network]` 代理等备份里的设置，`[vault]` 除外） |
| `--merge` | 允许 | 只添加备份里**目标机没有的**账号（配置条目 + `secret/<名>` + `tokencache/<名>` + state 里该账号的统计）。有同名账号 → 整体失败并列出冲突名，什么都不写。目标机的调度、告警、webhook、gui 设置和 `[network]` 代理保持不变。不导入调用记录。 |
| `--replace` | 允许 | 目标机的账号、调度、告警设置、webhook、`[network]` 代理、state、gui 设置全部换成备份里的（`[vault]` 段除外）。目标机上有、备份里没有的账号：删除其 vault 条目。备份里有调用记录时复制过去（同名文件覆盖）。 |

**`[network] proxy` 随默认和 `--replace` 导入一起走**：备份来自另一个网络环境时，新机器可能连不上那个代理，导入后要检查
（`e5extend doctor`）。merge 不动目标机的代理。

**本机 `config.toml` 无效时**：默认和 merge 拒绝（错误里提示用 `--replace`，什么都不写）。`--replace`（GUI 的“替换”同理）照常进行：
本机的 `[vault]` 从无效文件里能解析就取它、否则用 `auto`；无效文件先复制成 `config.toml.broken-<时间戳>`（和原文件同目录），
再被新配置覆盖，摘要里有一条注记说明这个文件名。保存配置失败时这份副本会被删掉。

### 5.3 写入顺序与回滚

在配置锁（`LockConfig`）里：

1. 准备：算出要写入的 vault 条目 `puts` 和 replace 时要删除的旧条目 `stale`。**对涉及的每个 `tokencache/<名>` 键拿 `vault.Lock`**（按键名排序），
   一直持有到导入结束（含回滚），引擎刷新令牌用的是同一把锁，所以刷新不会插进快照和写入之间。锁在 `ImportBackup` 触发引擎重新加载之前释放。
   **加锁顺序**：配置锁 →（排序后的）vault 键锁 → 引擎锁（只做不阻塞的尝试）。引擎持有键锁时不会去要配置锁，也没有谁会一直等引擎锁，所以不会死锁。
2. 对 `puts` 和 `stale` 的**每个**键在内存里做快照（`vault.Snapshot`：读磁盘上加密后的原始字节，不需要主密钥，解不开的旧条目也能原样放回去）。
3. 逐个 `Put`。
4. 交接运行状态（见下）：引擎在跑时，**先**把 `state.import.json` 写好，**再**保存配置。
5. 本机 `config.toml` 无效且是 replace 时：先把它复制成 `config.toml.broken-<时间戳>`。
6. 保存 `config.toml`（`cfg.Save`，原子写入；测试里可以通过 `Ops.SaveConfig` 注入失败）。
7. **3–6 任何一步失败 → 回滚**：把快照放回去（原来没有的键删除），撤回已写的交接文件，返回“……\nnothing was changed”（错误码 `save_failed`）。
   回滚本身失败时的建议按模式区分：
   - 默认 / replace（`rollback_failed`，Names = 没放回去的键）：运行 `e5extend doctor`，账号有问题就再用 `--replace` 导入一次；
   - merge（`rollback_failed_merge`，Names = 正要添加的账号）：这些账号的 vault 条目可能残留，先删掉（`e5extend account remove 名称`）再合并。
   GUI 里同样的两条是翻译过的、不带命令行参数的文字。
8. **配置保存成功 = 导入成功。** 之后的任何失败都只是**注记**，不是错误，所有模式一样：
   删不掉的旧条目（`stale_not_removed`），写不了 `state.json` / `gui.json` / 调用记录（`stats_not_imported`，Args 列出哪几项：state、gui、logs；
   意思是“账号和 secret 已经导入，统计没有导入”）。
9. 保存成功后才删除 `stale` 条目。
10. 解锁后调用 `ReloadEngine()`（引擎在跑就让它重新加载；没在跑不报错）。

默认模式和 merge 用同一套快照 / 回滚：它们写入的键要么是新的（回滚时删掉），要么是已有的（回滚时放回去）。

**引擎在运行时的 state.json**：引擎把运行状态保存在内存里，并会覆盖 `state.json`，所以导入直接写 `state.json` 会被它悄悄改回去（GUI 托盘里的引擎总是在跑）。因此：

- 导入用 `acquireEngineLock` 拿引擎锁：每 25 ms 重试，最多 1.5 秒（`status` 之类的探测只会短暂占用它）。拿到了说明没有引擎，直接写 `state.json`，
  并一直持有锁到写完，防止这时恰好有引擎启动；同时删掉遗留的 `state.import.json`。仍然拿不到（`ErrRunning`）才认为有引擎在跑：
  **不写 `state.json`**，而是把导入的逐账号状态原子写到数据目录下的 `state.import.json`（0600，
  `{"mode": "replace"|"merge", "written": <时间>, "accounts": {...}}`），并在保存配置**之前**写好，这样任何看到新账号的重新加载也能看到它们导入的状态。
- 引擎在启动（`init`）、每次重新加载之后、以及每个 tick 看到这个文件时认领它（`adoptPending`）：
  - **先把文件改名**成 `state.import.adopting-<pid>-<纳秒>` 再读（`store.ClaimPendingImports`；上次被打断遗留的同名前缀文件也一并处理）。
    `SavePendingImport` 只碰 `state.import.json`，所以认领期间到来的第二次导入会写一个新文件，由下一次认领处理，不会丢也不会被应用两次。
    （极小的窗口：第二次导入在读到旧文件之后、引擎改名之前合并写入，旧条目可能再应用一次，内容相同。）
  - **一次性**：认领的文件应用到内存之后立即删除，即使紧接着的保存失败（内存里的状态在之后的 tick 里补存，`unsaved`），所以不会反复认领。
  - 只应用当前配置里已有的账号。配置里还没有的账号的条目留在一个新的 `state.import.json` 里等（保留原来的 `written`），满 24 小时仍没出现就丢弃，
    整批只发一次 `state_import_expired` 事件。写入超过 7 天的文件直接忽略并删除（同样一个事件）。
  - 有一轮正在跑的账号不认领，条目留到那一轮结束后的下一个 tick；其他账号不用等。
  - replace / 默认模式用导入的条目替换这些账号的条目；merge 只在引擎的状态**没有任何记录**（`Totals` 全零且没有成功时间——即引擎为新账号建的空状态）时替换，
    有记录的账号保持不变。已经过期的 `next_run` 和启动时一样改成一两分钟后。每次认领只发一个 `state_import` 事件。
- 调用记录和 `gui.json` 仍然直接写（引擎只追加记录，不在内存里持有它们）。

窗口：引擎在交接文件写好之后、配置保存之前恰好 tick，同名账号会提前应用导入的状态；如果这时配置保存失败，回滚不能撤销这次应用。
这个窗口只有保存配置那几毫秒。

**令牌缓存的乐观写**：`internal/auth` 的 `vaultCache` 在 `Replace` 时记住读到的内容的摘要（或“当时没有”），`Export` 在锁内重新读一次，
和记住的不一致（导入、另一个进程写过）就**不写**并记一条 debug 日志，下次 `Replace` 会读到新值。这样引擎持有的旧令牌缓存不会盖掉刚导入的。

### 5.4 摘要

返回导入的账号名、模式、来源系统和版本、是否新建了主密钥、跳过了什么（例如 merge 模式忽略调用记录）。
导入后，有条件访问策略把令牌绑定在设备上的租户里，账号可能显示“需要登录”，摘要里给出一行提示：运行 `e5extend doctor` 检查。

摘要的 `Notes` / `Skipped` 是给命令行看的英文，同时有 `NoteCodes` / `SkippedCodes`（`{code, args}`）给 GUI 翻译。
拒绝和失败是 `*ops.BackupError{Code, Names, Msg, Err}`（`Error()` 是命令行用的英文，带 `--replace` 之类的参数），
码有 `file_exists`、`target_has_data`、`merge_conflict`（Names = 冲突账号）、`local_config_broken`、`invalid_backup`、`newer_version`、
`save_failed`、`rollback_failed`、`rollback_failed_merge`；文件本身的错误（`backup.ErrNotBackup`、`ErrTooNew`、`ErrDecrypt`、`ErrTooLarge`、
`ErrPayloadTooLarge`、`ErrBadFile`、`ErrShortPassphrase`）是哨兵错误。

## 6. 代码位置

- `internal/backup`：格式（头部、KDF、加解密、payload 编解码、大小和参数上限）。纯函数，不碰磁盘以外的东西，便于测试。
- `internal/vault`：新增 `List()`。
- `internal/ops/backup.go`：`ExportBackup(ctx, path, passphrase, ExportOptions) (BackupSummary, error)`、
  `ImportBackup(ctx, path, passphrase, ImportMode) (ImportSummary, error)`。
- `cmd/e5extend/backup.go`：命令。
- GUI：`gui/services/backup.go` 提供导出 / 导入，用 Wails 的保存 / 打开文件对话框；
  设置页新增“备份与迁移”区块：密码（导出时确认一次）、导出时是否包含记录、导入模式（目标为空时不显示）；中英文文案；`api/mock.ts` 同步。
  - 保存对话框默认文件名 `e5extend-backup-YYYYMMDD.e5backup`，过滤器 `*.e5backup`，标题用 `SaveFileDialogOptions.Title`（这个版本的保存对话框没有 `SetTitle`）。
  - 覆盖：只有对话框原样返回的路径才用 `Force`（对话框已经问过用户）。名字里没有扩展名、由我们补上 `.e5backup` 的，
    **不**覆盖已存在的文件，返回错误码 `file_exists`，界面显示“已经有同名文件，请换一个名字”。
  - 服务把拒绝和失败放在结果的 `problem`（`{code, names, detail}`）里，而不是 Go 错误，前端按码翻译成中 / 英文，
    GUI 文案里没有 `--replace` 之类的命令行参数；摘要里的注记同理按 `noteCodes` / `skippedCodes` 翻译。
  - 保存对话框的“覆盖确认”是对话框自己的：Wails 的 GTK3 构建（`linux_cgo_gtk3.go`）不会打开它，发布版用 GTK4，所以没问题；代码里有注释。
  - 选“替换”时先弹确认框，列出这台机器现有的账号（备份里同名的会被覆盖，其余被删除）；确认后才打开文件对话框。
  - 探测“这台机器有没有数据”失败（比如本机 `config.toml` 无效）时，界面显示模式选择，只留“替换”并说明原因，不再悄悄退回默认模式。
- 文档：`docs/user/backup.md`（中文，含跨平台迁移步骤），README 文档表加一行，`CHANGELOG.md` 加 “未发布” 一节。

## 7. 测试

- `internal/backup`：往返；密码错误；改动头部任一字节（AAD）或密文任一字节都失败；未知版本 / kdf；iterations 越界；超大文件；空 payload。
- `internal/vault`：`List` 返回所有键、忽略锁文件和临时文件、不需要主密钥。
- `internal/ops`：两套独立的配置 / 数据目录 + 不同主密钥（用 `vault.New` 注入），export → import 后 secret、tokencache、webhook、state 一致；
  `[vault]` 段不被覆盖；默认模式遇到已有账号拒绝且不写任何东西；merge 冲突时不写任何东西；merge 只加新账号；
  replace 删除多余账号的 vault 条目；导出时有解不开的条目 → 失败并列出键名；摘要和错误文本里不含任何 secret / webhook 地址。
- CLI：导出文件已存在且没有 `--force` 时拒绝；stdin 读密码的非终端路径。
- 写统计失败只是注记（`TestFailureWritingStatisticsIsOnlyANote`，三种模式）；回滚失败的建议按模式区分（`TestRollbackFailedAdviceDependsOnTheMode`）。
- 回滚：注入 `SaveConfig` 失败，replace / 默认 / merge 之后旧 vault 完全不变（含解不开的条目）、旧 secret 仍可解、没有文件变化；删除旧条目发生在保存配置之后。
- 引擎：持有引擎锁时导入不写 `state.json` 而写 `state.import.json`；引擎（`internal/engine/import_test.go`）重新加载 / 启动时认领，merge 只加没有的账号，
  导入之后引擎自己再保存也不会改回去；真实引擎在回路里的端到端测试（`internal/ops/backup_engine_test.go`）。
- 上限：`internal/backup` 里真实大小的导出（超过 96 MiB）被拒绝、一个很小但解压后超限的文件被拒绝、`readCapped` 最多读 96 MiB + 1 字节；
  `internal/ops` 里用稀疏文件造出超限的调用记录，导出被拒绝且不写文件。
- 错误文本：`internal/backup` 把每一种错误路径都喂一个带 secret 的文件，断言错误里没有 secret 和密码。
- 令牌缓存：`internal/auth` 里别的写入者在 `Replace` 和 `Export` 之间改了 / 删了 / 新建了缓存，`Export` 不覆盖。
