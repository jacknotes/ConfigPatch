package core

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/text/encoding/unicode"
)

func TestReplaceAllCI(t *testing.T) {
	cases := []struct {
		s, old, new, want string
		wantCount         int
	}{
		{"abc abc ABC", "abc", "X", "X X X", 3},
		{"Hello, World", "world", "earth", "Hello, earth", 1},
		{"nope", "xyz", "q", "nope", 0},
		{"aaa", "aa", "b", "ba", 1}, // overlapping occurrences are not re-matched
		{"", "a", "b", "", 0},
		{"中文 ABC 中文", "abc", "zz", "中文 zz 中文", 1},
		{"value=\"http://hlog.example.com:9200\"", "value=\"http://hlog.example.com:9200\"", "value=\"http://hlog.example.com:9201\"", "value=\"http://hlog.example.com:9201\"", 1},
	}
	for _, c := range cases {
		got, n := ReplaceAllCI(c.s, c.old, c.new)
		if got != c.want || n != c.wantCount {
			t.Errorf("ReplaceAllCI(%q,%q,%q) = (%q,%d), want (%q,%d)", c.s, c.old, c.new, got, n, c.want, c.wantCount)
		}
	}
}

func TestContainsCI(t *testing.T) {
	if !ContainsCI("AbC xYz", "xyz") {
		t.Error("expected case-insensitive match")
	}
	if ContainsCI("abc", "q") {
		t.Error("expected no match")
	}
}

func TestDetectEncoding(t *testing.T) {
	if k := DetectEncoding([]byte("<xml>plain ascii</xml>")); k != EncUTF8 {
		t.Errorf("ascii should be EncUTF8, got %v", k)
	}
	if k := DetectEncoding([]byte{0xEF, 0xBB, 0xBF, '<'}); k != EncUTF8BOM {
		t.Errorf("utf8 bom should be EncUTF8BOM, got %v", k)
	}
	if k := DetectEncoding([]byte{0xFF, 0xFE, '<', 0}); k != EncUTF16LE {
		t.Errorf("utf16le bom should be EncUTF16LE, got %v", k)
	}
	if k := DetectEncoding([]byte{0xFE, 0xFF, 0, '<'}); k != EncUTF16BE {
		t.Errorf("utf16be bom should be EncUTF16BE, got %v", k)
	}
	// 中文 GBK bytes (invalid UTF-8) should fall back to GBK
	if k := DetectEncoding([]byte{0xD6, 0xD0, 0xCE, 0xC4}); k != EncGBK {
		t.Errorf("gbk should be EncGBK, got %v", k)
	}
}

