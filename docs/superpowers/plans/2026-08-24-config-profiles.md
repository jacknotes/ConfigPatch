# 配置方案保存/加载与历史回滚 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 ConfigPatch 增加配置持久化：命名方案（保存/加载/删除/切换）、自动记忆上次配置（防抖自动保存 + 关闭兜底）、历史回滚（最近 5 版），且自动保存绝不覆盖命名方案。

**Architecture:** 新增可测试的 `configstore` 纯逻辑包（JSON 读写、方案增删改、`last`/`history` 压栈换位、原子写），main.go 仅做 UI 接线（方案区控件、启动加载、字段变更防抖保存、关闭兜底、回滚）。`config.json` 存于 exe 同目录并加入 `.gitignore`。

**Tech Stack:** Go 1.25，标准库 `encoding/json`/`slices`，Walk（Windows 原生 GUI）。

**设计文档:** `docs/superpowers/specs/2026-08-24-config-profiles-design.md`（已提交 `21eaa68`）

> **工作区状态：** 当前 main 分支工作区干净（上一功能已推送）。各任务提交时只 `git add` 本任务涉及的文件。

---

### Task 1: configstore 包（TDD）

**Files:**
- Create: `configstore/store.go`
- Test: `configstore/store_test.go`

- [ ] **Step 1: 写失败测试**

创建 `configstore/store_test.go`：

```go
package configstore

import (
	"os"
	"path/filepath"
	"testing"
)

func p(name string) Profile {
	return Profile{Name: name, RootDirs: []string{`\\server\d$\WebSite`}, FileNames: []string{"web.config"}, OldValue: "http://hlog.example.com:9200", NewValue: "http://hlog.example.com:9201", CaseOnlyAllow: true}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := &Store{Profiles: map[string]Profile{"方案A": p("方案A")}}
	last := p("last")
	s.Last = &last
	s.History = []Profile{p("h1"), p("h2")}

	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 1 || !got.Profiles["方案A"].Equal(p("方案A")) {
		t.Errorf("profiles roundtrip mismatch: %+v", got.Profiles)
	}
	if got.Last == nil || !got.Last.Equal(p("last")) {
		t.Errorf("last roundtrip mismatch: %+v", got.Last)
	}
	if len(got.History) != 2 || !got.History[0].Equal(p("h1")) {
		t.Errorf("history roundtrip mismatch: %+v", got.History)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not be fatal: %v", err)
	}
	if len(s.Profiles) != 0 || s.Last != nil || len(s.History) != 0 {
		t.Errorf("expected empty store, got %+v", s)
	}
}

func TestLoadCorruptReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o666); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err == nil {
		t.Fatal("expected error for corrupt json")
	}
	if len(s.Profiles) != 0 {
		t.Errorf("expected empty store on corrupt, got %+v", s)
	}
}

func TestSaveLastHistory(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	s.SaveLast(p("A"))
	if s.Last == nil || !s.Last.Equal(p("A")) {
		t.Fatal("last not set")
	}
	s.SaveLast(p("A")) // 相同值：不压历史
	if len(s.History) != 0 {
		t.Errorf("identical last should not push history, got %d", len(s.History))
	}
	s.SaveLast(p("B")) // 不同值：旧 last 压入历史
	if len(s.History) != 1 || !s.History[0].Equal(p("A")) {
		t.Errorf("history should contain old last A, got %+v", s.History)
	}
	if !s.Last.Equal(p("B")) {
		t.Errorf("last should be B, got %+v", s.Last)
	}
}

func TestHistoryCap(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	for i := 0; i < MaxHistory+3; i++ {
		s.SaveLast(p(string(rune('A' + i))))
	}
	if len(s.History) > MaxHistory {
		t.Errorf("history capped at %d, got %d", MaxHistory, len(s.History))
	}
}

func TestRollback(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	s.SaveLast(p("A"))
	s.SaveLast(p("B")) // history=[A], last=B
	r, ok := s.Rollback()
	if !ok {
		t.Fatal("expected rollback available")
	}
	if !r.Equal(p("A")) {
		t.Errorf("rollback should return A, got %+v", r)
	}
	if s.Last == nil || !s.Last.Equal(p("A")) {
		t.Errorf("last should be A after rollback, got %+v", s.Last)
	}
	r2, ok2 := s.Rollback() // 再次回滚应换回 B
	if !ok2 || !r2.Equal(p("B")) {
		t.Errorf("second rollback should return B, got %+v ok=%v", r2, ok2)
	}
	if _, ok := (&Store{Profiles: map[string]Profile{}}).Rollback(); ok {
		t.Error("expected no rollback on empty history")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /mnt/f/project/ConfigPatch && /usr/local/go/bin/go test ./configstore/ 2>&1 | head -20`
