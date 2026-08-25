package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"configpatch/configstore"
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
	regexCk   *walk.CheckBox
	paramHint *walk.Label // 参数配置区说明文字（正则开/关时显示不同文案）

	scanBtn *walk.PushButton
	execBtn *walk.PushButton
	stopBtn *walk.PushButton

	saveProfileBtn  *walk.PushButton
	loadProfileBtn  *walk.PushButton
	delProfileBtn   *walk.PushButton
	rollbackBtn     *walk.PushButton
	profilesCombo   *walk.ComboBox
	profileNameEdit *walk.LineEdit

	hitsList *walk.ListBox

	logEdit *walk.TextEdit
	status  *walk.StatusBarItem

	// runtime state
	dirs        []string // source of truth for the target directory list
	lastHits    []core.Hit
	busy        bool
	closed      bool
	cancelScan  atomic.Bool // 停止标志：UI 线程写，工作 goroutine 读
	scanPartial bool        // 最近一次扫描是否被中止（仅部分结果）；仅 UI 线程读写

	store         *configstore.Store
	configPath    string
	autosaveTimer *time.Timer // 变更防抖自动保存计时器
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
				Title:  "配置方案（保存 / 加载 / 删除 / 回滚）",
				Layout: decl.HBox{Spacing: 6}, // 一行多列：方案下拉 + 新方案名 + 操作按钮全部横排
				Children: []decl.Widget{
					decl.Label{Text: "方案:"},
					decl.ComboBox{
						AssignTo: &mw.profilesCombo, // 非可编辑下拉框：可靠显示并选中方案
						// 固定宽度：下拉框理想宽度会随最长方案名自适应而无限变大
						//（方案名/文件名过长时会把整行甚至整个窗口撑爆），
						// 这里同时设置 Min/Max 把宽度锁定在一个合适值。
						MinSize: decl.Size{Width: 250},
						MaxSize: decl.Size{Width: 250},
					},
					decl.Label{Text: "新方案名:"},
					decl.LineEdit{
						AssignTo:    &mw.profileNameEdit,
						MinSize:     decl.Size{Width: 140},
						ToolTipText: "保存为方案时在此输入新方案名；留空则覆盖当前选中的方案",
					},
					decl.PushButton{AssignTo: &mw.saveProfileBtn, Text: "保存", OnClicked: mw.onSaveProfile},
					decl.PushButton{AssignTo: &mw.loadProfileBtn, Text: "加载", OnClicked: mw.onLoadProfile},
					decl.PushButton{AssignTo: &mw.delProfileBtn, Text: "删除", OnClicked: mw.onDeleteProfile},
					decl.PushButton{AssignTo: &mw.rollbackBtn, Text: "回滚", OnClicked: mw.onRollback},
				},
			},
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
				Layout: decl.Grid{Columns: 3, Spacing: 6},
				Children: []decl.Widget{
					decl.Label{Text: "配置文件名称", Row: 0, Column: 0},
					decl.LineEdit{
						AssignTo:      &mw.namesEdit,
						Text:          "web.config",
						Row:           0,
						Column:        1,
						OnTextChanged: mw.scheduleAutosave,
						ToolTipText:   "多个文件名用逗号分隔，如 web.config,app.config",
					},
					decl.Label{Text: "原字符串", Row: 1, Column: 0},
					decl.LineEdit{
						AssignTo:      &mw.oldEdit,
						Row:           1,
						Column:        1,
						OnTextChanged: mw.scheduleAutosave,
						ToolTipText:   "要查找并替换的字符串，不区分大小写",
					},
					decl.PushButton{
						Text:        "⇅",
						Row:         1,
						Column:      2,
						RowSpan:     2,                     // 纵向跨「原字符串 + 新字符串」两行
						Alignment:   decl.AlignHFarVCenter, // 按钮在两行之间垂直居中（Walk 原生按钮不可拉伸填充两行）
						OnClicked:   mw.onSwapOldNew,
						ToolTipText: "交换原字符串与新字符串",
					},
					decl.Label{Text: "新字符串", Row: 2, Column: 0},
					decl.LineEdit{
						AssignTo:      &mw.newEdit,
						Row:           2,
						Column:        1,
						OnTextChanged: mw.scheduleAutosave,
						ToolTipText:   "替换为的字符串",
					},
					// 单个复选框承载完整文字：文字天然连在一起（不再拆成复选框短文本 + 可点击 Label，
					// 避免两者之间出现空隙），点击框或文字均可切换。
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
						AssignTo:   &mw.paramHint,
						Text:       "说明：勾选后，新值与原值仅大小写不同也会执行替换；不勾选则视为相同并中止。",
						Row:        5,
						Column:     0,
						ColumnSpan: 3,
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
	mw.initConfig()
	// 目标目录 / 命中文件列表挂载右键复制菜单（右键先选中光标下的项，再弹「复制 / 复制全部」）。
	mw.attachListCopyMenu(mw.dirsList)
	mw.attachListCopyMenu(mw.hitsList)
	// 窗口布局完成后再应用一次下拉高度修复：declarative.Create 时窗口已 Show
	//（VisibleChanged 已触发过），布局要等消息循环开始后才完成，故用延时+重试。
	mw.applyComboHeightAfterLayout(0)
	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		mw.closed = true
		mw.cancelScan.Store(true) // 关闭窗口同时停止后台扫描/替换
		if mw.autosaveTimer != nil {
			mw.autosaveTimer.Stop() // 停掉未触发的防抖计时器，避免关闭瞬间重复写盘
		}
		mw.saveLast() // 兜底保存最后一次配置
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
	mw.scheduleAutosave()
	mw.logf("已添加目标目录: %s", p)
}

