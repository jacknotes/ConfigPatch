package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"configpatch/core"

	"github.com/lxn/walk"
	decl "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

// MainWin is the application's main window.
type MainWin struct {
	*walk.MainWindow

	dirsList  *walk.ListBox
	pathEdit  *walk.LineEdit
	namesEdit *walk.LineEdit
	oldEdit   *walk.LineEdit
	newEdit   *walk.LineEdit
	caseCk    *walk.CheckBox

	scanBtn *walk.PushButton
	execBtn *walk.PushButton
	stopBtn *walk.PushButton

	hitsList *walk.ListBox

	logEdit *walk.TextEdit
	status  *walk.StatusBarItem

	// runtime state
	dirs       []string // source of truth for the target directory list
	lastHits   []core.Hit
	busy       bool
	closed     bool
	cancelScan atomic.Bool // 停止标志：UI 线程写，工作 goroutine 读
}

func main() {
	runtime.LockOSThread()
	mw := &MainWin{}

	app := decl.MainWindow{
		AssignTo: &mw.MainWindow,
		Title:    "ConfigPatch — 文件字符串批量替换工具",
		MinSize:  decl.Size{Width: 820, Height: 640},
		Size:     decl.Size{Width: 880, Height: 700},
		Layout:   decl.VBox{MarginsZero: false, Spacing: 6},
		Children: []decl.Widget{
			decl.GroupBox{
				Title:  "目标目录（可添加多个，各自递归扫描，支持本地 / UNC 共享）",
				Layout: decl.VBox{Spacing: 6},
				Children: []decl.Widget{
					decl.Composite{
						Layout: decl.HBox{Spacing: 6},
						Children: []decl.Widget{
							decl.ListBox{
								AssignTo: &mw.dirsList,
								MinSize:  decl.Size{Width: 540, Height: 120},
							},
							decl.Composite{
								Layout: decl.VBox{Spacing: 4},
								Children: []decl.Widget{
									decl.PushButton{Text: "浏览...", OnClicked: mw.addDir},
									decl.PushButton{Text: "删除选中", OnClicked: mw.delDir},
									decl.PushButton{Text: "清空", OnClicked: mw.clearDirs},
								},
							},
						},
					},
					decl.Composite{
						Layout: decl.HBox{Spacing: 6},
						Children: []decl.Widget{
							decl.LineEdit{
								AssignTo:    &mw.pathEdit,
								ToolTipText: "直接输入本地或 UNC 共享路径（如 \\\\192.168.0.100\\d$\\WebSite），回车或点「添加路径」",
								OnKeyDown:   mw.onPathKeyDown,
							},
							decl.PushButton{Text: "添加路径", OnClicked: mw.addDirFromText},
						},
					},
				},
			},
			decl.GroupBox{
				Title:  "参数配置",
				Layout: decl.Grid{Columns: 2, Spacing: 6},
				Children: []decl.Widget{
					decl.Label{Text: "配置文件名称（多个用逗号分隔）"},
					decl.LineEdit{AssignTo: &mw.namesEdit, Text: "web.config"},
					decl.Label{Text: "原字符串（查找并替换，不区分大小写）"},
					decl.LineEdit{AssignTo: &mw.oldEdit},
					decl.Label{Text: "新字符串（替换为）"},
					decl.LineEdit{AssignTo: &mw.newEdit},
					decl.CheckBox{
						AssignTo:   &mw.caseCk,
						Text:       "允许仅大小写变更",
						ColumnSpan: 2,
					},
					decl.Label{
						Text:       "说明：勾选后，新值与原值仅大小写不同也会执行替换；不勾选则视为相同并中止。",
						ColumnSpan: 2,
					},
				},
			},
			decl.GroupBox{
				Title:  "操作",
				Layout: decl.HBox{Spacing: 6},
				Children: []decl.Widget{
					decl.PushButton{AssignTo: &mw.scanBtn, Text: "① 扫描预览", OnClicked: mw.onScan},
					decl.PushButton{AssignTo: &mw.execBtn, Text: "② 执行替换（需先扫描）", OnClicked: mw.onExec},
					decl.PushButton{AssignTo: &mw.stopBtn, Text: "③ 停止", OnClicked: mw.onStop, Enabled: false},
				},
			},
			decl.GroupBox{
				Title:  "命中文件（先「扫描预览」，确认无误后再「执行替换」）",
				Layout: decl.VBox{},
				Children: []decl.Widget{
					decl.ListBox{
						AssignTo: &mw.hitsList,
						MinSize:  decl.Size{Height: 120},
					},
				},
			},
			decl.GroupBox{
				Title:  "运行日志",
				Layout: decl.VBox{},
				Children: []decl.Widget{
					decl.TextEdit{
						AssignTo: &mw.logEdit,
						ReadOnly: true,
						VScroll:  true, // 显示垂直滚动条，日志超出时可滚动查看
						MinSize:  decl.Size{Height: 120},
					},
				},
			},
		},
		StatusBarItems: []decl.StatusBarItem{
			decl.StatusBarItem{AssignTo: &mw.status, Text: "就绪"},
		},
	}
	if err := app.Create(); err != nil {
		log.Fatal(err)
	}
	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		mw.closed = true
		mw.cancelScan.Store(true) // 关闭窗口同时停止后台扫描/替换
	})
	if code := mw.Run(); code != 0 {
		os.Exit(code)
	}
}