func TestEncodingRoundTrip(t *testing.T) {
	content := "<?xml version=\"1.0\"?>\r\n<add key=\"log_connString\" value=\"http://hlog.example.com:9200\" />\r\n"

	// UTF-8 no BOM
	raw := []byte(content)
	if k := DetectEncoding(raw); k != EncUTF8 {
		t.Fatalf("expected EncUTF8, got %v", k)
	}
	text, err := Decode(EncUTF8, raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encode(EncUTF8, text)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, out) {
		t.Errorf("UTF8 roundtrip mismatch:\n in=%q\nout=%q", raw, out)
	}

	// UTF-8 with BOM
	rawBOM := append([]byte{0xEF, 0xBB, 0xBF}, content...)
	if k := DetectEncoding(rawBOM); k != EncUTF8BOM {
		t.Fatalf("expected EncUTF8BOM, got %v", k)
	}
	textBOM, err := Decode(EncUTF8BOM, rawBOM)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(textBOM, "\uFEFF") {
		t.Error("BOM was not stripped on decode")
	}
	outBOM, err := Encode(EncUTF8BOM, textBOM)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawBOM, outBOM) {
		t.Errorf("UTF8BOM roundtrip mismatch:\n in=%q\nout=%q", rawBOM, outBOM)
	}

	// UTF-16 LE with BOM
	e16 := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder()
	body16, err := e16.Bytes([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	raw16 := append([]byte{0xFF, 0xFE}, body16...)
	if k := DetectEncoding(raw16); k != EncUTF16LE {
		t.Fatalf("expected EncUTF16LE, got %v", k)
	}
	text16, err := Decode(EncUTF16LE, raw16)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(text16, "\uFEFF") {
		t.Error("UTF16 BOM was not stripped on decode")
	}
	if !strings.Contains(text16, "hlog") {
		t.Errorf("UTF16 decode produced unexpected text: %q", text16)
	}
	out16, err := Encode(EncUTF16LE, text16)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw16, out16) {
		t.Errorf("UTF16LE roundtrip mismatch:\n in=%q\nout=%q", raw16, out16)
	}

	// GBK roundtrip
	rawGbk := append([]byte{0xD6, 0xD0, 0xCE, 0xC4}, []byte(" value=\"abc\"")...)
	if k := DetectEncoding(rawGbk); k != EncGBK {
		t.Fatalf("expected EncGBK, got %v", k)
	}
	textGbk, err := Decode(EncGBK, rawGbk)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(textGbk, "中文") {
		t.Errorf("GBK decode produced unexpected text: %q", textGbk)
	}
	outGbk, err := Encode(EncGBK, textGbk)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawGbk, outGbk) {
		t.Errorf("GBK roundtrip mismatch:\n in=%x\nout=%x", rawGbk, outGbk)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	base := Config{RootDirs: []string{dir}, FileNames: []string{"web.config"}, OldValue: "abc", NewValue: "abd"}

	if err := Validate(base); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	// default: case-insensitive equality -> ErrSameValue
	c := base
	c.NewValue = "ABC"
	if err := Validate(c); err != ErrSameValue {
		t.Errorf("expected ErrSameValue, got %v", err)
	}
	// with CaseOnlyAllow: case-only change is allowed
	c = base
	c.CaseOnlyAllow = true
	c.NewValue = "ABC"
	if err := Validate(c); err != nil {
		t.Errorf("expected nil with CaseOnlyAllow, got %v", err)
	}
	// with CaseOnlyAllow: exact equality still blocked
	c = base
	c.CaseOnlyAllow = true
	c.NewValue = "abc"
	if err := Validate(c); err != ErrSameValue {
		t.Errorf("expected ErrSameValue (exact), got %v", err)
	}
	// blank old value blocked
	c = base
	c.OldValue = "   "
	if err := Validate(c); err == nil {
		t.Error("expected error for blank old value")
	}
	// blank new value blocked
	c = base
	c.NewValue = " \t "
	if err := Validate(c); err == nil {
		t.Error("expected error for blank new value")
	}
	// no dirs blocked
	c = base
	c.RootDirs = nil
	if err := Validate(c); err == nil {
		t.Error("expected error for no dirs")
	}
	// unreadable dir blocked
	c = base
	c.RootDirs = []string{filepath.Join(dir, "does-not-exist")}
	if err := Validate(c); err == nil {
		t.Error("expected error for missing dir")
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "web.config-20260101000000")
	if err := os.WriteFile(p, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	u := uniquePath(p)
	if u == p {
		t.Error("expected unique path to differ from existing path")
	}
	if _, err := os.Stat(u); !os.IsNotExist(err) {
		t.Errorf("expected unique path to not exist, got %v", err)
	}
}

func TestScanAndExec(t *testing.T) {
	root := t.TempDir()

	// site1: matching web.config
	site1 := filepath.Join(root, "site1")
	if err := os.MkdirAll(site1, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(site1, "web.config")
	content := "<?xml version=\"1.0\"?>\r\n<add key=\"log_connString\" value=\"http://hlog.example.com:9200\" />\r\n"
	if err := os.WriteFile(cfg, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}

	// site2: non-matching web.config
	site2 := filepath.Join(root, "site2")
	if err := os.MkdirAll(site2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site2, "web.config"), []byte("nothing here"), 0o666); err != nil {
		t.Fatal(err)
	}

	// site3: matching file but different filename case (must still be found)
	site3 := filepath.Join(root, "site3")
	if err := os.MkdirAll(site3, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site3, "Web.Config"), []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}

	// backup-config deep dir must be skipped
	deep := filepath.Join(site1, "backup-config", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "web.config"), []byte("should not be scanned"), 0o666); err != nil {
		t.Fatal(err)
	}

	c := Config{
		RootDirs:  []string{root},
		FileNames: []string{"web.config"},
		OldValue:  "http://hlog.example.com:9200",
		NewValue:  "http://hlog.example.com:9201",
	}

	hits, derrs, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(derrs) != 0 {
		t.Errorf("unexpected dir errors: %+v", derrs)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d: %+v", len(hits), hits)
	}

	// exec on the site1 hit
	var h Hit
	for _, x := range hits {
		if x.Path == cfg {
			h = x
		}
	}
	if h.Path == "" {
		t.Fatal("site1 hit not found")
	}
	res := ExecOne(h, c)
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
	if res.ReplacedCount != 1 {
		t.Errorf("expected 1 replacement, got %d", res.ReplacedCount)
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Errorf("backup file missing: %v", err)
	}
	if _, err := os.Stat(res.NewFilePath); err != nil {
		t.Errorf("new file missing: %v", err)
	}

	got, _ := os.ReadFile(cfg)
	if !strings.Contains(string(got), "http://hlog.example.com:9201") {
		t.Errorf("original not updated: %s", got)
	}
	if strings.Contains(string(got), "http://hlog.example.com:9200") {
		t.Errorf("old value still present in original: %s", got)
	}

	bak, _ := os.ReadFile(res.BackupPath)
	if string(bak) != content {
		t.Errorf("backup content mismatch: %q", bak)
	}

	nw, _ := os.ReadFile(res.NewFilePath)
	want := strings.ReplaceAll(content, "http://hlog.example.com:9200", "http://hlog.example.com:9201")
	if string(nw) != want {
		t.Errorf("new file content mismatch:\n got=%q\nwant=%q", nw, want)
	}
}