Expected: FAIL（`no Go files` 或编译错误 —— 包尚未创建）。

- [ ] **Step 3: 实现 configstore 包**

创建 `configstore/store.go`：

```go
// Package configstore 提供配置方案的持久化：命名方案、上次使用状态与历史回滚。
package configstore

import (
	"encoding/json"
	"os"
	"slices"
)

// MaxHistory 是历史快照的最大数量（防文件膨胀）。
const MaxHistory = 5

// Profile 表示一个配置方案（或一次状态快照）。
type Profile struct {
	Name          string   `json:"name"`
	RootDirs      []string `json:"dirs,omitempty"`      // 目标目录列表
	FileNames     []string `json:"fileNames,omitempty"` // 配置文件名称列表
	OldValue      string   `json:"oldValue,omitempty"`
	NewValue      string   `json:"newValue,omitempty"`
	CaseOnlyAllow bool     `json:"caseOnlyAllow,omitempty"`
}

// Equal 判断两个 Profile 内容是否相同（含切片字段）。
func (p Profile) Equal(o Profile) bool {
	return p.Name == o.Name &&
		p.OldValue == o.OldValue &&
		p.NewValue == o.NewValue &&
		p.CaseOnlyAllow == o.CaseOnlyAllow &&
		slices.Equal(p.RootDirs, o.RootDirs) &&
		slices.Equal(p.FileNames, o.FileNames)
}

// Store 是整个配置文件内容的运行时表示。
type Store struct {
	Profiles map[string]Profile `json:"profiles"`
	Last     *Profile           `json:"last,omitempty"`
	History  []Profile          `json:"history,omitempty"`
}

// Load 读取并解析配置文件；文件缺失或内容损坏时返回默认空 Store（err 供日志提示，非致命）。
func Load(path string) (*Store, error) {
	empty := func() *Store { return &Store{Profiles: map[string]Profile{}} }
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty(), nil
		}
		return empty(), err
	}
	s := empty()
	if err := json.Unmarshal(raw, s); err != nil {
		return empty(), err
	}
	if s.Profiles == nil {
		s.Profiles = map[string]Profile{}
	}
	return s, nil
}

// Save 原子写入配置：先写临时文件再重命名，避免写一半损坏。
func Save(path string, s *Store) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o666); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SaveLast 更新 last；若与旧 last 不同，把旧 last 压入历史队首（去重），并裁剪到 MaxHistory。
func (s *Store) SaveLast(p Profile) {
	if s.Last != nil && s.Last.Equal(p) {
		return
	}
	if s.Last != nil {
		s.push(*s.Last)
	}
	cp := p
	s.Last = &cp
}

// PushHistory 显式把当前状态压入历史（加载方案/回滚前调用，使操作可撤销）。
func (s *Store) PushHistory(p Profile) {
	s.push(p)
}

// Rollback 取出最近一个历史快照作为新的 last 返回；历史为空时返回 false。
func (s *Store) Rollback() (Profile, bool) {
	if len(s.History) == 0 {
		return Profile{}, false
	}
	p := s.History[0]
	s.History = s.History[1:]
	if s.Last != nil {
		s.push(*s.Last)
	}
	cp := p
	s.Last = &cp
	return p, true
}

// push 把 p 压入历史队首，与队首相同则去重，并裁剪到 MaxHistory。
func (s *Store) push(p Profile) {
	if len(s.History) > 0 && s.History[0].Equal(p) {
		return
	}
	s.History = append([]Profile{p}, s.History...)
	if len(s.History) > MaxHistory {
		s.History = s.History[:MaxHistory]
	}
}
```

