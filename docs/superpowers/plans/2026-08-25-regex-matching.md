# 参数配置支持正则匹配（方案 B）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 ConfigPatch 参数配置增加「启用正则匹配」开关：勾选后配置文件名称按正则完整匹配（不区分大小写）、原字符串按 RE2 正则查找、新字符串支持 `$1` 捕获组引用，校验统一为整文件文本比对。

**Architecture:** 在 `core` 包内新增 `Prepare(c Config) (Config, error)`：先 `Validate` 再预编译正则（文件名每项编译为 `(?i)^(?:...)$`，原字符串编译为 `*regexp.Regexp`），编译结果随 Config 值传递，扫描/执行复用同一份正则。匹配/替换/校验通过 `Config` 上的方法 `matchFileName` / `matchText` / `replaceAll` 统一分支（精确 vs 正则），执行校验改为 `verifyText == newText` 整文件比对，两种模式共用。`configstore.Profile` 增加 `RegexEnable` 字段（旧配置缺失时默认 false，向后兼容）。

**Tech Stack:** Go 1.25 + `regexp`（RE2）、github.com/lxn/walk（GUI）、configpatch/configstore（持久化）。

---

## 运行验证命令说明

本仓库在 Windows 上构建运行。在 **WSL 工作区**里通过 Windows Go 执行：

```bash
cd /mnt/f/project/ConfigPatch
"/mnt/c/Program Files/Go/bin/go.exe" test ./core/... ./configstore/...
"/mnt/c/Program Files/Go/bin/go.exe" build ./...
```

在 **Windows 原生环境**直接使用 `go test ./...`、`go build ./...`。下文统一写作 `go test` / `go build`，执行时按环境替换。

---

## 任务文件结构

- `core/core.go`：`Config` 加 `RegexEnable` 与预编译正则字段；新增 `Prepare`；`Validate` 正则分支；新增 `matchFileName`/`matchText`/`fileMatches`/`replaceAll`；`Scan`/`ExecOne` 改造。
- `core/core_test.go`：正则相关测试。
- `configstore/store.go`：`Profile` 加 `RegexEnable`、`Equal` 接入。
- `configstore/store_test.go`：`RegexEnable` 相等性 + 向后兼容测试。
- `main.go`：新增「启用正则匹配」复选框、联动禁用、ToolTip/说明切换、`collectProfile`/`applyProfile`/`buildConfig` 接线。
- `README.md`：功能、使用说明、RE2 限制。

---

### Task 1: core — 数据模型 + Prepare 预编译

**Files:**
- Modify: `core/core.go`（`Config` 结构体、新增 `Prepare`、`import "regexp"`）
- Test: `core/core_test.go`

- [ ] **Step 1: 写失败测试**

在 `core/core_test.go` 末尾追加：

```go
func TestPrepareRegex(t *testing.T) {
	dir := t.TempDir()
	base := Config{RootDirs: []string{dir}, FileNames: []string{"web.config"}, OldValue: "abc", NewValue: "abd", RegexEnable: true}

	// 合法正则：编译成功并填充预编译正则
	c, err := Prepare(base)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(c.nameRes) != 1 || c.oldRe == nil {
		t.Errorf("expected 1 name regex + old regex, got nameRes=%d oldRe nil=%v", len(c.nameRes), c.oldRe == nil)
	}

	// 非法文件名正则：报错并指明第 N 项
	bad := base
	bad.FileNames = []string{"("}
	if _, err := Prepare(bad); err == nil || !strings.Contains(err.Error(), "配置文件名称第 1 项") {
		t.Errorf("expected 1st filename regex error, got %v", err)
	}
	bad = base
	bad.FileNames = []string{"web.config", "["}
	if _, err := Prepare(bad); err == nil || !strings.Contains(err.Error(), "配置文件名称第 2 项") {
		t.Errorf("expected 2nd filename regex error, got %v", err)
	}

	// 非法原字符串正则：报错
	bad = base
	bad.OldValue = "("
	if _, err := Prepare(bad); err == nil || !strings.Contains(err.Error(), "原字符串正则非法") {
		t.Errorf("expected old value regex error, got %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./core/... -run TestPrepareRegex -v`