func TestExecLogsSteps(t *testing.T) {
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	content := "<add key=\"k\" value=\"abc\" />"
	if err := os.WriteFile(cfgFile, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}

	var lines []string
	c := Config{
		RootDirs:  []string{root},
		FileNames: []string{"web.config"},
		OldValue:  "abc",
		NewValue:  "xyz",
		Logf: func(format string, args ...interface{}) {
			lines = append(lines, fmt.Sprintf(format, args...))
		},
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
	if res.Verified != true {
		t.Fatal("expected verified")
	}

	joined := strings.Join(lines, "\n")
	for _, want := range []string{"处理:", "确认仍包含原值", "备份原文件", "生成新文件", "覆盖原文件", "校验通过"} {
		if !strings.Contains(joined, want) {
			t.Errorf("step log missing %q; got:\n%s", want, joined)
		}
	}

	// the stages must be recorded in chronological order
	order := []string{"处理:", "确认仍包含原值", "备份原文件", "生成新文件", "覆盖原文件", "校验通过"}
	last := -1
	for _, s := range order {
		idx := strings.Index(joined, s)
		if idx <= last {
			t.Errorf("step %q out of order; got:\n%s", s, joined)
		}
		last = idx
	}
}

func TestExecSkipsWhenNoLongerMatches(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "web.config")
	if err := os.WriteFile(cfg, []byte("hello abc"), 0o666); err != nil {
		t.Fatal(err)
	}
	c := Config{RootDirs: []string{root}, FileNames: []string{"web.config"}, OldValue: "abc", NewValue: "xyz"}
	hits, _, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatal("expected 1 hit")
	}
	// change the file so it no longer matches at exec time
	if err := os.WriteFile(cfg, []byte("hello"), 0o666); err != nil {
		t.Fatal(err)
	}
	res := ExecOne(hits[0], c)
	if res.Skipped == "" {
		t.Errorf("expected skip, got %+v", res)
	}
	if res.Err != nil {
		t.Errorf("expected no error, got %v", res.Err)
	}
	// original must remain untouched
	got, _ := os.ReadFile(cfg)
	if string(got) != "hello" {
		t.Errorf("original was modified: %q", got)
	}
}

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
	if len(hits) != 1 {
		t.Errorf("expected exactly 1 partial hit, got %d", len(hits))
	}
}

