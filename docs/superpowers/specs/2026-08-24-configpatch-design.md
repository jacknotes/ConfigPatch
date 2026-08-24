# ConfigPatch — 文件字符串批量替换工具 — 设计文档

日期：2026-08-24
技术栈：Go 1.25 + Walk（原生 Windows GUI），编译单 exe（`-ldflags "-H windowsgui"`）

## 背景与目标

运维场景需要跨多个 Windows 共享目录批量修改 `web.config` 中的连接字符串（例如把 `key="log_connString" value="http://hlog.example.com:9200"` 换成 `:9201`）。要求：
1. 只操作给定目录范围（含所有子目录），不触碰范围外文件；
2. 命中文件先按目录级备份，再生成新文件并覆盖，便于事后审计；
3. 新值与原值相同时要弹警告并中止。

## 关键设计决策（经用户确认）

| 决策点 | 结论 |
|---|---|
| 技术栈 | Go + Walk 原生界面；单 exe、零运行时依赖 |
| 目标目录 | 支持多个，各自递归扫描；文件名匹配不区分大小写 |
| 匹配/替换 | 大小写不敏感；同一文件**所有出现**全部替换为新值原文 |
| 新值=原值判定 | 默认大小写不敏感（`EqualFold`）→ 拦截仅改大小写；勾选"允许仅大小写变更"后改为精确相等 → 放行 |
| 操作流程 | 先"扫描预览"，人工确认后再"执行替换" |
| 备份目录 | 每文件所在目录 `backup-config\`：`<名>.config-YYYYMMDDHHMMSS`（快照，永不覆盖，冲突追加 `_1`） + `<名>-new.config`（每次覆盖为最新） |
| 编码 | 强一致保留原编码：识别 UTF-8 BOM / UTF-8 无 BOM / UTF-16 LE/BE / GBK，替换后同编码写回，BOM 与 `\r\n` 不变 |
| 校验/回滚 | 覆盖后重读校验"包含新值"；失败自动用快照回滚 |
| 日志 | 界面实时显示 + 程序目录 `logs\run-<ts>.log`（文件名精确到毫秒；每行带完整时间戳，逐步记录处理 → 备份 → 生成新文件 → 覆盖 → 校验/回滚） |

## 处理流程

```
输入校验（Validate）
 ├─ 目录非空、可访问、不重复；配置文件名非空；原值/新值 trim 后非空
 └─ 新值=原值判定（按开关，大小写不敏感或精确）→ ErrSameValue → 弹警告中止

扫描（Scan，每目录 WalkDir 递归）
 ├─ 跳过 backup-config 目录；只处理文件名（忽略大小写）匹配配置列表的文件
 ├─ 按原编码解码后大小写不敏感 contains 原值 → 命中
 └─ 无权限目录 → 记录错误继续，不中断

预览 → 用户确认 → 执行（ExecOne，逐文件独立）

执行（每个命中文件）
 ① 执行时重新检查仍包含原值（预览后可能变化）→ 否则跳过
 ② 创建 backup-config（不存在则建）
 ③ 快照原文件字节 → <名>.config-<ts>（uniquePath 防覆盖）
 ④ 生成 <名>-new.config：原编码解码 → 大小写不敏感全替换 → 同编码写回
 ⑤ 用 <名>-new.config 字节覆盖原文件（去只读属性 + 重试，容忍 IIS 短暂占用）
 ⑥ 重读校验包含新值；失败 → 用快照覆盖回滚
