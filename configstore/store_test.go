package configstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func p(name string) Profile {
	return Profile{Name: name, RootDirs: []string{`\\server\d$\WebSite`}, FileNames: []string{"web.config"}, OldValue: "http://hlog.example.com:9200", NewValue: "http://hlog.example.com:9201", CaseOnlyAllow: true}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := &Store{Profiles: map[string]Profile{"方案A": p("方案A")}}
	last := p("last")
	s.Last = &last
	s.History = []Profile{p("h1"), p("h2")}

	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 1 || !got.Profiles["方案A"].Equal(p("方案A")) {
		t.Errorf("profiles roundtrip mismatch: %+v", got.Profiles)
	}
	if got.Last == nil || !got.Last.Equal(p("last")) {
		t.Errorf("last roundtrip mismatch: %+v", got.Last)
	}
	if len(got.History) != 2 || !got.History[0].Equal(p("h1")) {
		t.Errorf("history roundtrip mismatch: %+v", got.History)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not be fatal: %v", err)
	}
	if len(s.Profiles) != 0 || s.Last != nil || len(s.History) != 0 {
		t.Errorf("expected empty store, got %+v", s)
	}
}

func TestLoadCorruptReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o666); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err == nil {
		t.Fatal("expected error for corrupt json")
	}
	if len(s.Profiles) != 0 {
		t.Errorf("expected empty store on corrupt, got %+v", s)
	}
	if s.Last != nil || len(s.History) != 0 {
		t.Errorf("expected empty last/history on corrupt, got last=%+v history=%v", s.Last, s.History)
	}
}

func TestSaveLastClonesInput(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	p := Profile{Name: "A", RootDirs: []string{"d1"}, FileNames: []string{"web.config"}, OldValue: "a", NewValue: "b"}
	s.SaveLast(p)
	p.RootDirs[0] = "MUTATED" // 调用方后续修改不得影响已存 last
	if s.Last == nil || s.Last.RootDirs[0] != "d1" {
		t.Errorf("SaveLast must not alias caller slice, got %+v", s.Last)
	}
}

func TestPushHistoryClonesInput(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	p := Profile{Name: "A", RootDirs: []string{"d1"}}
	s.PushHistory(p)
	p.RootDirs[0] = "MUTATED"
	if len(s.History) != 1 || s.History[0].RootDirs[0] != "d1" {
		t.Errorf("PushHistory must not alias caller slice, got %+v", s.History)
	}
}

func TestRollbackReturnsClone(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	s.PushHistory(Profile{Name: "A", RootDirs: []string{"d1"}})
	r, ok := s.Rollback()
	if !ok {
		t.Fatal("expected rollback available")
	}
	r.RootDirs[0] = "MUTATED" // 返回值修改不得影响 store 内 last
	if s.Last == nil || s.Last.RootDirs[0] != "d1" {
		t.Errorf("rollback return must not alias store last, got %+v", s.Last)
	}
}

func TestSaveLastHistory(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	s.SaveLast(p("A"))
	if s.Last == nil || !s.Last.Equal(p("A")) {
		t.Fatal("last not set")
	}
	s.SaveLast(p("A")) // 相同值：不压历史
	if len(s.History) != 0 {
		t.Errorf("identical last should not push history, got %d", len(s.History))
	}
	s.SaveLast(p("B")) // 不同值：旧 last 压入历史
	if len(s.History) != 1 || !s.History[0].Equal(p("A")) {
		t.Errorf("history should contain old last A, got %+v", s.History)
	}
	if !s.Last.Equal(p("B")) {
		t.Errorf("last should be B, got %+v", s.Last)
	}
}

func TestHistoryCap(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	for i := 0; i < MaxHistory+3; i++ {
		s.SaveLast(p(string(rune('A' + i))))
	}
	if len(s.History) != MaxHistory {
		t.Fatalf("history should be exactly %d, got %d", MaxHistory, len(s.History))
	}
	// 队首为最近、队尾为最旧
	if !s.History[0].Equal(p("G")) {
		t.Errorf("history front should be newest G, got %+v", s.History[0])
	}
	if !s.History[len(s.History)-1].Equal(p("C")) {
		t.Errorf("history tail should be oldest C, got %+v", s.History[len(s.History)-1])
	}
}

func TestProfileAddDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := &Store{Profiles: map[string]Profile{}}
	s.Profiles["方案A"] = p("方案A")
	s.Profiles["方案B"] = p("方案B")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(got.Profiles))
	}
	// 同名覆盖
	over := p("方案A")
	over.OldValue = "changed"
	got.Profiles["方案A"] = over
	if err := Save(path, got); err != nil {
		t.Fatal(err)
	}
	got2, _ := Load(path)
	if !got2.Profiles["方案A"].Equal(over) {
		t.Errorf("overwrite not persisted: %+v", got2.Profiles["方案A"])
	}
	// 删除
	delete(got2.Profiles, "方案A")
	if err := Save(path, got2); err != nil {
		t.Fatal(err)
	}
	got3, _ := Load(path)
	if _, ok := got3.Profiles["方案A"]; ok {
		t.Error("profile A should be deleted")
	}
	if len(got3.Profiles) != 1 {
		t.Errorf("expected 1 profile after delete, got %d", len(got3.Profiles))
	}
}

func TestPushHistoryDedupe(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	s.PushHistory(p("A"))
	s.PushHistory(p("A")) // 与队首相同：连续去重
	if len(s.History) != 1 {
		t.Errorf("expected 1 history entry after dedupe, got %d", len(s.History))
	}
}

func TestLoadNilProfilesNormalized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"last":{"name":"x"}}`), 0o666); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Profiles == nil {
		t.Error("Profiles should be non-nil after load")
	}
	if len(s.Profiles) != 0 {
		t.Errorf("expected empty profiles, got %d", len(s.Profiles))
	}
	if s.Last == nil || s.Last.Name != "x" {
		t.Errorf("last should be loaded, got %+v", s.Last)
	}
}

func TestRollback(t *testing.T) {
	s := &Store{Profiles: map[string]Profile{}}
	s.SaveLast(p("A"))
	s.SaveLast(p("B")) // history=[A], last=B
	r, ok := s.Rollback()
	if !ok {
		t.Fatal("expected rollback available")
	}
	if !r.Equal(p("A")) {
		t.Errorf("rollback should return A, got %+v", r)
	}
	if s.Last == nil || !s.Last.Equal(p("A")) {
		t.Errorf("last should be A after rollback, got %+v", s.Last)
	}
	r2, ok2 := s.Rollback() // 再次回滚应换回 B
	if !ok2 || !r2.Equal(p("B")) {
		t.Errorf("second rollback should return B, got %+v ok=%v", r2, ok2)
	}
	if _, ok := (&Store{Profiles: map[string]Profile{}}).Rollback(); ok {
		t.Error("expected no rollback on empty history")
	}
}

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
	// RegexEnable=true 经 Save→Load 后必须保留（防止 JSON tag 笔误静默丢失标志）
	sp := s.Profiles["A"]
	sp.RegexEnable = true
	s.Profiles["A"] = sp
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Profiles["A"].RegexEnable {
		t.Error("RegexEnable=true must survive Save/Load round trip")
	}
	// 仅靠往返不足以发现 tag 笔误：Save/Load 共享同一 struct，键名拼错仍对称往返。
	// 需直接断言序列化 JSON 中的键名，才能捕获 "regexEnable" 被拼错的静默丢失。
	raw2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw2), `"regexEnable": true`) {
		t.Errorf("saved JSON must contain %q key, got:\n%s", `"regexEnable": true`, raw2)
	}
}
