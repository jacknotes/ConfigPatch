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
