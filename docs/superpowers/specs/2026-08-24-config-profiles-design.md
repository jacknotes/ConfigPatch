# ConfigPatch — 配置方案保存/加载 + 自动记忆上次配置 — 设计文档

日期：2026-08-24
状态：已确认

## 目标

解决"每次打开程序需重新填写配置"的痛点，提供：

- **命名方案（profiles）**：把当前界面配置（目标目录、文件名、原值、新值、仅大小写开关）保存为有名字的方案，可随时加载、覆盖、删除、切换；
- **自动记忆上次配置**：程序自动保存"上次使用"的界面状态，下次启动自动加载；
- **历史回滚**：自动保存不会破坏好配置——保留最近 5 个历史快照，可一键回滚到上一版；命名方案**永不被自动保存覆盖**，作为长期可靠配置。

## 关键设计决策（经用户确认）

| 决策点 | 结论 |
|---|---|
| 实现方式 | 方案 A：新增可测试的 `configstore` 纯逻辑包 + main.go 接线 |
| 存储形态 | 单个 `config.json`，内含多命名方案 + `last` + `history` |
| 存放位置 | exe 同目录（便携随 exe 走）；加入 `.gitignore` 不进仓库 |
| 自动保存时机 | 内容变更防抖（500ms）+ 关闭窗口兜底 |
| 自动保存范围 | 只写 `last`/`history`，**绝不覆盖 `profiles`** |
| 历史回滚 | 最近 5 个快照，可回滚上一版；历史本质是配置撤销栈 |
| 长期保障 | 命名方案由用户显式管理，永不被自动保存覆盖 |

## 数据模型（configstore 包）

### Profile（一个配置方案）

```go
type Profile struct {
	Name          string   `json:"name"`
	RootDirs      []string `json:"dirs,omitempty"`      // 目标目录列表
	FileNames     []string `json:"fileNames,omitempty"` // 配置文件名称列表
	OldValue      string   `json:"oldValue,omitempty"`
	NewValue      string   `json:"newValue,omitempty"`
	CaseOnlyAllow bool     `json:"caseOnlyAllow,omitempty"`
}
```

### Store（整个配置文件的运行时表示）

```go
type Store struct {
	Profiles map[string]Profile `json:"profiles"` // 方案名 -> 方案
	Last     *Profile           `json:"last,omitempty"`     // 上次使用状态
	History  []Profile          `json:"history,omitempty"`  // 最近 N 个历史快照（队首最新）
}
```

### 文件格式（config.json）

```json
{
  "profiles": { "方案A": { "dirs": [...], "fileNames": [...], "oldValue": "...", "newValue": "...", "caseOnlyAllow": true } },
  "last":    { ... },
  "history": [ { ... }, { ... }, ... ]
}
```

## configstore 包接口（纯逻辑、可测试）

- `Load(path string) (*Store, error)`：读取并解析；文件缺失或损坏时返回**默认空 Store**（不报致命错，错误信息供日志提示）。
- `Save(path string, s *Store) error`：**原子写**——先写同目录临时文件再 `os.Rename`，避免写一半损坏。
- `(s *Store) SaveLast(p Profile)`：更新 `last`；若与旧 `last` 不同，先把旧 `last` 压入 `history` 队首（与队首相同则去重），裁剪到上限（默认 5）。
- `(s *Store) Rollback() (Profile, bool)`：取出 `history[0]` 作为新的 `last` 返回；`history` 空返回 `false`。
- `(s *Store) PushHistory(p Profile)`：显式把当前状态压入历史（供"加载方案/回滚"前调用，使操作可撤销）。
- 方案增删改查：直接操作 `Profiles` map（`AddProfile` 覆盖同名、`DeleteProfile` 删除）。
- 常量 `MaxHistory = 5`。

## UI 变更（main.go）

### 新增"方案"区（置于窗口顶部或"参数配置"上方）

- **可编辑 ComboBox**（`Editable: true`）：列出所有方案名，也可输入新方案名。
- 按钮：**保存为方案**、**加载**、**删除**、**回滚上一版**。
  - 保存为方案：以 ComboBox 文本为方案名；同名存在则弹确认"覆盖？"；空名禁止。
  - 加载：读取 ComboBox 选中方案 → 填充界面；加载前先把当前界面状态压入历史。
  - 删除：确认后从 `Profiles` 删除（若 `last`/历史引用同名则仅作普通快照，不受影响）。
  - 回滚上一版：`history[0]` 恢复到界面并换位；历史空则按钮禁用。

### 界面状态 ↔ Profile 转换

- `collectProfile() configstore.Profile`：从界面采集当前状态（目录列表、文件名、原值、新值、仅大小写开关），不做校验。
- `applyProfile(p configstore.Profile)`：把方案/历史快照填充回各控件。

### 启动自动加载

- 窗口创建后：`Load(configPath)` → 若 `Last` 非空则 `applyConfig(*Last)`，并刷新方案 ComboBox 列表。

### 变更防抖自动保存

- 监听字段变更：`namesEdit`/`oldEdit`/`newEdit` 的 `OnTextChanged`、`caseCk` 的 `OnCheckedChanged`、目录列表变更（`addDir`/`delDir`/`clearDirs`/`addDirFromText` 后）。
- 任一变更 → 重置 500ms 计时器 → 触发时 `saveLast()`（采集当前界面 → `store.SaveLast(p)` → `Save(configPath)`）。
- 窗口 `Closing` 时：立即 `saveLast()` 兜底，避免计时器未触发丢失改动。

### config.json 路径

- `filepath.Join(filepath.Dir(os.Executable()), "config.json")`（与 `logs/` 同级、exe 同目录）。

## 行为与边界

- 自动保存只写 `last`/`history`，**绝不覆盖 `profiles`**——命名方案永不被自动保存破坏。
- 读写失败：`logf` 提示"配置保存/加载失败：<err>"，不影响程序使用。
- 缺失/损坏的 config.json：按空配置处理，程序正常启动。
- 历史上限 5（防文件膨胀）；长期可靠配置应存为命名方案（永不淘汰）。
- 回滚/加载前压入历史，使最近几步状态操作均可撤销。

## 测试覆盖（configstore）

- `TestLoadSaveRoundTrip`：`Store` 序列化往返一致（含方案、last、history）。
- `TestAddDeleteProfile`：方案增删、同名覆盖。
- `TestSaveLastHistory`：写 `last` 时旧值压栈、与旧值相同不压栈、去重。
- `TestHistoryCap`：超过上限 5 时淘汰最旧。
- `TestRollback`：回滚换位逻辑、历史空返回 false。
- `TestLoadMissingOrCorrupt`：缺失文件/损坏 JSON → 默认空 Store、不报致命错。

## 文档与仓库

- `.gitignore` 增加 `config.json`（含敏感串的本地配置不进仓库）。
- README：功能特性与使用说明补充"配置方案"与"自动记忆/回滚"。
- 本设计文档（写入 `docs/superpowers/specs/`）。

## 已知边界

- 配置含内网路径/连接串等敏感内容，仅存本地 `config.json`（已被 `.gitignore` 排除），不会随仓库推送。
- 自动保存的 `last` 只代表"上次使用的界面状态"，不代表"已保存的方案"；用户期望长期保留的配置应显式保存为方案。