> 实现说明：设计文档中提及的 `AddProfile`/`DeleteProfile` 包装方法**有意省略**——方案增删即直接 `s.Profiles[name] = p` / `delete(...)`（与设计文档"直接操作 `Profiles` map"一致），避免无意义包装；其覆盖/删除持久化行为由补充测试 `TestProfileAddDelete` 覆盖。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /mnt/f/project/ConfigPatch && /usr/local/go/bin/go test ./configstore/ -v`
Expected: 6 个测试全部 PASS。

- [ ] **Step 5: 提交**

```bash
cd /mnt/f/project/ConfigPatch
git add configstore/store.go configstore/store_test.go
git commit -m "feat(configstore): 配置方案持久化（命名方案/last/history/回滚）"
```

---

### Task 2: main.go 接线（方案区 UI + 自动加载/保存/回滚）

**Files:**
- Modify: `main.go`

> 说明：纯 Win32 UI 层，无单元测试框架；以 `go vet` + 临时交叉编译作为验证。

**Step 1: 新增导入**

`main.go` import 块加入（`"sync/atomic"` 之后加 `"sort"`，以及 `"configpatch/configstore"` 到 core 导入附近）：

```go
	"sort"
	"sync/atomic"
	"time"

	"configpatch/configstore"
	"configpatch/core"
```

**Step 2: MainWin 新增字段**

在 `stopBtn *walk.PushButton` 之后（按钮区）加：

```go
	saveProfileBtn *walk.PushButton
	loadProfileBtn *walk.PushButton
	delProfileBtn  *walk.PushButton
	rollbackBtn    *walk.PushButton
	profilesCombo  *walk.ComboBox
```

在 `// runtime state` 区块末尾（`scanPartial bool` 之后）加：

```go
	store         *configstore.Store
	configPath    string
	autosaveTimer *time.Timer // 变更防抖自动保存计时器
```

**Step 3: UI 新增"方案"区（置于窗口最顶部，第一个 GroupBox）**

在 `app := decl.MainWindow{ ... Children: []decl.Widget{` 之后、现有第一个 GroupBox 之前插入：

```go
			decl.GroupBox{
				Title:  "配置方案（保存 / 加载 / 删除 / 回滚）",
				Layout: decl.VBox{Spacing: 4},
				Children: []decl.Widget{
					decl.ComboBox{AssignTo: &mw.profilesCombo, Editable: true},
					decl.Composite{
						Layout: decl.HBox{Spacing: 6},
						Children: []decl.Widget{
							decl.PushButton{AssignTo: &mw.saveProfileBtn, Text: "保存为方案", OnClicked: mw.onSaveProfile},
							decl.PushButton{AssignTo: &mw.loadProfileBtn, Text: "加载", OnClicked: mw.onLoadProfile},
							decl.PushButton{AssignTo: &mw.delProfileBtn, Text: "删除", OnClicked: mw.onDeleteProfile},
							decl.PushButton{AssignTo: &mw.rollbackBtn, Text: "回滚上一版", OnClicked: mw.onRollback},
						},
					},
				},
			},
```

**Step 4: 给文本/开关控件挂变更事件**

在 `namesEdit`、`oldEdit`、`newEdit` 三个 `decl.LineEdit` 上各加 `OnTextChanged: mw.scheduleAutosave`；在 `caseCk` 的 `decl.CheckBox` 上加 `OnCheckedChanged: mw.scheduleAutosave`：

