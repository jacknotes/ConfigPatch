---
name: "win-gui-toolkit"
description: "Reusable engineering patterns for Windows native GUI tools (esp. Go + lxn/walk): UI/logic separation, GUI thread model, safe file backup-verify-rollback, encoding-roundtrip, and Walk/Win32 gotchas. Invoke when starting or designing a Windows desktop/tool project, or when hitting Walk/Win32 issues (quiet exit on tooltip, combo dropdown collapse, log repaint, checkbox truncation)."
---

# Windows 图形化工具开发经验（win-gui-toolkit）

一套从真实项目（Go + lxn/walk 的 Windows 配置文件批量替换工具）沉淀出来的可复用工程模式。
适用于**在本机/LAN 上操作文件、配置或目录的 Windows 图形化小工具**，不限定业务领域。

> 核心理念：**界面与逻辑分离**，让核心逻辑可以在无 GUI 环境里单元测试；对"操作线上文件"这类高风险动作，一律走「预览 → 确认 → 备份 → 写入 → 校验 → 回滚」的安全链。

---

## 1. 何时使用本 skill

- 要新开/设计一个 **Windows 原生桌面工具**（Go+Walk 或同思路的其它栈）。
- 需求涉及：批量改文件、操作配置、目录递归扫描、多目标目录/UNC 共享。
- 在 Walk/Win32 上遇到「双击无反应/静默退出、下拉塌陷、日志/文本不刷新、文字截断」等问题。
- 需要处理 UTF-8/UTF-16/GBK 等中文编码的读写。

---

## 2. 架构与模块划分（照抄骨架）

把代码拆成三层，让业务尽量远离 GUI：

```
main/          GUI 层：只用界面控件采集输入、渲染结果、转发事件
core/          纯逻辑层：不 import 任何 GUI 库，全部可单测
   ├─ core.go   Validate / Scan / ExecOne 与文件、路径辅助
   └─ encoding.go  编码识别与强一致往返
configstore/   可选：配置持久化（命名方案 + last + 历史回滚）
```

- `core` 包通过一个**配置结构体 + 可选回调**和 UI 交互，而不是反向调用 GUI：

```go
type Config struct {
	RootDirs  []string
	FileNames []string
	OldValue  string
	NewValue  string
	// 回调，注入而不依赖具体 GUI：
	Logf       func(format string, args ...interface{})
	Cancel     func() bool // 返回 true 表示用户请求停止
}
```

- 长操作写成**纯函数**：`Scan(cfg) (hits, errs, err)`、`ExecOne(hit, cfg) Result`，
  返回结构化结果（成功/跳过/失败原因/备份路径/回滚标志），由 GUI 层去展示。
- 好处：核心逻辑可在 CI/命令行跑单测；GUI 层变得很薄、只做接线的脏活。

---

## 3. 线程模型（Go + Walk 标准范式）

- 包级 `func init() { runtime.LockOSThread() }`（或 main 开头）：锁定 GUI 线程。
- 长任务放后台 goroutine；所有 **UI 更新必须经 `mw.Synchronize(func(){...})` 回到 GUI 线程**。
- 维护一个 `closed bool` 标志：关窗时置 true，Synchronize 回调里先判断，避免关闭后回刷已销毁的控件。
- 用 `atomic.Bool` 做停止标志：UI 线程写，工作 goroutine 只读。
- 模式（onScan 开头）：

```go
go func() {
	// 后台做耗时工作……
	hits, _, err := core.Scan(cfg)
	mw.Synchronize(func() {
		if mw.closed { return }   // 防止关窗后操作已销毁控件
		// 回刷 UI……
	})
}()
```

---

## 4. 安全文件操作链（高风险动作必做）

对 **预览 → 确认 → 备份 → 生成新文件 → 覆盖 → 校验 → 回滚**：

1. **预览用（Scan）**：只读、只收集命中列表，绝不改动。
2. **执行前（ExecOne）重新确认**：执行时刻再读一次文件，仍命中才处理（预览后可能已变）。
3. **先备份再动原文件**：把原文件字节快照到 `backup-config\<名>-<时间戳>`，用 `uniquePath` 保证**永不覆盖旧备份**（冲突自动追加 `_1/_2`）。
4. **写新文件而非直接改原文件**：先构造 `-new` 副本，校验无异常后再覆盖原文件。
5. **覆盖后回读校验**：写入不等于成功，必须回读比对；失败自动用刚备份的快照回滚。
6. **单个文件失败不阻塞其它文件**：错误记入结果，循环继续。

关键函数原型：

```go
// 备份路径冲突时追加序号，保证历史备份不被覆盖
func uniquePath(p string) string

// 覆盖写：重试 3 次/300ms，失败时自动去只读属性（容忍临时文件锁/权限）
func writeFile(path string, data []byte) error
```

**校验的一个隐蔽坑**：校验解码要用**执行时捕获的编码**，绝不能重新 `DetectEncoding`。
例：GBK 文件替换后剩余字节可能恰好构成合法 UTF-8，重新检测会把编码翻转为 UTF-8，导致内容比对误判、错误回滚。
务必用执行时 `enc := DetectEncoding(raw)` 捕获的值，在 Encode 和校验前持续复用同一个 `enc`。

---

## 5. 强一致编码往返（处理中文文件/配置的刚需）

识别优先级：**BOM → utf8.Valid → 兜底 GBK(ANSI/cp936)**。

