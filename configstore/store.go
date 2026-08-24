// Package configstore 提供配置方案的持久化：命名方案、上次使用状态与历史回滚。
package configstore

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// clone 返回深拷贝，避免与调用方共享切片底层数组。
func (p Profile) clone() Profile {
	c := p
	c.RootDirs = append([]string(nil), p.RootDirs...)
	c.FileNames = append([]string(nil), p.FileNames...)
	return c
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

// Save 原子写入配置：先写同目录临时文件、Sync 后重命名，避免写一半损坏。
func Save(path string, s *Store) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // 失败时清理残留临时文件（重命名成功后此处无文件，忽略错误）
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SaveLast 更新 last；若与旧 last 不同，把旧 last 压入历史队首（连续去重），并裁剪到 MaxHistory。
func (s *Store) SaveLast(p Profile) {
	if s.Last != nil && s.Last.Equal(p) {
		return
	}
	if s.Last != nil {
		s.push(*s.Last)
	}
	cp := p.clone()
	s.Last = &cp
}

// PushHistory 显式把当前状态压入历史（加载方案/回滚前调用，使操作可撤销）。
func (s *Store) PushHistory(p Profile) {
	s.push(p)
}

// Rollback 取出最近一个历史快照作为新的 last 返回；历史为空时返回 false。
// 返回的是独立副本，调用方修改返回值不会影响 store 内部状态。
func (s *Store) Rollback() (Profile, bool) {
	if len(s.History) == 0 {
		return Profile{}, false
	}
	p := s.History[0]
	s.History = s.History[1:]
	if s.Last != nil {
		s.push(*s.Last)
	}
	last := p.clone()
	s.Last = &last
	return p.clone(), true
}

// push 把 p 压入历史队首；与队首相同则跳过（连续去重），并裁剪到 MaxHistory。
func (s *Store) push(p Profile) {
	if len(s.History) > 0 && s.History[0].Equal(p) {
		return
	}
	s.History = append([]Profile{p.clone()}, s.History...)
	if len(s.History) > MaxHistory {
		s.History = s.History[:MaxHistory]
	}
}