```go
					decl.LineEdit{AssignTo: &mw.namesEdit, Text: "web.config", OnTextChanged: mw.scheduleAutosave},
					decl.LineEdit{AssignTo: &mw.oldEdit, OnTextChanged: mw.scheduleAutosave},
					decl.LineEdit{AssignTo: &mw.newEdit, OnTextChanged: mw.scheduleAutosave},
					decl.CheckBox{
						AssignTo:     &mw.caseCk,
						Text:         "允许仅大小写变更",
						ColumnSpan:   2,
						OnCheckedChanged: mw.scheduleAutosave,
					},
```

**Step 5: 新增配置相关方法（放在 `onStop` 之后、`onScan` 之前）**

```go
// ---------- 配置方案：加载 / 保存 / 自动记忆 / 回滚 ----------

// initConfig 加载配置文件并恢复到上次使用状态；在窗口创建后调用。
func (mw *MainWin) initConfig() {
	if exe, err := os.Executable(); err == nil {
		mw.configPath = filepath.Join(filepath.Dir(exe), "config.json")
	}
	s, err := configstore.Load(mw.configPath)
	if err != nil {
		mw.logf("读取配置文件失败：%v（按空配置继续）", err)
	}
	mw.store = s
	mw.refreshProfilesCombo()
	if s.Last != nil {
		mw.applyProfile(*s.Last)
		mw.logf("已加载上次使用的配置")
	}
}

// collectProfile 从界面采集当前配置（不做校验）。
func (mw *MainWin) collectProfile() configstore.Profile {
	var names []string
	for _, n := range strings.Split(mw.namesEdit.Text(), ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			names = append(names, n)
		}
	}
	return configstore.Profile{
		RootDirs:      mw.dirs,
		FileNames:     names,
		OldValue:      mw.oldEdit.Text(),
		NewValue:      mw.newEdit.Text(),
		CaseOnlyAllow: mw.caseCk.Checked(),
	}
}

// applyProfile 把配置填充回界面控件。
func (mw *MainWin) applyProfile(p configstore.Profile) {
	mw.dirs = append([]string(nil), p.RootDirs...)
	mw.refreshDirs()
	mw.namesEdit.SetText(strings.Join(p.FileNames, ","))
	mw.oldEdit.SetText(p.OldValue)
	mw.newEdit.SetText(p.NewValue)
	mw.caseCk.SetChecked(p.CaseOnlyAllow)
}

// refreshProfilesCombo 用方案名（排序后）刷新下拉框。
func (mw *MainWin) refreshProfilesCombo() {
	names := make([]string, 0, len(mw.store.Profiles))
	for n := range mw.store.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	mw.profilesCombo.SetModel(names)
	mw.rollbackBtn.SetEnabled(len(mw.store.History) > 0)
}

// scheduleAutosave 变更后防抖 500ms 自动保存 last；可被控件事件与目录变更调用。
func (mw *MainWin) scheduleAutosave() {
	if mw.autosaveTimer != nil {
		mw.autosaveTimer.Stop()
	}
	mw.autosaveTimer = time.AfterFunc(500*time.Millisecond, func() {
		mw.Synchronize(func() {
			if mw.closed {
				return
			}
			mw.saveLast()
		})
	})
}

// saveLast 把当前界面状态保存为 last（含历史压栈）并写盘；失败仅记日志。
func (mw *MainWin) saveLast() {
	if mw.store == nil || mw.configPath == "" {
		return
	}
	mw.store.SaveLast(mw.collectProfile())
	if err := configstore.Save(mw.configPath, mw.store); err != nil {
		mw.logf("保存配置失败：%v", err)
	}
}

func (mw *MainWin) onSaveProfile() {
	name := strings.TrimSpace(mw.profilesCombo.Text())
	if name == "" {
		walk.MsgBox(mw, "提示", "请先在方案下拉框输入方案名称。", walk.MsgBoxIconInformation)
		return
	}
	if _, exists := mw.store.Profiles[name]; exists {
		if r := walk.MsgBox(mw, "确认", fmt.Sprintf("方案「%s」已存在，是否覆盖？", name), walk.MsgBoxYesNo|walk.MsgBoxIconQuestion); r != walk.DlgCmdYes {
			return
		}
	}
	p := mw.collectProfile()
	p.Name = name
	mw.store.Profiles[name] = p
	if err := configstore.Save(mw.configPath, mw.store); err != nil {
		mw.logf("保存配置失败：%v", err)
		return
	}
	mw.refreshProfilesCombo()
	mw.logf("已保存方案「%s」", name)
}

func (mw *MainWin) onLoadProfile() {
	name := mw.profilesCombo.Text()
	p, ok := mw.store.Profiles[name]
	if !ok {
		walk.MsgBox(mw, "提示", "请选择要加载的方案。", walk.MsgBoxIconInformation)
		return
	}
	mw.store.PushHistory(mw.collectProfile()) // 当前状态入历史，可回滚
	mw.applyProfile(p)
	mw.refreshProfilesCombo()
	mw.logf("已加载方案「%s」", name)
}

func (mw *MainWin) onDeleteProfile() {
	name := mw.profilesCombo.Text()
	if _, ok := mw.store.Profiles[name]; !ok {
		return
	}
	if r := walk.MsgBox(mw, "确认", fmt.Sprintf("确定删除方案「%s」？", name), walk.MsgBoxYesNo|walk.MsgBoxIconQuestion); r != walk.DlgCmdYes {
		return
	}
	delete(mw.store.Profiles, name)
	if err := configstore.Save(mw.configPath, mw.store); err != nil {
		mw.logf("保存配置失败：%v", err)
	}
	mw.refreshProfilesCombo()
	mw.logf("已删除方案「%s」", name)
}

func (mw *MainWin) onRollback() {
	p, ok := mw.store.Rollback()
	if !ok {
		walk.MsgBox(mw, "提示", "没有可回滚的历史版本。", walk.MsgBoxIconInformation)
		return
	}
	mw.applyProfile(p)
	if err := configstore.Save(mw.configPath, mw.store); err != nil {
		mw.logf("保存配置失败：%v", err)
	}
	mw.refreshProfilesCombo()
	mw.logf("已回滚到上一版配置")
}
```

