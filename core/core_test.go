package core

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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