// ---------- directory list handling ----------

func (mw *MainWin) refreshDirs() {
	mw.dirsList.SetModel(mw.dirs)
}

func (mw *MainWin) addDir() {
	dlg := &walk.FileDialog{Title: "选择目标目录（本地目录或 UNC 共享）"}
	ok, err := dlg.ShowBrowseFolder(mw)
	if err != nil {
		mw.logf("打开目录选择失败: %v", err)
		return
	}
	if !ok {
		return
	}
	p := dlg.FilePath
	for _, it := range mw.dirs {
		if strings.EqualFold(it, p) {
			mw.logf("目录已在列表中: %s", p)
			return
		}
	}
	mw.dirs = append(mw.dirs, p)
	mw.refreshDirs()
	mw.logf("已添加目标目录: %s", p)
}

func (mw *MainWin) delDir() {
	idx := mw.dirsList.CurrentIndex()
	if idx < 0 {
		return
	}
	mw.dirs = append(mw.dirs[:idx], mw.dirs[idx+1:]...)
	mw.refreshDirs()
}

// onPathKeyDown adds the typed path when Enter is pressed in the path input.
func (mw *MainWin) onPathKeyDown(key walk.Key) {
	if key == walk.KeyReturn {
		mw.addDirFromText()
	}
}

// addDirFromText adds a directory typed by the user (local or UNC path).
// Unlike the browse dialog, this works for shares not mapped to a local drive.
func (mw *MainWin) addDirFromText() {
	p := strings.TrimSpace(mw.pathEdit.Text())
	if p == "" {
		mw.logf("请输入要添加的目录路径（本地目录或 UNC 共享）")
		return
	}
	// Normalize separators and trailing separators so duplicate detection is consistent.
	p = filepath.Clean(filepath.FromSlash(p))
	for _, it := range mw.dirs {
		if strings.EqualFold(it, p) {
			mw.logf("目录已在列表中: %s", p)
			mw.pathEdit.SetText("")
			return
		}
	}
	mw.dirs = append(mw.dirs, p)
	mw.refreshDirs()
	mw.pathEdit.SetText("")
	mw.logf("已添加目标目录: %s", p)
}

func (mw *MainWin) clearDirs() {
	mw.dirs = nil
	mw.refreshDirs()
}

// onStop 请求停止正在进行的扫描或替换。
func (mw *MainWin) onStop() {
	mw.cancelScan.Store(true)
	mw.logf("用户请求停止，正在中断…")
	mw.status.SetText("正在停止...")
}

// ---------- config building ----------

func (mw *MainWin) buildConfig() (core.Config, error) {
	var names []string
	for _, n := range strings.Split(mw.namesEdit.Text(), ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			names = append(names, n)
		}
	}
	cfg := core.Config{
		RootDirs:      mw.dirs,
		FileNames:     names,
		OldValue:      mw.oldEdit.Text(),
		NewValue:      mw.newEdit.Text(),
		CaseOnlyAllow: mw.caseCk.Checked(),
	}
	if err := core.Validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ---------- actions ----------

func (mw *MainWin) onScan() {
	if mw.busy {
		mw.logf("正在处理中，请稍候...")
		return
	}
	cfg, err := mw.buildConfig()
	if err != nil {
		mw.showConfigError(err)
		return
	}
	mw.cancelScan.Store(false)
	cfg.Cancel = func() bool { return mw.cancelScan.Load() }
	mw.setBusy(true)
	mw.logf("==== 开始扫描 ====")
	mw.logf("目标目录 %d 个，配置文件 %v，原值 %q", len(cfg.RootDirs), cfg.FileNames, cfg.OldValue)
	mw.hitsList.SetModel(nil)
	mw.lastHits = nil
	mw.status.SetText("扫描中...")
	go func() {
		hits, dirErrs, serr := core.Scan(cfg)
		mw.Synchronize(func() {
			defer mw.setBusy(false)
			if mw.closed {
				return
			}
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
				mw.logf("扫描出错: %v", serr)
				mw.status.SetText("扫描出错")
				return
			}
			for _, de := range dirErrs {
				mw.logf("  跳过不可访问项: %s（%v）", de.Path, de.Err)
			}
			mw.lastHits = hits
			paths := make([]string, 0, len(hits))
			for _, h := range hits {
				paths = append(paths, h.Path)
			}
			mw.hitsList.SetModel(paths)
			mw.logf("扫描完成：命中 %d 个配置文件", len(hits))
			mw.status.SetText(fmt.Sprintf("扫描完成，命中 %d 个", len(hits)))
			if len(hits) == 0 {
				walk.MsgBox(mw, "结果", "未找到任何包含原字符串的配置文件。", walk.MsgBoxIconInformation)
			}
		})
	}()
}