```go
type EncKind int
const (
	EncUTF8 EncKind = iota // UTF-8 无 BOM（含纯 ASCII）
	EncUTF8BOM             // UTF-8 带 BOM
	EncUTF16LE
	EncUTF16BE
	EncGBK                 // ANSI/GBK, 代码页 936
)

func DetectEncoding(raw []byte) EncKind {
	// 顺序：UTF8 BOM(EF BB BF) → UTF16LE(FF FE) → UTF16BE(FE FF) → utf8.Valid → EncGBK
}
```

- `Decode(k, raw) (string, error)` / `Encode(k, s) ([]byte, error)`，要求**严格往返**：BOM、`\r\n` 一字不差。
- **x/text 的坑**：`unicode.UTF16(..., unicode.IgnoreBOM)` 解码时会把 BOM 当作 U+FEFF 字符留在输出里，造成二次往返出现双 BOM。对策：**解码前先手动剥离 BOM 字节，编码时再手动加回**。
- 每个编码类型都要有**往返单测**（拿原始字节 Decode→Encode 必须全等），尤其覆盖 UTF-16 双 BOM 回归用例。
- 编码处理用 `golang.org/x/text` 的 `transform` / `simplifiedchinese.GBK`。

---

## 6. 取消 / 停止机制

- `Config.Cancel func() bool` 回调实现。
- 递归/遍历里（如 `WalkDir` 回调）**每个节点检查** `Cancel()`，命中即返回哨兵错误 `ErrCancelled`，
  并**保留已收集的部分结果**，明确告知用户"部分结果/未完成"。
- Exec 侧：入口检查 + 大循环在文件之间检查；停止后已处理的文件各有备份，可追溯。
- 关窗时同时置位停止标志，真正中止后台任务。

```go
var ErrCancelled = errors.New("操作已被用户中止")
```

---

## 7. Walk / Win32 实测避坑（本项目踩过，务必警惕）

1. **ToolTip 创建失败导致程序静默退出**（最坑）：
   - 某些 Windows 会话（受限/非交互桌面）在 Win32 层面 `TTM_ADDTOOL` 返回 FALSE 且 `GetLastError=0`。
   - Walk 默认把它当硬错误 → `Create()` 返回 error → `log.Fatal` → GUI 程序无控制台，表现为"双击打不开、静默退出"。
   - 对策：`go mod vendor` 后**打补丁** `vendor/github.com/lxn/walk/tooltip.go` 的 `addTool`，让 `TTM_ADDTOOL` 失败时返回 nil（忽略这一纯装饰功能）。
   - ⚠️ 升级 Walk 需重新 `go mod vendor` 并重打补丁；补丁不影响业务功能。

2. **ComboBox 下拉列表塌陷（约 2px 几乎不可见）**：
   - `CBS_DROPDOWNLIST` 的下拉高度跟随组合框高度，Walk 布局把组合框压成一行（~21px）后，点击箭头下拉只有几像素。
   - 对策：`MoveWindow` 先把组合框临时拉高，让 Windows 预留下拉高度，再让 Walk 重新布局压回一行（下拉高度会保留）。窗口首次布局完成后用 `time.AfterFunc` 延时+重试应用。

3. **TextEdit/文本控件追加内容后不自动重绘**：
   - 程序化 `AppendText` + 滚动后，新暴露区域不重绘，表现为"内容半天不出现、选中才显示"。
   - 对策：追加后手动 `WM_VSCROLL(SB_BOTTOM)` + `InvalidateRect` + `UpdateWindow` 强制立即重绘。

4. **CheckBox 文字不换行被截断**：
   - Win32 CheckBox 长文本会被截断。对策：控件内放**短文案**，详细说明放下方跨列 Label。

5. **平台习惯差异**：
   - 路径/文件名匹配不区分大小写 → 全程用 `strings.EqualFold`。
   - 支持直接输入 UNC 共享路径（如 `\\server\d$\...`），因为浏览对话框可能打不开未映射的共享。

6. **下拉框/宽控件撑爆布局**：`ComboBox` 理想宽度会随最长项自适应而无限大，需同时设 `MinSize`/`MaxSize` 锁宽。

---

## 8. 测试与验证纪律

- 核心逻辑（core）要有覆盖：替换计数、重叠不重匹配、编码往返、validate 分支、备份不覆盖、端到端扫描执行、取消/部分结果、正则边界。
- 用真实临时目录（`t.TempDir()`）做端到端用例，不 mock 文件系统。
- 关键 UI 文案与 core 行为的一致性，可用单测锁住（如断言正则提示文案包含 `$1`/`$$`/`(?i)` 等关键 token，防止提示与实现漂移）。
- 提交前必须 `go test ./...` 通过。

---

## 9. 快速核对清单（新项目开工时检查）

- [ ] 业务逻辑已抽到不依赖 GUI 的包，且能单测
- [ ] 长任务走后台 goroutine + Synchronize + closed 标志；LockOSThread
- [ ] 高风险文件操作：先备份 → 写副本 → 覆盖 → 回读校验 → 失败回滚
- [ ] 编码走 Detect/Decode/Encode 严格往返，校验复用执行时编码，每类编码有往返测试
- [ ] 支持取消/停止，且停止后状态明确
- [ ] 若用 Walk：已处理 ToolTip 静默退出、ComboBox 下拉塌陷、文本重绘、CheckBox 截断这些坑
- [ ] vendor 了依赖，Walk 补丁已应用并在仓库中可追溯