```

## 架构与模块

```
main.go          Walk 声明式 UI：目录列表、参数、按钮、命中列表、日志、状态栏
core/core.go     纯逻辑：Validate / Scan / ExecOne / 文件与路径辅助（可单元测试）
core/encoding.go 编码识别（DetectEncoding）与 Decode/Encode（强一致往返）
core/core_test.go 单元测试
```

- 界面与逻辑分离：`core` 不依赖 Walk，可在 CI/命令行环境测试。
- 长操作（扫描/执行）在后台 goroutine 运行，UI 更新经 `mw.Synchronize` 回到 GUI 线程；窗口关闭置 `closed` 标志防止关闭后更新 UI。

## 错误处理与安全

- 备份成功后才覆盖原文件；覆盖失败或校验失败自动回滚，原文件不损坏。
- 单个文件失败不阻塞其它文件。
- 原值含空、目标不可访问等在 Validate 阶段拦截。
- 覆盖写重试 3 次（间隔 300ms），自动清除只读属性，处理 IIS 短暂文件锁。

## 测试覆盖（core）

- `TestReplaceAllCI`：大小写不敏感全替换、计数、重叠不重匹配、中文场景。
- `TestContainsCI`、`TestDetectEncoding`、`TestEncodingRoundTrip`：四种编码 + BOM 强一致往返（含 UTF-16 双 BOM 回归用例）。
- `TestValidate`：新值=原值两种开关逻辑、空值、目录校验。
- `TestUniquePath`：备份不覆盖。
- `TestScanAndExec`：端到端（命中、跳过 backup-config、文件名大小写、备份内容一致、原文件更新、新文件内容一致）。
- `TestExecSkipsWhenNoLongerMatches`：预览后文件变化的跳过逻辑。

## 构建

```bat
go mod tidy
go build -ldflags "-H windowsgui" -o ConfigPatch.exe .
go test ./...
```

## 已知边界

- GUI 交互需在 Windows 桌面会话运行验证（本环境为无头会话，无法目视确认界面）。
- 访问 `\\server\d$` 管理共享需目标机管理员权限。
- 未来扩展：配置文件名称列表已可配置；如需对其它文件（如 `app.config`）同样处理，直接加入列表即可。

## 兼容性修复记录（2026-08-24）

### 现象
`ConfigPatch.exe` 双击无反应（GUI 子系统无控制台，静默退出）。

### 根因（已用裸 Win32 测试证实）
- 部分 Windows 会话在 Win32 层面无法创建 ToolTip：即便 tooltip 窗口与 tool 窗口句柄均有效，`TTM_ADDTOOL` 仍返回 FALSE（`GetLastError=0`）。
- Walk 在 `WidgetBase.init` 中为每个控件创建 ToolTip，`TTM_ADDTOOL` 失败即返回错误 → `decl.MainWindow.Create()` 返回错误 → `main()` 里 `log.Fatal` → 进程静默退出。
- 该失败与业务代码无关（最小化 "Hello" 窗口同样失败）；ToolTip 纯属装饰。

### 修复
- `go mod vendor` 将依赖打入 `vendor/`。
- 补丁 `vendor/github.com/lxn/walk/tooltip.go` 的 `addTool`：`TTM_ADDTOOL` 返回 FALSE 时改为返回 nil（忽略），不再中断窗口创建。
- 验证：补丁后程序成功启动，进程存活，主窗口标题创建成功。
- 影响面：仅悬停提示不可用（本程序未使用 ToolTip 功能），业务功能无影响。

## 优化记录（2026-08-24，v2）

1. **直接输入 UNC 路径**：目标目录区新增路径输入框（LineEdit + "添加路径"按钮 + 回车触发），支持直接输入本地或 UNC 共享路径，解决浏览对话框打不开未映射共享的问题。添加时不做强校验（避免不可达主机阻塞 UI），扫描时再报错。
2. **项目更名 ConfigPatch**：go.mod 模块名 `iisconfig-replace` → `configpatch`；exe 输出 `ConfigPatch.exe`；窗口标题 `ConfigPatch — 文件字符串批量替换工具`；文档同步更新。工具定位从"IIS"扩展为通用文件字符串替换。
3. **复选框文字截断修复**：Win32 CheckBox 文字不自动换行，"允许仅大小写变更（勾选后...）"过长被截断；改为短文案"允许仅大小写变更"，详细说明移入下方跨列 Label。
