# 停止扫描 / 中止替换 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 ConfigPatch 增加"停止扫描 / 中止替换"能力：UI 新增"③ 停止"按钮，core 通过 `Config.Cancel` 回调支持中途中断，关闭窗口同时停止后台任务。

**Architecture:** 复用现有 `Config` 回调模式（同 `Logf`）：给 `core.Config` 增加 `Cancel func() bool`，`Scan` 在 `WalkDir` 回调内检查并返回哨兵错误 `ErrCancelled`（保留部分命中）；`ExecOne` 入口兜底检查；`main.go` 用 `sync/atomic.Bool` 做跨线程停止标志，扫描/执行主循环在每项之间检查。

**Tech Stack:** Go 1.25，Walk（Windows 原生 GUI），`sync/atomic`，`filepath.WalkDir`。

**设计文档:** `docs/superpowers/specs/2026-08-24-stop-abort-feature-design.md`（已提交 `40811a8`）

> **工作区前置提醒：** 当前工作区存在**未提交的历史改动**（日志增强：毫秒级文件名/时间戳/逐步记录 + 日志框显示修复：VScroll/强制重绘），涉及 `core/core.go`、`core/core_test.go`、`main.go`、`README.md`、`docs/superpowers/specs/2026-08-24-configpatch-design.md`。必须先执行 **Task 0** 提交它们，后续功能提交才能干净。提交时**只 `git add` 对应任务涉及的文件**，避免把无关改动混入。

---

### Task 0: 提交历史挂起改动（清理工作区）

**Files:**
- Modify（提交，不改内容）: `core/core.go`、`core/core_test.go`、`main.go`、`README.md`、`docs/superpowers/specs/2026-08-24-configpatch-design.md`

- [ ] **Step 1: 确认挂起改动清单**

Run: `cd /mnt/f/project/ConfigPatch && git status --short`
Expected: 出现上述 5 个文件为 ` M`（已修改未暂存）。

- [ ] **Step 2: 提交历史改动（日志增强 + 显示修复）**

```bash
cd /mnt/f/project/ConfigPatch
git add core/core.go core/core_test.go main.go README.md docs/superpowers/specs/2026-08-24-configpatch-design.md
git commit -m "feat: 增强运行日志并修复日志框显示

- 日志文件名精确到毫秒（run-YYYYMMDD-HHMMSS.mmm.log），同毫秒自动追加 _1/_2
- 日志每行带完整时间戳（含毫秒），逐步记录 处理→备份→生成新文件→覆盖→校验/回滚
- 日志框增加垂直滚动条，追加后强制 Invalidate+UpdateWindow 修复显示不全"
```

- [ ] **Step 3: 确认工作区已干净**

Run: `git status --short`
Expected: 无输出（工作区干净）。

---

### Task 1: core — 支持取消（TDD）

**Files:**
- Modify: `core/core.go`
- Test: `core/core_test.go`

- [ ] **Step 1: 写失败测试**

在 `core/core_test.go` 文件末尾（`TestExecSkipsWhenNoLongerMatches` 之后）追加两个测试：

```go
func TestScanStopsWhenCancelled(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"site1", "site2"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "web.config"), []byte("<add key=\"k\" value=\"abc\" />"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	c := Config{
		RootDirs:  []string{root},
		FileNames: []string{"web.config"},
		OldValue:  "abc",
		NewValue:  "xyz",
		Cancel: func() bool {
			calls++
			return calls >= 4 // root + site1 + site1/web.config(命中) + site2 时停止
		},
	}
	hits, derrs, err := Scan(c)
	if err != ErrCancelled {
		t.Fatalf("expected ErrCancelled, got %v", err)
	}
	if len(derrs) != 0 {
		t.Errorf("expected no dir errors, got %+v", derrs)
	}
	if len(hits) < 1 {
		t.Errorf("expected partial hits, got %d", len(hits))
	}
}

func TestExecCancelled(t *testing.T) {
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	content := "<add key=\"k\" value=\"abc\" />"
	if err := os.WriteFile(cfgFile, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	c := Config{
		RootDirs:  []string{root},
		FileNames: []string{"web.config"},
		OldValue:  "abc",
		NewValue:  "xyz",
		Cancel:    func() bool { return true },
	}
	hits, _, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	res := ExecOne(hits[0], c)
	if res.Err != nil {
		t.Fatalf("expected no error, got %v", res.Err)
	}
	if res.Skipped == "" {
		t.Fatalf("expected skipped (cancelled), got %+v", res)
	}
	if res.BackupPath != "" || res.NewFilePath != "" {
		t.Errorf("cancelled exec must not create backup/new files, got backup=%q new=%q", res.BackupPath, res.NewFilePath)
	}
	got, _ := os.ReadFile(cfgFile)
	if string(got) != content {
		t.Errorf("original was modified: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "backup-config")); !os.IsNotExist(err) {
		t.Errorf("backup-config dir should not exist after cancelled exec")
	}
}
```

