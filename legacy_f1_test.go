package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestF1LegacyTemplateUnchangedDoesNotBlockImport(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "targets.json")
	os.WriteFile(p, []byte(`[{"templateId":"ubits","name":"legacy-name","url":"https://ubits.club/attendance.php","warmupPath":"/favicon.ico","localStorage":[]}]`), 0600)
	tpl := NewTemplateStore(filepath.Join(dir, "missing.yml"))
	if err := tpl.Load(); err != nil {
		t.Fatal(err)
	}
	s := NewTargetStore(p, WithTemplates(tpl))
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	existing := s.Get()
	merged := append(existing, Target{URL: "https://example.test/", LocalStorage: []LocalStorage{}})
	if _, err := s.Save(merged); err != nil {
		t.Fatalf("unchanged legacy target blocks ordinary import: %v", err)
	}
	merged[0].URL = "https://elsewhere.test/"
	if _, err := s.Save(merged); err == nil {
		t.Fatal("legacy exception allowed tampering")
	}
}
func TestF1TargetStoreCopiesStorage(t *testing.T) {
	s := NewTargetStore(filepath.Join(t.TempDir(), "targets.json"))
	_, err := s.Save([]Target{{URL: "https://example.test/", LocalStorage: []LocalStorage{{Key: "k", Value: "v"}}}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	got[0].LocalStorage[0].Value = "changed"
	if s.Get()[0].LocalStorage[0].Value != "v" {
		t.Fatal("read caller mutated live storage")
	}
}