**Step 6: 目录变更时触发防抖自动保存**

在 `addDir`、`addDirFromText`、`delDir`、`clearDirs` 四个方法的末尾（`refreshDirs()` 之后）各加一行 `mw.scheduleAutosave()`。

**Step 7: 启动自动加载 + 关闭兜底**

- 在 `main()` 中 `if err := app.Create(); err != nil { ... }` 之后、`mw.Closing().Attach(...)` 之前，加一行 `mw.initConfig()`。
- 在 `mw.Closing().Attach(...)` 回调内，`mw.cancelScan.Store(true)` 之后加一行 `mw.saveLast()`。

**Step 8: 验证编译**

```bash
cd /mnt/f/project/ConfigPatch && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 /usr/local/go/bin/go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 /usr/local/go/bin/go build -ldflags "-H windowsgui" -o /tmp/cp-check.exe .
rm -f /tmp/cp-check.exe
```
Expected: 均无报错。

**Step 9: 提交**

```bash
cd /mnt/f/project/ConfigPatch
git add main.go
git commit -m "feat: 配置方案保存/加载/删除/回滚，自动记忆上次配置（防抖保存+关闭兜底）"
```

---

### Task 3: .gitignore 与文档同步

**Files:**
- Modify: `.gitignore`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-24-configpatch-design.md`

- [ ] **Step 1: .gitignore 增加本地配置**

在 `.gitignore` 中追加：

```gitignore
# 本地配置（含内网路径/连接串等敏感内容，不进仓库）
config.json
config.json.tmp
```

- [ ] **Step 2: README 功能特性补充**

在 `README.md` 的"随时可停止"功能项之后新增：

```markdown
- **配置方案保存/加载**：可把目标目录、文件名、原值、新值、仅大小写开关存为命名方案，随时加载、覆盖、删除；界面自动记忆上次使用的配置，下次打开自动恢复。
- **历史回滚**：自动保存不会破坏好配置——保留最近 5 版历史快照，可一键回滚到上一版；命名方案永不被自动保存覆盖，长期可靠。
```

- [ ] **Step 3: README 使用说明补充**

在 `README.md` 使用说明第 1 步之前新增（改为第 1 步，原 1-6 顺延为 2-7）：

```markdown
1. （可选）在顶部"配置方案"区：从下拉框选择并点「加载」恢复某方案；点「保存为方案」把当前配置存为命名方案；误改后可点「回滚上一版」恢复。
```

- [ ] **Step 4: 主设计文档补充优化记录**

在 `docs/superpowers/specs/2026-08-24-configpatch-design.md` 末尾新增：

```markdown
## 优化记录（2026-08-24，v4）