Expected: FAIL，编译错误 `undefined: Prepare`（或 `c.nameRes` 不存在）。

- [ ] **Step 3: 实现**

`core/core.go` 的 import 块追加 `"regexp"`：

```go
import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)
```

`Config` 结构体追加字段（放在 `CaseOnlyAllow` 之后）：

```go
	CaseOnlyAllow bool // true: new==old compared exactly (allows case-only change); false: case-insensitive
	RegexEnable   bool // true: 文件名/原字符串按正则解释，新字符串支持 $1 捕获组
	// prepared: 由 Prepare 填充；RegexEnable 时非 nil，否则为 nil。
	nameRes []*regexp.Regexp // 文件名正则（自动加 (?i)^(?:...)$，完整匹配、不区分大小写）
	oldRe   *regexp.Regexp   // 原字符串正则
```

在 `Validate` 函数之后新增：

```go
// Prepare 校验输入并预编译正则；返回的 Config 供 Scan/ExecOne 使用。
// RegexEnable 时：文件名每项编译为 (?i)^(?:<pattern>)$（完整匹配、不区分大小写），
// OldValue 编译为原字符串正则；编译失败返回带具体位置的中文错误。
func Prepare(c Config) (Config, error) {
	if err := Validate(c); err != nil {
		return c, err
	}
	if !c.RegexEnable {
		return c, nil
	}
	for i, n := range c.FileNames {
		re, err := regexp.Compile("(?i)^(?:" + n + ")$")
		if err != nil {
			return c, fmt.Errorf("配置文件名称第 %d 项正则非法: %v", i+1, err)
		}
		c.nameRes = append(c.nameRes, re)
	}
	re, err := regexp.Compile(c.OldValue)
	if err != nil {
		return c, fmt.Errorf("原字符串正则非法: %v", err)
	}
	c.oldRe = re
	return c, nil
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./core/... -run TestPrepareRegex -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add core/core.go core/core_test.go
git commit -m "feat(core): Config 增加 RegexEnable 与 Prepare 正则预编译"
```

---

### Task 2: core — Validate 正则模式跳过「新值=原值」拦截

**Files:**
- Modify: `core/core.go`（`Validate`）
- Test: `core/core_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestValidateRegexSkipsSameValueCheck(t *testing.T) {
	dir := t.TempDir()
	// 正则模式：原字符串是正则，无法与字面量比较，跳过拦截
	c := Config{RootDirs: []string{dir}, FileNames: []string{"web.config"}, OldValue: "a+", NewValue: "a+", RegexEnable: true}
	if err := Validate(c); err != nil {
		t.Errorf("regex mode must skip same-value check, got %v", err)
	}
	// 精确模式仍拦截
	c2 := Config{RootDirs: []string{dir}, FileNames: []string{"web.config"}, OldValue: "abc", NewValue: "abc"}
	if err := Validate(c2); err != ErrSameValue {
		t.Errorf("literal mode must keep same-value check, got %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./core/... -run TestValidateRegexSkipsSameValueCheck -v`
Expected: FAIL，regex 分支未实现，返回 `ErrSameValue`。

- [ ] **Step 3: 实现**

`core/core.go` 的 `Validate` 中，把「新值=原值」判断块替换为：

```go
	// "新值=原值" guard：默认比较忽略大小写；"允许仅大小写变更"开启时精确比较。
	// 正则模式：原字符串是正则表达式，无法与字面量比较，跳过该拦截。
	if c.RegexEnable {
		// no-op
	} else if c.CaseOnlyAllow {
		if c.OldValue == c.NewValue {
			return ErrSameValue
		}
	} else {
		if strings.EqualFold(c.OldValue, c.NewValue) {
			return ErrSameValue
		}
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./core/... -run TestValidateRegexSkipsSameValueCheck -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add core/core.go core/core_test.go
git commit -m "feat(core): 正则模式跳过新值=原值/仅大小写拦截"
```

---

### Task 3: core — 匹配助手 + 扫描（正则文件名/内容）

