package configstore

import (
	"os"
	"path/filepath"
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