- [ ] **Step 2: 运行测试确认失败（编译错误）**

Run: `cd /mnt/f/project/ConfigPatch && /usr/local/go/bin/go test ./core/ 2>&1 | head -20`
Expected: FAIL —— 报 `c.Cancel undefined` / `ErrCancelled undefined`（`Config` 尚无这些成员）。

- [ ] **Step 3: 实现核心取消支持**

修改 `core/core.go`：

(1) 在 `ErrSameValue` 声明之后新增哨兵错误：

```go
// ErrCancelled 表示操作被用户请求中止（扫描未完成 / 替换未全部执行）。
var ErrCancelled = errors.New("操作已被用户中止")
```

(2) 在 `Config` 结构体末尾（`Logf` 之后）新增字段：

```go
	// Cancel 返回 true 表示用户请求停止当前操作；nil 表示不支持取消。
	Cancel func() bool
```

(3) `Scan` 的 `WalkDir` 回调最顶部（`walkErr` 判断之前）插入：

```go
		werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if c.Cancel != nil && c.Cancel() {
				return ErrCancelled
			}
			if walkErr != nil {
```

(4) `ExecOne` 函数最顶部（`res := ExecResult{Hit: h}` 之后、`step` 定义之前）插入：

```go
	// 用户已请求停止：不读取、不触碰原文件，也不创建备份目录。
	if c.Cancel != nil && c.Cancel() {
		res.Skipped = "用户已请求中止"
		return res
	}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /mnt/f/project/ConfigPatch && /usr/local/go/bin/go test ./core/`
Expected: `ok configpatch/core`，全部测试（含 2 个新测试）PASS。

- [ ] **Step 5: 提交**

```bash
cd /mnt/f/project/ConfigPatch
git add core/core.go core/core_test.go
git commit -m "feat(core): 支持扫描/替换中途取消（Config.Cancel + ErrCancelled）"
```

---

### Task 2: main.go — 停止按钮与取消接线

**Files:**
- Modify: `main.go`

> 说明：纯 Win32 UI 层，无单元测试框架；以 `go vet` + 交叉编译成功作为验证。

- [ ] **Step 1: 引入 sync/atomic 导入**

`main.go` 的 `import` 块，在 `"runtime"` 后新增一行：

```go
	"sync/atomic"
```

- [ ] **Step 2: MainWin 增加字段**

在 `execBtn *walk.PushButton` 后新增：

```go
	stopBtn *walk.PushButton
```

在 `// runtime state` 区块、`closed   bool` 后新增：

```go
	cancelScan atomic.Bool // 停止标志：UI 线程写，工作 goroutine 读
```

- [ ] **Step 3: UI 增加"③ 停止"按钮**

"操作" `GroupBox` 的 `Children` 中，在 ② 执行替换按钮后新增：

```go
					decl.PushButton{AssignTo: &mw.stopBtn, Text: "③ 停止", OnClicked: mw.onStop, Enabled: false},
```

- [ ] **Step 4: 更新 setBusy**