func TestExecCancelled(t *testing.T) {
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	content := "<add key=\"k\" value=\"abc\" />"
	if err := os.WriteFile(cfgFile, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	// 仅在执行阶段模拟取消：扫描需先成功拿到命中，执行时才中止。
	cancelled := false
	c := Config{
		RootDirs:  []string{root},
		FileNames: []string{"web.config"},
		OldValue:  "abc",
		NewValue:  "xyz",
		Cancel:    func() bool { return cancelled },
	}
	hits, _, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	cancelled = true // 执行阶段用户请求中止
	res := ExecOne(hits[0], c)
	if res.Err != nil {
		t.Fatalf("expected no error, got %v", res.Err)
	}
	if res.Skipped != "用户已请求中止" {
		t.Fatalf("expected skipped (cancelled), got %q", res.Skipped)
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

func TestScanRegexFileNames(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"web-1.config":   `<add key="k" value="abc" />`,
		"web-2.config":   "abc",
		"Web-3.Config":   "abc", // 大小写不同也应命中（避免与 web-1.config 在大小写不敏感文件系统上冲突）
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
		RootDirs:    []string{root},
		FileNames:   []string{`^web-.*\.config$`},
		OldValue:    "abc",
		NewValue:    "xyz",
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
	for _, want := range []string{"web-1.config", "web-2.config", "Web-3.Config"} {
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
	c, err := Prepare(Config{
		RootDirs:    []string{root},
		FileNames:   []string{"web.config"},
		OldValue:    "abc",
		NewValue:    "xyz",
		RegexEnable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !c.matchText("hello abc") {
		t.Error("expected exact-case match")
	}
	if c.matchText("hello ABC") {
		t.Error("regex must be case-sensitive by default")
	}
	// (?i) 前缀不区分大小写
	ci, err := Prepare(Config{
		RootDirs:    []string{root},
		FileNames:   []string{"web.config"},
		OldValue:    "(?i)abc",
		NewValue:    "xyz",
		RegexEnable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ci.matchText("hello ABC") {
		t.Error("(?i) prefix must make match case-insensitive")
	}
}

func TestPrepareRegexHardening(t *testing.T) {
	dir := t.TempDir()

	// RegexEnable=false：不预编译，字段保持 nil
	c, err := Prepare(Config{RootDirs: []string{dir}, FileNames: []string{"web.config"}, OldValue: "abc", NewValue: "abd"})
	if err != nil {
		t.Fatal(err)
	}
	if c.RegexEnable || len(c.nameRes) != 0 || c.oldRe != nil {
		t.Errorf("literal mode must not precompile; nameRes=%d oldRe=%v", len(c.nameRes), c.oldRe)
	}

	// 多个合法文件名正则：编译数量=2
	c, err = Prepare(Config{RootDirs: []string{dir}, FileNames: []string{"web.*", "app.*"}, OldValue: "abc", NewValue: "abd", RegexEnable: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.nameRes) != 2 {
		t.Errorf("expected 2 name regexes, got %d", len(c.nameRes))
	}

	// 幂等：对已预编译的 Config 再 Prepare，不重复追加
	c2, err := Prepare(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.nameRes) != 2 {
		t.Errorf("Prepare must be idempotent; got %d name regexes", len(c2.nameRes))
	}

	// Validate 错误先于编译暴露：目录不存在时报目录错误而非正则错误
	bad := Config{RootDirs: []string{filepath.Join(dir, "nope")}, FileNames: []string{"("}, OldValue: "abc", NewValue: "abd", RegexEnable: true}
	if _, err := Prepare(bad); err == nil || !strings.Contains(err.Error(), "目标目录不可访问") {
		t.Errorf("Validate error must surface before compile, got %v", err)
	}

	// 零宽正则被拒绝
	for _, pat := range []string{"(?i)", "a*", "a?"} {
		zw := Config{RootDirs: []string{dir}, FileNames: []string{"web.config"}, OldValue: pat, NewValue: "x", RegexEnable: true}
		if _, err := Prepare(zw); err == nil || !strings.Contains(err.Error(), "不能匹配空字符串") {
			t.Errorf("zero-width pattern %q must be rejected, got %v", pat, err)
		}
	}
}

func TestScanRegexLazyPrepare(t *testing.T) {
	// 未调用 Prepare 直接 Scan（正则模式）：守卫应自动编译，不 panic
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "web.config"), []byte("hello 123"), 0o666); err != nil {
		t.Fatal(err)
	}
	c := Config{RootDirs: []string{root}, FileNames: []string{"web.config"}, OldValue: `\d+`, NewValue: "X", RegexEnable: true}
	hits, _, err := Scan(c)
	if err != nil {
		t.Fatalf("lazy prepare should not error, got %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("expected 1 hit, got %d", len(hits))
	}
}

func TestRegexReplaceWithCapture(t *testing.T) {
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	content := "Host=192.168.1.10\r\nHost=10.0.0.5\r\nnothing"
	if err := os.WriteFile(cfgFile, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	c, err := Prepare(Config{
		RootDirs:    []string{root},
		FileNames:   []string{"web.config"},
		OldValue:    `Host=(\d+\.\d+\.\d+\.\d+)`,
		NewValue:    `Host=$1:8080`,
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
		RootDirs:    []string{root},
		FileNames:   []string{"web.config"},
		OldValue:    `value="\d+"`,
		NewValue:    `value="2026"`,
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
	if err := os.WriteFile(cfgFile, []byte("hello 123"), 0o666); err != nil {
		t.Fatal(err)
	}
	c, err := Prepare(Config{
		RootDirs:    []string{root},
		FileNames:   []string{"web.config"},
		OldValue:    `\d+`,
		NewValue:    "X",
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

func TestPrepareFailureLeavesNoResidue(t *testing.T) {
	dir := t.TempDir()
	// 文件名正则编译成功、但 OldValue 编译失败：返回的 Config 不应携带半成品正则
	bad := Config{RootDirs: []string{dir}, FileNames: []string{"web.config"}, OldValue: "(", NewValue: "x", RegexEnable: true}
	c, err := Prepare(bad)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(c.nameRes) != 0 || c.oldRe != nil {
		t.Errorf("failed Prepare must not leave residue; nameRes=%d oldRe=%v", len(c.nameRes), c.oldRe)
	}
}

func TestScanRegexLazyFailure(t *testing.T) {
	// 正则模式 + 非法正则，未 Prepare 直接 Scan：应返回编译错误而非 panic
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "web.config"), []byte("hello"), 0o666); err != nil {
		t.Fatal(err)
	}
	c := Config{RootDirs: []string{root}, FileNames: []string{"web.config"}, OldValue: "(", NewValue: "x", RegexEnable: true}
	if _, _, err := Scan(c); err == nil || !strings.Contains(err.Error(), "原字符串正则非法") {
		t.Errorf("expected old-regex compile error, got %v", err)
	}
}

func TestExecRegexLazyPrepare(t *testing.T) {
	// 未 Prepare 直接 ExecOne（正则模式）：守卫应自动编译，不 panic、正常执行
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	content := "hello 123"
	if err := os.WriteFile(cfgFile, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	c := Config{RootDirs: []string{root}, FileNames: []string{"web.config"}, OldValue: `\d+`, NewValue: "X", RegexEnable: true}
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
	if !res.Verified {
		t.Error("expected verified")
	}
	got, _ := os.ReadFile(cfgFile)
	if string(got) != "hello X" {
		t.Errorf("content mismatch: got %q", got)
	}
}

func TestExecVerifyUsesSameEncoding(t *testing.T) {
	// GBK 文件中，替换后剩余字节恰好构成合法 UTF-8：校验必须用执行时捕获的编码解码，
	// 否则 DetectEncoding 翻转为 UTF-8，导致误判内容不一致并回滚（应通过、不回滚）。
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	// 0xC2 0x80 同时是合法 UTF-8（U+0080）与有效 GBK 双字节；0x81 0x61 是 GBK 双字节但非合法 UTF-8。
	raw := []byte{0xC2, 0x80, 0x81, 0x61}
	if k := DetectEncoding(raw); k != EncGBK {
		t.Fatalf("precondition: expected EncGBK, got %v", k)
	}
	if err := os.WriteFile(cfgFile, raw, 0o666); err != nil {
		t.Fatal(err)
	}
	text, err := Decode(EncGBK, raw)
	if err != nil {
		t.Fatal(err)
	}
	runes := []rune(text)
	if len(runes) != 2 {
		t.Fatalf("precondition: expected 2 runes, got %d (%q)", len(runes), text)
	}
	// 把"非合法 UTF-8 的 GBK 双字节"解码出的第二个字符替换为 'X'，剩余字节变为合法 UTF-8。
	del := runes[1]
	c, err := Prepare(Config{
		RootDirs:   []string{root},
		FileNames:  []string{"web.config"},
		OldValue:   regexp.QuoteMeta(string(del)),
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
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	res := ExecOne(hits[0], c)
	if res.Err != nil {
		t.Fatalf("exec failed: %v", res.Err)
	}
	if res.RolledBack {
		t.Fatal("must NOT roll back when re-detected encoding differs from exec-time encoding")
	}
	if !res.Verified {
		t.Error("expected verified")
	}
	got, _ := os.ReadFile(cfgFile)
	want := []byte{0xC2, 0x80, 'X'}
	if !bytes.Equal(got, want) {
		t.Errorf("content mismatch: got %x, want %x", got, want)
	}
}

func TestExecRegexUTF16(t *testing.T) {
	root := t.TempDir()
	cfgFile := filepath.Join(root, "web.config")
	content := "Hello 123"
	e16 := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder()
	body, err := e16.Bytes([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	raw16 := append([]byte{0xFF, 0xFE}, body...)
	if err := os.WriteFile(cfgFile, raw16, 0o666); err != nil {
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
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	res := ExecOne(hits[0], c)
	if res.Err != nil {
		t.Fatalf("exec failed: %v", res.Err)
	}
	if res.RolledBack {
		t.Error("expected no rollback")
	}
	if !res.Verified {
		t.Error("expected verified")
	}
	got, _ := os.ReadFile(cfgFile)
	gotText, err := Decode(DetectEncoding(got), got)
	if err != nil {
		t.Fatal(err)
	}
	if gotText != "Hello X" {
		t.Errorf("content mismatch: %q", gotText)
	}
}
