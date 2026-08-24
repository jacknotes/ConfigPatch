# ConfigPatch — 停止扫描 / 中止替换 — 设计文档

日期：2026-08-24
状态：已确认（方案 A：`Config` 增加 `Cancel` 回调字段）

## 目标

为"扫描预览"与"执行替换"提供手动停止/中止能力：

- 扫描进行中可停止，保留已扫到的部分命中，并明确提示"未完成，仅部分结果"；
- 替换进行中可中止，不再处理新文件；已替换完成的文件保留改动（每个文件都有 `backup-config` 备份可查）；
- 关闭窗口同时触发停止，避免后台扫描/替换继续运行（修复"关窗后替换仍在后台跑"的安全隐患）。

## 关键设计决策（经用户确认）

| 决策点 | 结论 |
|---|---|
| 实现方式 | 方案 A：`Config` 增加 `Cancel func() bool` 回调字段（与现有 `Logf` 同模式） |
| 停止粒度（扫描） | `filepath.WalkDir` 回调内检查，置位即中止遍历 |
| 停止粒度（替换） | 每个文件之间检查；`ExecOne` 入口兜底检查 |
| 停止后已替换文件 | 保留改动（每个文件均有 `backup-config` 备份） |
| 扫描停止后的命中 | 保留部分命中，明确提示"未完成，仅部分结果" |
| 关闭窗口 | 同时置位 `cancelScan`，真正停止后台扫描/替换 |
| 线程安全 | `sync/atomic.Bool`，UI 线程写、工作 goroutine 读 |

## 接口变更

### core 包

- `Config` 新增字段：

  ```go
  // Cancel 返回 true 表示用户请求停止当前操作；nil 表示不支持取消。
  Cancel func() bool
  ```

- 新增哨兵错误：

  ```go
  // ErrCancelled 表示操作被用户请求中止（扫描未完成 / 替换未全部执行）。
  var ErrCancelled = errors.New("操作已被用户中止")
  ```

### Scan

- `WalkDir` 回调内检查 `c.Cancel()`；为 true 时返回 `ErrCancelled` 中止遍历。
- 外层捕获 `ErrCancelled` 后：返回已收集的**部分 hits** + `ErrCancelled`，不当作致命错误，不算入目录错误列表。

### ExecOne

- 入口处检查 `c.Cancel()`；为 true 时直接返回 `Skipped="用户已请求中止"` 的结果（不读取、不触碰原文件，不创建备份目录）。

## UI 变更（main.go）

- "操作"分组框在 ① 扫描预览、② 执行替换旁新增 **"③ 停止"** 按钮 `stopBtn`，初始禁用。
- `MainWin` 新增字段 `cancelScan sync/atomic.Bool`。
- 停止按钮点击：`cancelScan.Store(true)`，日志输出"用户请求停止，正在中断…"；重复点击无副作用。
- `setBusy(b)` 统一控制三个按钮：
  - `b = true`（进入忙）：禁用 ① ②，启用 ③；
  - `b = false`（结束忙）：启用 ① ②，禁用 ③。
- 窗口 `Closing` 处理器：置位 `cancelScan.Store(true)`（关闭窗口即停止后台任务）。

## 处理流程

### 扫描（onScan）

```
用户点 ③ 停止
 └─ cancelScan.Store(true)
扫描 goroutine（WalkDir 回调内检查 Cancel）
 └─ 返回 ErrCancelled → 捕获
结果：
 ├─ 保留部分 hits
 ├─ 日志 "扫描已停止：命中 N 个（未完成，仅部分结果）"
 └─ 状态栏 "扫描已停止"
```

### 替换（onExec）

```
执行循环（for _, h := range lastHits）
 ├─ 每次进入前检查 cancelScan.Load() → true 则 break
 └─ 调用 ExecOne（其内部也兜底检查 Cancel）
结果：
 ├─ 已处理文件保留改动（有备份）
 ├─ 日志汇总 "已中止：成功 X / 跳过 Y / 失败 Z"
 └─ 状态栏显示已中止
```

## 测试覆盖（core）

- `TestScanStopsWhenCancelled`：注入 `Cancel`（遍历若干项后返回 true）→ `Scan` 返回部分命中 + `ErrCancelled`。
- `TestExecCancelled`：`Cancel`=true → `ExecOne` 返回"已中止"的跳过结果，原文件未被修改。
- 现有测试不受影响（`Cancel` 默认 nil）。

## 边界与说明

- 空闲时"③ 停止"禁用，点击无效果。
- 停止按钮可重复点击，第二次无副作用。
- 执行前确认对话框（"是否继续？"）流程不变，仍可点"否"取消；若上次扫描被停止（命中列表为部分结果），对话框会附加"命中列表可能不完整"的醒目警告，用户仍可选择继续（与"保留部分结果、可预览/执行"的决策一致）。
- 停止不自动回滚已替换文件（经用户确认：保留改动，依赖 `backup-config` 审计）。
- `ErrCancelled` 由 UI 层识别处理，不弹错误对话框（属于正常用户操作而非故障）。

## 文档同步

- README：功能特性与使用说明补充"③ 停止"。
- 本设计文档（写入 `docs/superpowers/specs/`）。
