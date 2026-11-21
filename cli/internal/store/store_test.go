package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"reqboard/internal/model"
)

func TestInitAndLoad(t *testing.T) {
	root := t.TempDir()
	s, err := Init(root, "req", []model.Repo{{Name: "api", Path: "api", BaseRef: "master"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"config.json", "prd.md", "dag.json", "bindings.json", "events.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, DirName, f)); err != nil {
			t.Fatalf("%s missing: %v", f, err)
		}
	}
	cfg, err := s.LoadConfig()
	if err != nil || cfg.RequirementName != "req" || len(cfg.IgnoreGlobs) == 0 {
		t.Fatalf("config = %+v err=%v", cfg, err)
	}
	if _, err := Init(root, "req", nil); err == nil {
		t.Fatal("second init should fail")
	}
	_ = os.MkdirAll(filepath.Join(root, "api", "deep"), 0o755)
	found, err := Find(filepath.Join(root, "api", "deep"))
	if err != nil || found != root {
		t.Fatalf("Find = %s, %v", found, err)
	}
}

func TestAtomicWriteAndEvents(t *testing.T) {
	root := t.TempDir()
	s, err := Init(root, "req", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := model.NewDAG("req")
	d.Revision = 7
	if err := s.SaveDAG(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadDAG()
	if err != nil || got.Revision != 7 {
		t.Fatalf("dag = %+v err=%v", got, err)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(s.Dir())
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || (len(e.Name()) > 0 && e.Name()[0] == '.' && e.Name() != ".lock") {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
	for i := 0; i < 5; i++ {
		if err := s.AppendEvent(Event{Actor: "agent", Action: "test", Detail: fmt.Sprintf("e%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.Events(3)
	if err != nil || len(evs) != 3 || evs[2].Detail != "e4" {
		t.Fatalf("events = %+v err=%v", evs, err)
	}
}

func TestWithLockSerializesWriters(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "req", nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(root)
			if err != nil {
				errs <- err
				return
			}
			err = s.WithLock(func() error {
				d, err := s.LoadDAG()
				if err != nil {
					return err
				}
				d.Revision++
				return s.SaveDAG(d)
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	s, _ := Open(root)
	d, _ := s.LoadDAG()
	if d.Revision != 21 { // initial 1 + 20 increments
		t.Fatalf("revision = %d, want 21 (flock failed)", d.Revision)
	}
}