func (mw *MainWin) delDir() {
	idx := mw.dirsList.CurrentIndex()
	if idx < 0 {
		return
	}
	mw.dirs = append(mw.dirs[:idx], mw.dirs[idx+1:]...)
	mw.refreshDirs()
	mw.scheduleAutosave()
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
	mw.scheduleAutosave()
	mw.pathEdit.SetText("")
	mw.logf("已添加目标目录: %s", p)
}

func (mw *MainWin) clearDirs() {
	mw.dirs = nil
	mw.refreshDirs()
	mw.scheduleAutosave()
}

// onStop 请求停止正在进行的扫描或替换。
func (mw *MainWin) onStop() {
	mw.cancelScan.Store(true)
	mw.logf("用户请求停止，正在中断…")
	mw.status.SetText("正在停止...")
}

// ---------- 列表右键复制 ----------

// attachListCopyMenu 给列表控件挂载右键菜单：
// 1) 右键按下时先把光标下的项设为当前选中项（LB_ITEMFROMPOINT），
//    保证「复制」复制的是右键点击的那一项而非此前选中的项；
// 2) 菜单提供「复制」（选中项）与「复制全部」（所有项）两项。
func (mw *MainWin) attachListCopyMenu(lb *walk.ListBox) {
	lb.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.RightButton {
			return
		}
		// LB_ITEMFROMPOINT：LOWORD=光标处项索引，HIWORD=是否在客户区外。
		index := int(int32(lb.SendMessage(win.LB_ITEMFROMPOINT, 0, uintptr(win.MAKELONG(uint16(x), uint16(y))))))
		if model, ok := lb.Model().([]string); ok && index >= 0 && index < len(model) {
			lb.SetCurrentIndex(index)
		}
	})

	menu, err := walk.NewMenu()
	if err != nil {
		return
	}
	copyAct := walk.NewAction()
	if err := copyAct.SetText("复制"); err != nil {
		return
	}
	copyAct.Triggered().Attach(func() { mw.copyListSelected(lb) })
	menu.Actions().Add(copyAct)

	copyAllAct := walk.NewAction()
	if err := copyAllAct.SetText("复制全部"); err != nil {
		return
	}
	copyAllAct.Triggered().Attach(func() { mw.copyListAll(lb) })
	menu.Actions().Add(copyAllAct)

	lb.SetContextMenu(menu)
}