```go
func (mw *MainWin) setBusy(b bool) {
	mw.busy = b
	mw.scanBtn.SetEnabled(!b)
	mw.execBtn.SetEnabled(!b)
	mw.stopBtn.SetEnabled(b)
}
```

- [ ] **Step 5: 新增 onStop 处理器**

在 `clearDirs` 之后新增：

```go
// onStop 请求停止正在进行的扫描或替换。
func (mw *MainWin) onStop() {
	mw.cancelScan.Store(true)
	mw.logf("用户请求停止，正在中断…")
	mw.status.SetText("正在停止...")
}
```

- [ ] **Step 6: onScan 接线**

在 `onScan` 中 `mw.buildConfig()` 成功之后、`mw.setBusy(true)` 之前插入：

```go
	mw.cancelScan.Store(false)
	cfg.Cancel = func() bool { return mw.cancelScan.Load() }
```

将 `onScan` 的 `Synchronize` 块中 `if serr != nil {` 之前插入 `ErrCancelled` 分支：

```go
			if serr == core.ErrCancelled {
				mw.lastHits = hits
				paths := make([]string, 0, len(hits))
				for _, h := range hits {
					paths = append(paths, h.Path)
				}
				mw.hitsList.SetModel(paths)
				mw.logf("扫描已停止：命中 %d 个配置文件（未完成，仅部分结果）", len(hits))
				mw.status.SetText(fmt.Sprintf("扫描已停止，命中 %d 个（部分结果）", len(hits)))
				return
			}
			if serr != nil {
```

- [ ] **Step 7: onExec 接线**

在 `onExec` 中确认对话框通过之后、`// open the run log file` 之前插入：

```go
	mw.cancelScan.Store(false)
	cfg.Cancel = func() bool { return mw.cancelScan.Load() }
```

将 `onExec` 的 goroutine 循环替换为（在循环内每项前检查停止，并区分"已中止"汇总）：

```go
	go func() {
		okCount, skipCount, failCount := 0, 0, 0
		stopped := false
		for _, h := range mw.lastHits {
			if mw.cancelScan.Load() {
				stopped = true
				break
			}
			res := core.ExecOne(h, cfg)
			line := formatResult(res)
			mw.Synchronize(func() {
				if mw.closed {
					return
				}
				mw.logf("%s", line)
			})
			writeLogLine(logf, line)
			switch {
			case res.Err != nil:
				failCount++
			case res.Skipped != "":
				skipCount++
			default:
				okCount++
			}
		}
		var summary string
		if stopped {
			summary = fmt.Sprintf("已中止：成功 %d，跳过 %d，失败 %d，已处理 %d/%d 个文件", okCount, skipCount, failCount, okCount+skipCount+failCount, len(mw.lastHits))
		} else {
			summary = fmt.Sprintf("执行完成：成功 %d，跳过 %d，失败 %d，耗时 %s", okCount, skipCount, failCount, time.Since(start).Round(time.Millisecond))
		}
		if logf != nil {
			writeLogLine(logf, "==== "+summary+" ====")
			logf.Close()
		}
		mw.Synchronize(func() {
			if mw.closed {
				return
			}
			mw.logf("==== %s ====", summary)
			mw.status.SetText(summary)
			mw.setBusy(false)
			walk.MsgBox(mw, "完成", summary, walk.MsgBoxIconInformation)
		})
	}()
```

- [ ] **Step 8: 关闭窗口同时停止后台任务**

`mw.Closing().Attach(...)` 回调内，在 `mw.closed = true` 后新增一行：

```go
		mw.cancelScan.Store(true) // 关闭窗口同时停止后台扫描/替换
```

- [ ] **Step 9: 验证编译**

Run: `cd /mnt/f/project/ConfigPatch && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 /usr/local/go/bin/go vet ./...`
Expected: 无输出，退出码 0。

- [ ] **Step 10: 提交**

```bash
cd /mnt/f/project/ConfigPatch
git add main.go
git commit -m "feat: 新增「③ 停止」按钮，支持扫描/替换中途停止，关窗同步停止后台任务"
```

---