**Files:**
- Modify: `core/core.go`（新增 `matchFileName`/`matchText`/`fileMatches`，改造 `Scan`）
- Test: `core/core_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestScanRegexFileNames(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"web-1.config":   `<add key="k" value="abc" />`,
		"web-2.config":   "abc",
		"Web-1.Config":   "abc", // 大小写不同也应命中
		"myweb-1.config": "abc", // 部分命中但不应整名匹配
		"web.config.bak": "abc", // 后缀不匹配
		"app.config":     "abc",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Prepare(Config{
		RootDirs:   []string{root},
		FileNames:  []string{`^web-.*\.config$`},
		OldValue:   "abc",
		NewValue:   "xyz",
		RegexEnable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, _, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, h := range hits {
		got[filepath.Base(h.Path)] = true
	}
	for _, want := range []string{"web-1.config", "web-2.config", "Web-1.Config"} {
		if !got[want] {
			t.Errorf("missing hit %s; got %v", want, got)
		}
	}
	for _, not := range []string{"myweb-1.config", "web.config.bak", "app.config"} {
		if got[not] {
			t.Errorf("unexpected hit %s; got %v", not, got)
		}
	}
}

func TestRegexCaseSensitive(t *testing.T) {
	root := t.TempDir()
	// 正则默认区分大小写
	c, _ := Prepare(Config{
		RootDirs:   []string{root},
		FileNames:  []string{"web.config"},
		OldValue:   "abc",
		NewValue:   "xyz",
		RegexEnable: true,
	})
	if !c.matchText("hello abc") {
		t.Error("expected exact-case match")
	}
	if c.matchText("hello ABC") {
		t.Error("regex must be case-sensitive by default")
	}
	// (?i) 前缀不区分大小写
	ci, _ := Prepare(Config{
		RootDirs:   []string{root},
		FileNames:  []string{"web.config"},
		OldValue:   "(?i)abc",
		NewValue:   "xyz",
		RegexEnable: true,
	})
	if !ci.matchText("hello ABC") {
		t.Error("(?i) prefix must make match case-insensitive")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./core/... -run 'TestScanRegexFileNames|TestRegexCaseSensitive' -v`
Expected: FAIL，编译错误 `c.matchText undefined`（或命中数不对）。

- [ ] **Step 3: 实现**

`core/core.go` 新增三个辅助函数（放在 `FileContainsCI` 之前）：

```go
// matchFileName 判断文件名是否命中：正则模式任一正则完整匹配（不区分大小写），
// 精确模式大小写不敏感全等。names 为精确模式的预处理小写名集合。
func (c Config) matchFileName(name string, names map[string]struct{}) bool {
	if c.RegexEnable {
		for _, re := range c.nameRes {
			if re.MatchString(name) {
				return true
			}
		}
		return false
	}
	_, ok := names[strings.ToLower(name)]
	return ok
}

// matchText 判断已解码文本是否命中原字符串。
func (c Config) matchText(text string) bool {
	if c.RegexEnable {
		return c.oldRe.MatchString(text)
	}
	return ContainsCI(text, c.OldValue)
}

// fileMatches 读取文件解码后判断内容是否命中原字符串。
func fileMatches(path string, c Config) (bool, error) {
	text, err := ReadText(path)
	if err != nil {
		return false, err
	}
	return c.matchText(text), nil
}
```

把 `Scan` 中文件名判断与内容判断两处替换为：

```go
			if !c.matchFileName(d.Name(), names) {
				return nil
			}
			contains, rerr := fileMatches(path, c)
```