// copyListSelected 复制列表当前选中项到剪贴板。
func (mw *MainWin) copyListSelected(lb *walk.ListBox) {
	model, ok := lb.Model().([]string)
	if !ok {
		return
	}
	idx := lb.CurrentIndex()
	if idx < 0 || idx >= len(model) {
		mw.logf("没有可复制的选中项")
		return
	}
	if err := walk.Clipboard().SetText(model[idx]); err != nil {
		mw.logf("复制到剪贴板失败: %v", err)
		return
	}
	mw.logf("已复制: %s", model[idx])
}

// copyListAll 复制列表全部项到剪贴板（每项一行）。
func (mw *MainWin) copyListAll(lb *walk.ListBox) {
	model, ok := lb.Model().([]string)
	if !ok || len(model) == 0 {
		mw.logf("列表为空，无可复制内容")
		return
	}
	text := strings.Join(model, "\r\n")
	if err := walk.Clipboard().SetText(text); err != nil {
		mw.logf("复制到剪贴板失败: %v", err)
		return
	}
	mw.logf("已复制全部 %d 项", len(model))
}

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
	return configstore.Profile{
		RootDirs:      append([]string(nil), mw.dirs...), // 深拷贝，避免与界面目录列表共享底层数组
		FileNames:     parseFileNames(mw.namesEdit.Text()),
		OldValue:      mw.oldEdit.Text(),
		NewValue:      mw.newEdit.Text(),
		CaseOnlyAllow: mw.caseCk.Checked(),
		RegexEnable:   mw.regexCk.Checked(),
	}
}

// parseFileNames 把逗号分隔的文件名文本解析为去空白的列表。
func parseFileNames(text string) []string {
	var names []string
	for _, n := range strings.Split(text, ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			names = append(names, n)
		}
	}
	return names
}

// applyProfile 把配置填充回界面控件。
func (mw *MainWin) applyProfile(p configstore.Profile) {
	mw.dirs = append([]string(nil), p.RootDirs...)
	mw.refreshDirs()
	mw.namesEdit.SetText(strings.Join(p.FileNames, ","))
	mw.oldEdit.SetText(p.OldValue)
	mw.newEdit.SetText(p.NewValue)
	mw.regexCk.SetChecked(p.RegexEnable)
	mw.applyRegexUIState()
	mw.caseCk.SetChecked(p.CaseOnlyAllow && !p.RegexEnable)
}

// refreshProfilesCombo 用方案名（排序后）刷新下拉框；非可编辑框自动选中当前项或第一项，避免关闭态显示为空。
func (mw *MainWin) refreshProfilesCombo() {
	prev := mw.profilesCombo.Text()
	names := mw.profileNames()
	mw.profilesCombo.SetModel(names)
	if len(names) > 0 {
		idx := 0
		if i := slices.Index(names, prev); i >= 0 {
			idx = i
		}
		mw.profilesCombo.SetCurrentIndex(idx)
	}
	mw.rollbackBtn.SetEnabled(len(mw.store.History) > 0)
	mw.fixComboDropDownHeight(len(names)) // 修复下拉列表塌陷（见 fixComboDropDownHeight 注释）
}

// fixComboDropDownHeight 修复 Win32 下拉列表塌陷（约 2px，几乎不可见）的问题：
// CBS_DROPDOWNLIST 的下拉列表高度跟随组合框高度，但 Walk 布局会把组合框压成
// 一行（约 21px），导致点击下拉箭头时弹出框只有 2px 高、看不到任何方案（只能用
// 方向键切换）。这里把组合框临时拉高，让 Windows 为下拉列表预留足够高度以显示
// 全部方案；之后 Walk 重新布局会把组合框压回一行，但下拉高度会保留。
func (mw *MainWin) fixComboDropDownHeight(count int) {
	if mw.profilesCombo == nil {
		return
	}
	visible := count
	if visible > 10 {
		visible = 10 // 上限，避免方案过多时下拉过长
	}
	itemH := int(mw.profilesCombo.SendMessage(win.CB_GETITEMHEIGHT, 0, 0))
	if itemH <= 0 {
		itemH = 15
	}
	r := mw.profilesCombo.BoundsPixels()
	if r.Height <= 0 {
		return // 窗口尚未完成布局，由 applyComboHeightAfterLayout 在布局完成后应用
	}
	// 实测：下拉高度 ≈ 组合框高度 - 32，故需多预留一项高度（+1）才能显示全部方案
	win.MoveWindow(mw.profilesCombo.Handle(), int32(r.X), int32(r.Y), int32(r.Width), int32(r.Height+itemH*(visible+1)), true)
}