### Task 3: 文档同步

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-24-configpatch-design.md`

- [ ] **Step 1: README 功能特性补充**

在 `README.md` 的"运行日志"功能项之后（`- **运行日志**：...` 所在行之后）新增：

```markdown
- **随时可停止**：扫描或执行替换过程中可随时点「③ 停止」中断：扫描保留已扫到的部分结果并明确提示未完成；替换保留已处理完的文件（各有 `backup-config` 备份）；关闭窗口也会同时停止后台任务。
```

- [ ] **Step 2: README 使用说明补充**

在 `README.md` 使用说明第 5 步（`5. 点 **② 执行替换**，...`）之后新增：

```markdown
6. 如需中途停止，点「③ 停止」即可中断当前扫描或替换；关闭窗口同样会停止后台任务。
```

- [ ] **Step 3: 主设计文档补充优化记录**

在 `docs/superpowers/specs/2026-08-24-configpatch-design.md` 末尾（"优化记录（2026-08-24，v2）"小节之后）新增：

```markdown
## 优化记录（2026-08-24，v3）

1. **停止/中止支持**：新增「③ 停止」按钮；`Config` 增加 `Cancel func() bool` 回调，`Scan` 在 `WalkDir` 回调内检查（返回哨兵 `ErrCancelled`，保留部分命中），`ExecOne` 入口兜底检查；执行主循环在每个文件之间检查。扫描停止保留部分结果并提示"未完成，仅部分结果"；替换中止保留已处理完的文件（各有备份）。关闭窗口同时置位停止标志，真正停止后台任务。详见 `2026-08-24-stop-abort-feature-design.md`。
```

- [ ] **Step 4: 验证并提交**

Run: `cd /mnt/f/project/ConfigPatch && git diff --stat`
Expected: 显示 `README.md` 与主设计文档有改动。

```bash
cd /mnt/f/project/ConfigPatch
git add README.md docs/superpowers/specs/2026-08-24-configpatch-design.md
git commit -m "docs: 同步停止/中止功能的使用说明与设计记录"
```

---

### Task 4: 最终构建与全量验证

**Files:** 无新增/修改（仅验证）。

- [ ] **Step 1: 全量测试**

Run: `cd /mnt/f/project/ConfigPatch && /usr/local/go/bin/go test ./core/`
Expected: `ok configpatch/core`。

- [ ] **Step 2: 确认无进程占用后重建 exe**

Run: `tasklist.exe 2>/dev/null | grep -i configpatch || echo "无进程占用"`
- 若输出进程行，先 `taskkill.exe /F /PID <pid>` 再继续（须用户授权时先确认）。

```bash
cd /mnt/f/project/ConfigPatch
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 /usr/local/go/bin/go build -ldflags "-H windowsgui" -o ConfigPatch.exe .
```
Expected: `BUILD OK`（无报错）。

- [ ] **Step 3: 敏感信息复检**

Run:
```bash
cd /mnt/f/project/ConfigPatch
for s in "192.168.13.72" "hlog.hs.com"; do strings ConfigPatch.exe | grep -qF "$s" && echo "✗ $s" || echo "✓ 无 $s"; done
```
Expected: 两处均 `✓ 无`。

- [ ] **Step 4: 提交剩余内容（如有）并查看提交历史**

Run: `cd /mnt/f/project/ConfigPatch && git status --short && git log --oneline -5`
Expected: 工作区干净；日志包含 Task 0~3 的 4 个提交。

---

## Self-Review（已在编写时检查）

- **Spec 覆盖**：停止按钮（Task2 Step3/4/5）、Scan 取消+部分命中（Task1 + Task2 Step6）、Exec 取消+保留已改文件（Task1 + Task2 Step7）、关窗停止（Task2 Step8）、测试（Task1）、文档（Task3）、构建验证（Task4）——全覆盖。
- **占位符扫描**：所有步骤含完整代码与命令，无 TBD/TODO。
- **类型一致性**：`ErrCancelled`、`Config.Cancel func() bool`、`atomic.Bool`、`stopBtn`、`cancelScan`、`onStop` 在各任务间名称一致。
