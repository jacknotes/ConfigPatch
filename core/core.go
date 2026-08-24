package core

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrSameValue is returned by Validate when the new value equals the old value
// under the configured comparison rule. The GUI shows a warning and aborts.
var ErrSameValue = errors.New("新值和原值一样，无需操作")

// ErrCancelled 表示操作被用户请求中止（扫描未完成 / 替换未全部执行）。
var ErrCancelled = errors.New("操作已被用户中止")

// Config carries the user's inputs for one scan/exec cycle.
type Config struct {
	RootDirs      []string // target roots; each is scanned recursively
	FileNames     []string // config file names, matched case-insensitively
	OldValue      string   // string to find (case-insensitive)
	NewValue      string   // string to replace with (exact text written)
	CaseOnlyAllow bool     // true: new==old compared exactly (allows case-only change); false: case-insensitive
	// Logf, when non-nil, receives detailed step-by-step progress during
	// ExecOne (处理 → 备份 → 生成新文件 → 覆盖 → 校验/回滚). It is optional
	// and must be safe to call from the goroutine that runs ExecOne.
	Logf func(format string, args ...interface{})
	// Cancel 返回 true 表示用户请求停止当前操作；nil 表示不支持取消。
	Cancel func() bool
}

// Validate checks inputs and returns a user-facing error if anything is wrong.
func Validate(c Config) error {
	if len(c.RootDirs) == 0 {
		return errors.New("请至少添加一个目标目录")
	}
	seen := make(map[string]bool, len(c.RootDirs))
	for _, d := range c.RootDirs {
		d = strings.TrimSpace(d)
		if d == "" {
			return errors.New("存在空白的目标目录，请检查")
		}
		if seen[d] {
			return fmt.Errorf("目标目录重复: %s", d)
		}
		seen[d] = true
		info, err := os.Stat(d)
		if err != nil {
			return fmt.Errorf("目标目录不可访问: %s（%v）", d, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("目标不是目录: %s", d)
		}
	}
	if len(c.FileNames) == 0 {
		return errors.New("请至少填写一个配置文件名称")
	}
	for _, f := range c.FileNames {
		if strings.TrimSpace(f) == "" {
			return errors.New("配置文件名称存在空白项，请检查")
		}
	}
	if strings.TrimSpace(c.OldValue) == "" {
		return errors.New("原字符串不能为空")
	}
	if strings.TrimSpace(c.NewValue) == "" {
		return errors.New("新字符串不能为空")
	}
	// "新值=原值" guard: default compares case-insensitively; with the
	// "允许仅大小写变更" switch on it compares exactly.
	if c.CaseOnlyAllow {
		if c.OldValue == c.NewValue {
			return ErrSameValue
		}
	} else {
		if strings.EqualFold(c.OldValue, c.NewValue) {
			return ErrSameValue
		}
	}
	return nil
}

// Hit describes one config file found to contain the old value.
type Hit struct {
	Root string // the target root it was found under
	Path string // full path of the config file
}

// ScanError describes a non-fatal problem encountered while walking.
type ScanError struct {
	Path string
	Err  error
}

// Scan walks each root recursively and returns config files that contain
// OldValue (case-insensitive). Directories named backup-config are skipped,
// and inaccessible entries are collected into dirErrors without aborting.
func Scan(c Config) (hits []Hit, dirErrors []ScanError, err error) {
	names := make(map[string]struct{}, len(c.FileNames))
	for _, n := range c.FileNames {
		names[strings.ToLower(strings.TrimSpace(n))] = struct{}{}
	}
	for _, root := range c.RootDirs {
		root = strings.TrimSpace(root)
		werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if c.Cancel != nil && c.Cancel() {
				return ErrCancelled
			}
			if walkErr != nil {
				dirErrors = append(dirErrors, ScanError{Path: path, Err: walkErr})
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if strings.EqualFold(d.Name(), "backup-config") {
					return filepath.SkipDir
				}
				return nil
			}
			if _, ok := names[strings.ToLower(d.Name())]; !ok {
				return nil
			}
			contains, rerr := FileContainsCI(path, c.OldValue)
			if rerr != nil {
				dirErrors = append(dirErrors, ScanError{Path: path, Err: rerr})
				return nil
			}
			if contains {
				hits = append(hits, Hit{Root: root, Path: path})
			}
			return nil
		})
		if werr != nil {
			return hits, dirErrors, werr
		}
	}
	return hits, dirErrors, nil
}

// ReadText reads a file, decodes it with its detected encoding and returns the text.
func ReadText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Decode(DetectEncoding(raw), raw)
}

// FileContainsCI reports whether the file (decoded with its own encoding)
// contains needle, ignoring case.
func FileContainsCI(path, needle string) (bool, error) {
	text, err := ReadText(path)
	if err != nil {
		return false, err
	}
	return ContainsCI(text, needle), nil
}

// ContainsCI reports whether s contains needle ignoring case.
func ContainsCI(s, needle string) bool {
	if needle == "" {
		return true
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(needle))
}

