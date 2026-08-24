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
	if len(s.History) > MaxHistory {
		t.Errorf("history capped at %d, got %d", MaxHistory, len(s.History))
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