即：删除原来的 `if _, ok := names[strings.ToLower(d.Name())]; !ok { return nil }` 与 `contains, rerr := FileContainsCI(path, c.OldValue)` 两行，换成上面两行。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./core/... -run 'TestScanRegexFileNames|TestRegexCaseSensitive' -v`
Expected: PASS。

- [ ] **Step 5: 回归 + 提交**

Run: `go test ./core/...`
Expected: 全部 PASS（含原有 `TestScanAndExec` 等精确模式用例）。

```bash
git add core/core.go core/core_test.go
git commit -m "feat(core): Scan 支持正则文件名完整匹配与正则内容匹配"
```

---

### Task 4: core — 替换与校验（捕获组 + 整文件文本比对）

**Files:**
- Modify: `core/core.go`（新增 `replaceAll`，改造 `ExecOne` 复检/替换/校验）
- Test: `core/core_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestRegexReplaceWithCapture(t *testing.T) {
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	content := "Host=192.168.1.10\r\nHost=10.0.0.5\r\nnothing"
	if err := os.WriteFile(cfgFile, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	c, err := Prepare(Config{
		RootDirs:   []string{root},
		FileNames:  []string{"web.config"},
		OldValue:   `Host=(\d+\.\d+\.\d+\.\d+)`,
		NewValue:   `Host=$1:8080`,
		RegexEnable: true,
	})
	if err != nil {
		t.Fatal(err)
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
		t.Fatalf("exec failed: %v", res.Err)
	}
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if !res.Verified {
		t.Error("expected verified")
	}
	if res.RolledBack {
		t.Error("expected no rollback")
	}
	if res.ReplacedCount != 2 {
		t.Errorf("expected 2 replacements, got %d", res.ReplacedCount)
	}
	got, _ := os.ReadFile(cfgFile)
	want := "Host=192.168.1.10:8080\r\nHost=10.0.0.5:8080\r\nnothing"
	if string(got) != want {
		t.Errorf("content mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestRegexVerifyWhenNewMatchesPattern(t *testing.T) {
	// 用户场景：新字符串本身命中原正则，校验必须通过、不得回滚。
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	content := `<add key="version" value="2025" />`
	if err := os.WriteFile(cfgFile, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	c, err := Prepare(Config{
		RootDirs:   []string{root},
		FileNames:  []string{"web.config"},
		OldValue:   `value="\d+"`,
		NewValue:   `value="2026"`,
		RegexEnable: true,
	})
	if err != nil {
		t.Fatal(err)
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
		t.Fatalf("exec failed: %v", res.Err)
	}
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if !res.Verified {
		t.Error("expected verified even though new text still matches old pattern")
	}
	if res.RolledBack {
		t.Error("must NOT roll back when replacement itself matches the old regex")
	}
	got, _ := os.ReadFile(cfgFile)
	if !strings.Contains(string(got), `value="2026"`) {
		t.Errorf("original not updated: %s", got)
	}
}

func TestRegexExecSkipsWhenNoLongerMatches(t *testing.T) {
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	if err := os.WriteFile(cfgFile, []byte("hello abc"), 0o666); err != nil {
		t.Fatal(err)
	}
	c, err := Prepare(Config{
		RootDirs:   []string{root},
		FileNames:  []string{"web.config"},
		OldValue:   `\d+`,
		NewValue:   "X",
		RegexEnable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, _, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatal("expected 1 hit")
	}
	// 执行时内容已不再命中 -> 跳过、不改动、不备份
	if err := os.WriteFile(cfgFile, []byte("hello"), 0o666); err != nil {
		t.Fatal(err)
	}
	res := ExecOne(hits[0], c)
	if res.Skipped == "" {
		t.Errorf("expected skip, got %+v", res)
	}
	if res.Err != nil {
		t.Errorf("expected no error, got %v", res.Err)
	}
	got, _ := os.ReadFile(cfgFile)
	if string(got) != "hello" {
		t.Errorf("original was modified: %q", got)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./core/... -run 'TestRegexReplaceWithCapture|TestRegexVerifyWhenNewMatchesPattern|TestRegexExecSkipsWhenNoLongerMatches' -v`
Expected: FAIL（如 `c.replaceAll undefined`、校验按字面量误判等）。

- [ ] **Step 3: 实现**

`core/core.go` 在 `ReplaceAllCI` 之后新增：

```go
// replaceAll 按当前模式替换所有命中并返回替换次数。
// 正则模式：ReplaceAllString 天然支持 $1/${name} 捕获组；计数取非重叠命中数。
func (c Config) replaceAll(text string) (string, int) {
	if c.RegexEnable {
		return c.oldRe.ReplaceAllString(text, c.NewValue), len(c.oldRe.FindAllStringIndex(text, -1))
	}
	return ReplaceAllCI(text, c.OldValue, c.NewValue)
}
```

`ExecOne` 中三处替换：

1. 复检（原 `if !ContainsCI(text, c.OldValue) {`）：

```go
	if !c.matchText(text) {
		res.Skipped = "执行时已不再包含原字符串，已跳过"
		step("  - 跳过: %s", res.Skipped)
		return res
	}
```

2. 替换（原 `newText, n := ReplaceAllCI(text, c.OldValue, c.NewValue)`）：

```go
	newText, n := c.replaceAll(text)
	res.ReplacedCount = n
```

3. 校验（原 `if verr == nil && ContainsCI(verifyText, c.NewValue) {`，改为整文件文本比对）：

```go
	verifyText, verr := ReadText(h.Path)
	if verr == nil && verifyText == newText {
		res.Verified = true
		step("  - 校验通过（替换 %d 处）", res.ReplacedCount)
		return res
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./core/... -run 'TestRegexReplaceWithCapture|TestRegexVerifyWhenNewMatchesPattern|TestRegexExecSkipsWhenNoLongerMatches' -v`
Expected: PASS。

- [ ] **Step 5: 回归 + 提交**

Run: `go test ./core/...`
Expected: 全部 PASS（原有用例 `TestScanAndExec`/`TestExecLogsSteps` 在校验改为文本比对后仍应通过，因写入成功时 `verifyText == newText`）。

```bash
git add core/core.go core/core_test.go
git commit -m "feat(core): 正则替换支持捕获组，校验统一为整文件文本比对"
```

---

### Task 5: configstore — Profile 增加 RegexEnable

**Files:**
- Modify: `configstore/store.go`
- Test: `configstore/store_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestProfileEqualRegexEnable(t *testing.T) {
	a := Profile{Name: "A", OldValue: "a", NewValue: "b"}
	b := a
	if !a.Equal(b) {
		t.Error("identical profiles should be equal")
	}
	b.RegexEnable = true
	if a.Equal(b) {
		t.Error("RegexEnable difference must make profiles unequal")
	}
}

func TestLoadWithoutRegexEnableDefaultsFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"profiles": {"A": {"name":"A","oldValue":"a","newValue":"b"}}}`
	if err := os.WriteFile(path, []byte(raw), 0o666); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Profiles["A"].RegexEnable {
		t.Error("missing regexEnable must default to false (backward compatible)")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./configstore/... -run 'TestProfileEqualRegexEnable|TestLoadWithoutRegexEnableDefaultsFalse' -v`
Expected: FAIL，`Equal` 未比较 `RegexEnable`。

- [ ] **Step 3: 实现**

`configstore/store.go` 的 `Profile` 结构体追加字段：

```go
	CaseOnlyAllow bool   `json:"caseOnlyAllow,omitempty"`
	RegexEnable   bool   `json:"regexEnable,omitempty"` // 启用正则匹配（旧配置缺失时默认 false）
```

`Equal` 增加一行比较：

```go
	return p.Name == o.Name &&
		p.OldValue == o.OldValue &&
		p.NewValue == o.NewValue &&
		p.CaseOnlyAllow == o.CaseOnlyAllow &&
		p.RegexEnable == o.RegexEnable &&
		slices.Equal(p.RootDirs, o.RootDirs) &&
		slices.Equal(p.FileNames, o.FileNames)
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./configstore/...`
Expected: 全部 PASS（含原有用例）。

- [ ] **Step 5: 提交**

```bash
git add configstore/store.go configstore/store_test.go
git commit -m "feat(configstore): Profile 增加 RegexEnable 字段并接入 Equal"
```

---

### Task 6: main.go — UI 复选框 + 联动 + 接线

**Files:**
- Modify: `main.go`

> GUI 无法单元测试，本节以 `go build` + `go vet` 验证编译，并在 Windows 手动验证（见 Step 4 清单）。

- [ ] **Step 1: MainWin 结构体加字段**

`main.go` 的 `MainWin` 结构体，在 `caseCk` 行后追加：

```go
	namesEdit *walk.LineEdit
	oldEdit   *walk.LineEdit
	newEdit   *walk.LineEdit
	caseCk    *walk.CheckBox
	regexCk   *walk.CheckBox
	regexHint *walk.Label
```

- [ ] **Step 2: 参数配置区加复选框 + 说明标签接管**

把参数配置 `GroupBox` 内原有的「允许仅大小写变更」复选框与说明 `Label` 两段替换为三行（正则复选框、大小写复选框、说明标签，说明标签赋给 `regexHint`）：

```go
				decl.CheckBox{
					AssignTo:         &mw.regexCk,
					Text:             "启用正则匹配",
					Row:              3,
					Column:           0,
					ColumnSpan:       3,
					MinSize:          decl.Size{Width: 220},
					OnCheckedChanged: mw.onRegexToggle,
				},
				decl.CheckBox{
					AssignTo:         &mw.caseCk,
					Text:             "允许仅大小写变更",
					Row:              4,
					Column:           0,
					ColumnSpan:       3,
					MinSize:          decl.Size{Width: 220},
					OnCheckedChanged: mw.scheduleAutosave,
				},
				decl.Label{
					AssignTo: &mw.regexHint,
					Text:     "说明：勾选后，新值与原值仅大小写不同也会执行替换；不勾选则视为相同并中止。",
					Row:      5,
					Column:   0,
					ColumnSpan: 3,
				},
```

- [ ] **Step 3: 新增正则开关处理器 + 联动状态刷新**

在 `onSwapOldNew` 函数之后新增：

```go
// onRegexToggle 切换正则开关：联动禁用「允许仅大小写变更」、刷新说明与 ToolTip、触发自动保存。
func (mw *MainWin) onRegexToggle() {
	mw.applyRegexUIState()
	mw.scheduleAutosave()
}

// applyRegexUIState 根据正则开关刷新界面联动状态。
func (mw *MainWin) applyRegexUIState() {
	on := mw.regexCk.Checked()
	mw.caseCk.SetEnabled(!on)
	if on {
		mw.regexHint.SetText("说明：正则模式——文件名须匹配整个名称（不区分大小写）；原字符串为 RE2 正则，默认区分大小写，(?i) 前缀不区分；新字符串 $1/${name} 为捕获组引用，$$ 为字面 $；不支持 lookaround 与 \\1。")
		mw.namesEdit.SetToolTipText("多个正则用逗号分隔，须匹配整个文件名（自动加 ^...$），不区分大小写")
		mw.oldEdit.SetToolTipText("RE2 正则，默认区分大小写；(?i) 前缀表示不区分大小写")
		mw.newEdit.SetToolTipText("支持 $1/${name} 捕获组引用；$$ 表示字面 $")
	} else {
		mw.regexHint.SetText("说明：勾选后，新值与原值仅大小写不同也会执行替换；不勾选则视为相同并中止。")
		mw.namesEdit.SetToolTipText("多个文件名用逗号分隔，如 web.config,app.config")
		mw.oldEdit.SetToolTipText("要查找并替换的字符串，不区分大小写")
		mw.newEdit.SetToolTipText("替换为的字符串")
	}
}
```

- [ ] **Step 4: collectProfile / applyProfile / buildConfig 接线**

`collectProfile` 返回值追加：

```go
		CaseOnlyAllow: mw.caseCk.Checked(),
		RegexEnable:   mw.regexCk.Checked(),
```

`applyProfile` 把 `mw.caseCk.SetChecked(p.CaseOnlyAllow)` 一行替换为（先设正则开关、再按正则状态刷新联动、大小写开关在正则开启时清空并禁用）：

```go
	mw.regexCk.SetChecked(p.RegexEnable)
	mw.applyRegexUIState()
	mw.caseCk.SetChecked(p.CaseOnlyAllow && !p.RegexEnable)
```

`buildConfig` 的 `core.Config` 字面量追加 `RegexEnable`，并把 `core.Validate(cfg)` 改为 `core.Prepare(cfg)`（Prepare 内部先 Validate，再预编译正则；ErrSameValue 等错误原样返回，`showConfigError` 特判不受影响）：

```go
	cfg := core.Config{
		RootDirs:      mw.dirs,
		FileNames:     parseFileNames(mw.namesEdit.Text()),
		OldValue:      mw.oldEdit.Text(),
		NewValue:      mw.newEdit.Text(),
		CaseOnlyAllow: mw.caseCk.Checked(),
		RegexEnable:   mw.regexCk.Checked(),
	}
	return core.Prepare(cfg)
```

（同时删除 `buildConfig` 里原有的 `if err := core.Validate(cfg); err != nil { return cfg, err }` 两行。）

- [ ] **Step 5: 编译 + vet 验证**

Run: `go build ./... && go vet ./...`
Expected: 无错误输出。

- [ ] **Step 6: Windows 手动验证清单**

在 Windows 上运行 `ConfigPatch.exe`，勾选「启用正则匹配」后确认：
1. 「允许仅大小写变更」复选框变灰不可点；
2. 说明文字与三个输入框悬停提示切换为正则版；
3. 保存方案 → 重开程序，正则开关状态恢复；
4. 非法正则（如文件名填 `(`）点「扫描预览」弹框提示"配置文件名称第 1 项正则非法"。

- [ ] **Step 7: 提交**

```bash
git add main.go
git commit -m "feat(ui): 参数配置新增启用正则匹配开关及联动"
```

---

### Task 7: README 更新

**Files:**
- Modify: `README.md`

- [ ] **Step 1: 功能特性追加**

在「大小写不敏感查找/替换」特性行后追加：

```markdown
- **正则匹配开关**：勾选「启用正则匹配」后，配置文件名称按正则完整匹配（不区分大小写、自动加 ^...$ 锚定），原字符串按 RE2 正则查找，新字符串支持 `$1`/`${name}` 捕获组引用（`$$` 表示字面 `$`）；默认不勾选即为精确匹配。正则模式默认区分大小写，可用 `(?i)` 前缀改为不区分。
```

- [ ] **Step 2: 使用说明补充**

「参数配置」步骤 4 之后追加一行说明：

```markdown
   - 需要模式匹配时，勾选「启用正则匹配」：文件名/原字符串按正则解释，新字符串支持捕获组引用。
```

- [ ] **Step 3: 兼容性说明补充 RE2 限制**

在「兼容性说明（重要）」小节内追加：

```markdown
### 正则语法（RE2）

正则匹配使用 Go 的 RE2 引擎：**不支持** lookahead/lookbehind（`(?=...)`、`(?<=...)`）与模式内反向引用（`\1`）；替换侧支持 `$1`/`${name}` 捕获组引用。习惯 Notepad++（PCRE）写法时请注意差异。
```

- [ ] **Step 4: 提交**

```bash
git add README.md
git commit -m "docs: 正则匹配功能与 RE2 限制说明"
```

---

### Task 8: 全量回归与构建

**Files:**
- 无（验证任务）

- [ ] **Step 1: 全量测试**

Run: `go test ./...`
Expected: `ok  configpatch/core`、`ok  configpatch/configstore`（main 无测试，正常跳过）。

- [ ] **Step 2: vet 与构建**

Run: `go vet ./... && go build ./...`
Expected: 无输出，退出码 0。

- [ ] **Step 3: 提交（如有剩余改动）**

若上述命令产生未提交的格式修正，一并提交：

```bash
git add -A
git commit -m "chore: 全量回归通过"
```

---

## Self-Review

**1. Spec coverage:**
- 交互与 UI（复选框、联动禁用、ToolTip/说明切换）→ Task 6 ✅
- 字段行为（文件名完整匹配不区分大小写、原字符串 RE2 区分大小写、新字符串捕获组）→ Task 1/3/4 ✅
- 核心逻辑（Prepare 编译一次、Scan 匹配、替换、整文件文本比对校验、正则跳过拦截）→ Task 1/2/3/4 ✅
- 持久化与兼容性（Profile.RegexEnable、Equal、向后兼容默认 false）→ Task 5 ✅
- 错误处理（非法正则指明第 N 项、RE2 限制文档）→ Task 1/6/7 ✅
- 备份范围、执行时复检 → Task 4（`TestRegexExecSkipsWhenNoLongerMatches`）✅

**2. Placeholder scan:** 无 TBD/TODO；每个代码步骤都给出完整代码。✅

**3. Type consistency:** `Prepare` 返回 `(Config, error)` 一致；`matchFileName(name string, names map[string]struct{})`、`matchText(text string) bool`、`replaceAll(text string) (string, int)` 在任务间签名一致；`Config.nameRes []*regexp.Regexp` / `oldRe *regexp.Regexp` 为包内未导出字段，仅在 `core` 包内使用。✅