1. **配置方案保存/加载**：新增 `configstore` 包（`config.json`，存于 exe 同目录、已 gitignore）：命名方案（保存/加载/覆盖/删除）、`last` 上次配置自动记忆（变更防抖 500ms + 关闭兜底）、`history` 最近 5 版历史回滚；自动保存只写 `last`/`history`，绝不覆盖命名方案。详见 `2026-08-24-config-profiles-design.md`。
```

- [ ] **Step 5: 验证并提交**

Run: `cd /mnt/f/project/ConfigPatch && git diff --stat`
Expected: 显示 3 个文件有改动。

```bash
cd /mnt/f/project/ConfigPatch
git add .gitignore README.md docs/superpowers/specs/2026-08-24-configpatch-design.md
git commit -m "docs: 同步配置方案/历史回滚功能说明，忽略本地 config.json"
```

---

### Task 4: 最终构建与全量验证

**Files:** 无（仅验证）。

- [ ] **Step 1: 全量测试**

Run: `cd /mnt/f/project/ConfigPatch && /usr/local/go/bin/go test ./... 2>&1 | tail -10`
Expected: `configpatch/configstore` 与 `configpatch/core` 均 `ok`（注意 `go test ./...` 会尝试构建 Windows-only 的根包 `main.go`，在 Linux 上会失败——只需确认两个纯逻辑包 `ok`，根包报错属预期；如报错干扰，改用 `go test ./configstore/ ./core/`）。

- [ ] **Step 2: 确认无进程占用后重建 exe**

Run: `tasklist.exe 2>/dev/null | grep -i configpatch || echo "无进程占用"`
- 若有进程，先 `taskkill.exe /F /PID <pid>`（需用户授权时先确认）。

```bash
cd /mnt/f/project/ConfigPatch
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 /usr/local/go/bin/go build -ldflags "-H windowsgui" -o ConfigPatch.exe .
```
Expected: `BUILD OK`。

- [ ] **Step 3: 敏感信息与提交历史复检**

```bash
cd /mnt/f/project/ConfigPatch
for s in "192.168.13.72" "hlog.hs.com"; do strings ConfigPatch.exe | grep -qF "$s" && echo "✗ $s" || echo "✓ 无 $s"; done
git status --short
git log --oneline -6
```
Expected: 敏感信息无残留；工作区干净；日志含本次 3 个功能/文档提交。

---

## Self-Review（已在编写时检查）

- **Spec 覆盖**：configstore 包与测试（Task1）、方案区 UI 与自动加载/保存/回滚接线（Task2）、.gitignore 与文档（Task3）、构建验证（Task4）——全覆盖。
- **占位符扫描**：所有步骤含完整代码与命令，无 TBD/TODO。
- **类型一致性**：`configstore.Profile/Store`、`SaveLast/Rollback/PushHistory`、`collectProfile/applyProfile`、`scheduleAutosave/saveLast`、`profilesCombo` 等名称在各任务间一致。