// applyComboHeightAfterLayout 在窗口完成首次布局后应用下拉高度修复。
// declarative.Create 时窗口已 Show（VisibleChanged 已触发过），而布局要等消息
// 循环开始后才完成，因此用延时 + 重试确保在组合框布局就绪后应用。
func (mw *MainWin) applyComboHeightAfterLayout(attempt int) {
	time.AfterFunc(150*time.Millisecond, func() {
		mw.Synchronize(func() {
			if mw.closed || mw.profilesCombo == nil {
				return
			}
			if mw.profilesCombo.BoundsPixels().Height <= 0 {
				if attempt < 20 {
					mw.applyComboHeightAfterLayout(attempt + 1) // 布局未完成，稍后重试
				}
				return
			}
			mw.refreshProfilesCombo()
		})
	})
}

// profileNames 返回排序后的方案名列表。
func (mw *MainWin) profileNames() []string {
	names := make([]string, 0, len(mw.store.Profiles))
	for n := range mw.store.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
	mw.rollbackBtn.SetEnabled(len(mw.store.History) > 0)
	mw.persist()
}

// persist 把 store 写盘；失败记日志并返回 false。
func (mw *MainWin) persist() bool {
	if err := configstore.Save(mw.configPath, mw.store); err != nil {
		mw.logf("保存配置失败：%v", err)
		return false
	}
	return true
}

func (mw *MainWin) onSaveProfile() {
	name := strings.TrimSpace(mw.profileNameEdit.Text())
	if name == "" {
		name = strings.TrimSpace(mw.profilesCombo.Text()) // 未填新名则覆盖当前选中的方案
	}
	if name == "" {
		walk.MsgBox(mw, "提示", "请先在「方案名」输入框填写方案名称。", walk.MsgBoxIconInformation)
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
	if !mw.persist() {
		delete(mw.store.Profiles, name) // 落盘失败则不保留未持久化的方案
		return
	}
	mw.refreshProfilesCombo()
	if i := slices.Index(mw.profileNames(), name); i >= 0 {
		mw.profilesCombo.SetCurrentIndex(i) // 选中刚保存的方案
	}
	mw.profileNameEdit.SetText("") // 清空方案名输入
	mw.logf("已保存方案「%s」", name)
}

func (mw *MainWin) onLoadProfile() {
	name := strings.TrimSpace(mw.profilesCombo.Text())
	p, ok := mw.store.Profiles[name]
	if !ok {
		walk.MsgBox(mw, "提示", "请选择要加载的方案。", walk.MsgBoxIconInformation)
		return
	}
	mw.store.PushHistory(mw.collectProfile()) // 当前状态入历史，可回滚
	mw.applyProfile(p)
	mw.persist()
	mw.refreshProfilesCombo()
	mw.logf("已加载方案「%s」", name)
}

func (mw *MainWin) onDeleteProfile() {
	name := strings.TrimSpace(mw.profilesCombo.Text())
	if _, ok := mw.store.Profiles[name]; !ok {
		return
	}
	if r := walk.MsgBox(mw, "确认", fmt.Sprintf("确定删除方案「%s」？", name), walk.MsgBoxYesNo|walk.MsgBoxIconQuestion); r != walk.DlgCmdYes {
		return
	}
	delete(mw.store.Profiles, name)
	mw.persist()
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
	mw.persist()
	mw.refreshProfilesCombo()
	mw.logf("已回滚到上一版配置")
}

// onSwapOldNew 交换原字符串与新字符串输入框的内容（交换后自动触发防抖保存）。
func (mw *MainWin) onSwapOldNew() {
	old := mw.oldEdit.Text()
	mw.oldEdit.SetText(mw.newEdit.Text())
	mw.newEdit.SetText(old)
	mw.logf("已交换原字符串与新字符串")
}

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
		mw.paramHint.SetText("说明：正则模式——文件名须匹配整个名称（不区分大小写）；原字符串为 RE2 正则，默认区分大小写，(?i) 前缀不区分；新字符串 $1/${name} 为捕获组引用，$$ 为字面 $；不支持 lookaround 与 \\1。")
		mw.namesEdit.SetToolTipText("多个正则用逗号分隔，须匹配整个文件名（自动加 ^...$），不区分大小写")
		mw.oldEdit.SetToolTipText("RE2 正则，默认区分大小写；(?i) 前缀表示不区分大小写")
		mw.newEdit.SetToolTipText("支持 $1/${name} 捕获组引用；$$ 表示字面 $")
	} else {
		mw.paramHint.SetText("说明：勾选后，新值与原值仅大小写不同也会执行替换；不勾选则视为相同并中止。")
		mw.namesEdit.SetToolTipText("多个文件名用逗号分隔，如 web.config,app.config")
		mw.oldEdit.SetToolTipText("要查找并替换的字符串，不区分大小写")
		mw.newEdit.SetToolTipText("替换为的字符串")
	}
}