func (mw *MainWin) onExec() {
	if mw.busy {
		mw.logf("正在处理中，请稍候...")
		return
	}
	cfg, err := mw.buildConfig()
	if err != nil {
		mw.showConfigError(err)
		return
	}
	if len(mw.lastHits) == 0 {
		walk.MsgBox(mw, "提示", "请先点击「扫描预览」生成命中列表，确认后再执行替换。", walk.MsgBoxIconInformation)
		return
	}
	if r := walk.MsgBox(mw, "确认执行", fmt.Sprintf("将对 %d 个命中文件执行「备份 → 生成新文件 → 覆盖原文件」，是否继续？", len(mw.lastHits)), walk.MsgBoxYesNo|walk.MsgBoxIconQuestion); r != walk.DlgCmdYes {
		mw.logf("已取消执行")
		return
	}

	mw.cancelScan.Store(false)
	cfg.Cancel = func() bool { return mw.cancelScan.Load() }

	// open the run log file (best effort; failure is non-fatal)
	logf, lerr := openRunLog()
	if lerr != nil {
		mw.logf("警告：无法写入日志文件（%v），本次仅界面显示日志", lerr)
	}
	if logf != nil {
		// self-contained header: each log file records the inputs it was run with
		writeLogLine(logf, "==== 开始执行替换 ====")
		writeLogLine(logf, "目标目录: "+strings.Join(cfg.RootDirs, "; "))
		writeLogLine(logf, "配置文件: "+strings.Join(cfg.FileNames, ", "))
		writeLogLine(logf, fmt.Sprintf("原值: %q", cfg.OldValue))
		writeLogLine(logf, fmt.Sprintf("新值: %q", cfg.NewValue))
	}

	// route detailed per-step progress from core to both the UI and the file
	cfg.Logf = func(format string, args ...interface{}) {
		line := fmt.Sprintf(format, args...)
		mw.Synchronize(func() {
			if mw.closed {
				return
			}
			mw.logf("%s", line)
		})
		writeLogLine(logf, line)
	}

	mw.setBusy(true)
	mw.status.SetText("执行中...")
	start := time.Now()
	mw.logf("==== 开始执行替换（共 %d 个文件） ====", len(mw.lastHits))
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
}

// ---------- helpers ----------

func (mw *MainWin) showConfigError(err error) {
	if err == core.ErrSameValue {
		walk.MsgBox(mw, "警告", core.ErrSameValue.Error()+"，不做任何扫描和替换。", walk.MsgBoxIconWarning)
		return
	}
	walk.MsgBox(mw, "参数有误", err.Error(), walk.MsgBoxIconError)
}

func (mw *MainWin) setBusy(b bool) {
	mw.busy = b
	mw.scanBtn.SetEnabled(!b)
	mw.execBtn.SetEnabled(!b)
	mw.stopBtn.SetEnabled(b)
}

func (mw *MainWin) logf(format string, args ...interface{}) {
	line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
	mw.logEdit.AppendText(line + "\r\n")
	// 滚动到底部并强制整窗重绘：标准多行 EDIT 控件在程序化追加文本并滚动后，
	// 新暴露区域不会自动重绘，会表现为日志显示不全、鼠标选中后才出现，
	// 这里强制 Invalidate + UpdateWindow 立即重绘。
	mw.logEdit.SendMessage(win.WM_VSCROLL, uintptr(win.SB_BOTTOM), 0)
	win.InvalidateRect(mw.logEdit.Handle(), nil, false)
	win.UpdateWindow(mw.logEdit.Handle())
}

func formatResult(res core.ExecResult) string {
	if res.Err != nil {
		return fmt.Sprintf("✗ 失败: %s — %v", res.Path, res.Err)
	}
	if res.Skipped != "" {
		return fmt.Sprintf("— 跳过: %s — %s", res.Path, res.Skipped)
	}
	return fmt.Sprintf("✔ 成功: %s（替换 %d 处）\n    备份: %s\n    新文件: %s",
		res.Path, res.ReplacedCount, res.BackupPath, res.NewFilePath)
}

// openRunLog opens a fresh per-run log file under ./logs next to the
// executable. The name carries millisecond precision (run-YYYYMMDD-HHMMSS.mmm)
// and never collides with an existing file: a same-millisecond run gets _1, _2,
// ... appended instead of appending to the same file. Returns an error if the
// log directory cannot be created or the file cannot be opened.
func openRunLog() (*os.File, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logDir := filepath.Join(filepath.Dir(exe), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	ts := time.Now().Format("20060102-150405.000")
	for i := 0; ; i++ {
		name := filepath.Join(logDir, "run-"+ts+".log")
		if i > 0 {
			name = filepath.Join(logDir, fmt.Sprintf("run-%s_%d.log", ts, i))
		}
		f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o666)
		if err == nil {
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
}

// writeLogLine appends one timestamped line (full date/time with milliseconds)
// to the run log, so every entry is self-describing and chronologically
// ordered. A nil file is ignored (logging to disk unavailable).
func writeLogLine(f *os.File, line string) {
	if f == nil {
		return
	}
	fmt.Fprintf(f, "[%s] %s\r\n", time.Now().Format("2006-01-02 15:04:05.000"), line)
}