// ReplaceAllCI replaces every occurrence of old in s with new, ignoring case,
// and returns the new string plus the number of replacements made.
func ReplaceAllCI(s, old, new string) (string, int) {
	if old == "" {
		return s, 0
	}
	var b strings.Builder
	count := 0
	i := 0
	n := len(s)
	ol := len(old)
	for i <= n-ol {
		if strings.EqualFold(s[i:i+ol], old) {
			b.WriteString(new)
			count++
			i += ol
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	b.WriteString(s[i:])
	return b.String(), count
}

// ExecResult describes the outcome of processing one hit.
type ExecResult struct {
	Hit
	BackupPath    string // backup-config\<base><ext>-<ts> (original snapshot)
	NewFilePath   string // backup-config\<base>-new<ext> (modified copy)
	ReplacedCount int    // occurrences replaced
	Verified      bool   // post-replace verification passed
	RolledBack    bool   // verification failed and the original was restored
	Skipped       string // non-empty when the file was skipped at exec time
	Err           error  // non-nil on failure
}

// ExecOne processes a single hit:
//  1. re-check the file still contains OldValue (it may have changed since preview)
//  2. snapshot original bytes -> backup-config\<base><ext>-<ts> (never overwritten)
//  3. write the modified copy -> backup-config\<base>-new<ext> (original encoding)
//  4. copy the modified copy over the original
//  5. verify the original now contains NewValue; roll back from the snapshot on failure
func ExecOne(h Hit, c Config) ExecResult {
	res := ExecResult{Hit: h}
	// 用户已请求停止：不读取、不触碰原文件，也不创建备份目录。
	if c.Cancel != nil && c.Cancel() {
		res.Skipped = "用户已请求中止"
		return res
	}
	dir := filepath.Dir(h.Path)
	base, ext := splitExt(filepath.Base(h.Path))

	// step emits one line of step progress when a logger is configured; the
	// "处理" line is the per-file header and every step is indented beneath it.
	step := func(format string, args ...interface{}) {
		if c.Logf != nil {
			c.Logf(format, args...)
		}
	}
	if c.Logf != nil {
		c.Logf("处理: %s", h.Path)
	}

	backupDir := filepath.Join(dir, "backup-config")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		res.Err = fmt.Errorf("创建备份目录失败: %v", err)
		return res
	}

	// 0) re-check at exec time
	raw, err := os.ReadFile(h.Path)
	if err != nil {
		res.Err = fmt.Errorf("读取失败: %v", err)
		return res
	}
	enc := DetectEncoding(raw)
	text, derr := Decode(enc, raw)
	if derr != nil {
		res.Err = fmt.Errorf("解码失败: %v", derr)
		return res
	}
	if !ContainsCI(text, c.OldValue) {
		res.Skipped = "执行时已不再包含原字符串，已跳过"
		step("  - 跳过: %s", res.Skipped)
		return res
	}
	step("  - 确认仍包含原值")

	// 1) snapshot the original bytes (never overwrite an existing backup)
	ts := time.Now().Format("20060102150405")
	backupPath := uniquePath(filepath.Join(backupDir, base+ext+"-"+ts))
	if err := copyFile(h.Path, backupPath); err != nil {
		res.Err = fmt.Errorf("备份失败: %v", err)
		return res
	}
	res.BackupPath = backupPath
	step("  - 备份原文件 → %s", backupPath)

	// 2) build the modified copy with the ORIGINAL encoding
	newText, n := ReplaceAllCI(text, c.OldValue, c.NewValue)
	res.ReplacedCount = n
	newRaw, eerr := Encode(enc, newText)
	if eerr != nil {
		res.Err = fmt.Errorf("编码失败: %v", eerr)
		return res
	}
	newFilePath := filepath.Join(backupDir, base+"-new"+ext)
	if err := writeFile(newFilePath, newRaw); err != nil {
		res.Err = fmt.Errorf("写入 %s 失败: %v", filepath.Base(newFilePath), err)
		return res
	}
	res.NewFilePath = newFilePath
	step("  - 生成新文件 → %s", newFilePath)

	// 3) copy the modified copy over the original
	if err := overwriteFile(h.Path, newFilePath); err != nil {
		res.Err = fmt.Errorf("覆盖原文件失败: %v", err)
		return res
	}
	step("  - 覆盖原文件")

	// 4) verify the original now contains NewValue; roll back on failure
	verifyText, verr := ReadText(h.Path)
	if verr == nil && ContainsCI(verifyText, c.NewValue) {
		res.Verified = true
		step("  - 校验通过（替换 %d 处）", res.ReplacedCount)
		return res
	}
	step("  - 校验失败，尝试回滚")
	if rbErr := overwriteFile(h.Path, backupPath); rbErr != nil {
		res.Err = fmt.Errorf("替换后校验失败，且回滚失败: %v（请手动用备份恢复: %s）", verr, backupPath)
		return res
	}
	res.RolledBack = true
	step("  - 已回滚原文件")
	if verr != nil {
		res.Err = fmt.Errorf("替换后校验失败（读取错误: %v），已自动回滚", verr)
	} else {
		res.Err = errors.New("替换后校验失败（未找到新值），已自动回滚")
	}
	return res
}

// splitExt splits "web.config" into ("web", ".config").
func splitExt(name string) (base, ext string) {
	ext = filepath.Ext(name)
	base = strings.TrimSuffix(name, ext)
	return base, ext
}

// uniquePath returns a path that does not currently exist, appending _1, _2, ...
// so that historical backups are never overwritten.
func uniquePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	dir := filepath.Dir(p)
	base := filepath.Base(p)
	for i := 1; ; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s_%d", base, i))
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

// copyFile copies src to dst byte-for-byte.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writeFile writes data to path, retrying a few times and clearing the
// read-only attribute if the first attempt fails (covers transient IIS locks).
func writeFile(path string, data []byte) error {
	var err error
	for i := 0; i < 3; i++ {
		err = os.WriteFile(path, data, 0o666)
		if err == nil {
			return nil
		}
		if fi, statErr := os.Stat(path); statErr == nil && fi.Mode().Perm()&0o200 == 0 {
			_ = os.Chmod(path, 0o666)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return err
}

// overwriteFile copies the bytes of src over dst (used to replace the original
// and to roll back from a snapshot).
func overwriteFile(dst, src string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFile(dst, raw)
}