// ---------- config building ----------

func (mw *MainWin) buildConfig() (core.Config, error) {
	cfg := core.Config{
		RootDirs:      mw.dirs,
		FileNames:     parseFileNames(mw.namesEdit.Text()),
		OldValue:      mw.oldEdit.Text(),
		NewValue:      mw.newEdit.Text(),
		CaseOnlyAllow: mw.caseCk.Checked(),
		RegexEnable:   mw.regexCk.Checked(),
	}
	return core.Prepare(cfg)
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
	mw.scanPartial = false
	mw.bindCancel(&cfg)
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
				mw.scanPartial = true
				mw.renderHits(hits, dirErrs)
				mw.logf("扫描已停止：命中 %d 个配置文件（未完成，仅部分结果）", len(hits))
				mw.status.SetText(fmt.Sprintf("扫描已停止，命中 %d 个（部分结果）", len(hits)))
				return
			}
			if serr != nil {
				mw.logf("扫描出错: %v", serr)
				mw.status.SetText("扫描出错")
				return
			}
			mw.renderHits(hits, dirErrs)
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
	confirmMsg := fmt.Sprintf("将对 %d 个命中文件执行「备份 → 生成新文件 → 覆盖原文件」，是否继续？", len(mw.lastHits))
	if mw.scanPartial {
		confirmMsg = "警告：上次扫描已被停止，命中列表可能不完整（仅部分结果）。\n\n" + confirmMsg
	}
	if r := walk.MsgBox(mw, "确认执行", confirmMsg, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion); r != walk.DlgCmdYes {
		mw.logf("已取消执行")
		return
	}

	mw.bindCancel(&cfg)

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
			if cfg.Cancel() {
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
		// 若停止请求恰好在最后一个文件处理期间到达，汇总仍如实标记为已中止
		stopped = stopped || mw.cancelScan.Load()
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
			title := "完成"
			if stopped {
				title = "已中止"
			}
			walk.MsgBox(mw, title, summary, walk.MsgBoxIconInformation)
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

// bindCancel 重置并绑定取消回调：操作开始前调用，置位停止标志即通过 cfg.Cancel 反馈给 core。
func (mw *MainWin) bindCancel(cfg *core.Config) {
	mw.cancelScan.Store(false)
	cfg.Cancel = func() bool { return mw.cancelScan.Load() }
}

// renderHits 将命中结果写入界面列表，并记录扫描中遇到的不可访问项。
func (mw *MainWin) renderHits(hits []core.Hit, dirErrs []core.ScanError) {
	mw.lastHits = hits
	paths := make([]string, 0, len(hits))
	for _, h := range hits {
		paths = append(paths, h.Path)
	}
	mw.hitsList.SetModel(paths)
	for _, de := range dirErrs {
		mw.logf("  跳过不可访问项: %s（%v）", de.Path, de.Err)
	}
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
