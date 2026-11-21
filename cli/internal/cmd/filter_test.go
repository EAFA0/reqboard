package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"reqboard/internal/model"
)

func filterFromJSON(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	m := jsonMap(t, out)
	f, ok := m["filter"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing filter in %s", out)
	}
	return f
}

func TestFilterShowDefaults(t *testing.T) {
	f := setupCLI(t)
	filter := filterFromJSON(t, mustCLI(t, f.root, "filter", "show", "--json"))
	if filter["exclude_tests"] != true {
		t.Fatalf("exclude_tests = %v", filter["exclude_tests"])
	}
	globs, _ := filter["ignore_globs"].([]interface{})
	if len(globs) != len(model.DefaultIgnoreGlobs) {
		t.Fatalf("ignore_globs = %v", globs)
	}
	if include, _ := filter["include_globs"].([]interface{}); len(include) != 0 {
		t.Fatalf("include_globs = %v", include)
	}
}

func TestFilterSetPersistsAndLogs(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "filter", "set",
		"--include", "svc/**, api/*.thrift",
		"--exclude", "vendor/**, **/*.pb.go",
		"--exclude-tests=false", "--json")
	filter := filterFromJSON(t, out)
	if filter["exclude_tests"] != false {
		t.Fatalf("exclude_tests = %v", filter["exclude_tests"])
	}
	include, _ := filter["include_globs"].([]interface{})
	if len(include) != 2 || include[0] != "svc/**" || include[1] != "api/*.thrift" {
		t.Fatalf("include_globs = %v", include)
	}
	exclude, _ := filter["ignore_globs"].([]interface{})
	if len(exclude) != 2 || exclude[0] != "vendor/**" {
		t.Fatalf("ignore_globs = %v", exclude)
	}

	cfg, err := f.store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShouldExcludeTests() {
		t.Fatal("config exclude_tests not persisted")
	}
	if len(cfg.IncludeGlobs) != 2 {
		t.Fatalf("config include_globs = %v", cfg.IncludeGlobs)
	}
	if len(cfg.IgnoreGlobs) != 2 || cfg.IgnoreGlobs[0] != "vendor/**" {
		t.Fatalf("config ignore_globs = %v", cfg.IgnoreGlobs)
	}

	events, err := f.store.Events(0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Action == "filter.update" && strings.Contains(ev.Detail, "exclude_tests=false") {
			found = true
		}
	}
	if !found {
		t.Fatalf("filter.update event missing: %+v", events)
	}
}

func TestFilterResetRestoresDefaults(t *testing.T) {
	f := setupCLI(t)
	mustCLI(t, f.root, "filter", "set", "--include", "svc/**", "--exclude", "vendor/**", "--exclude-tests=false")
	filter := filterFromJSON(t, mustCLI(t, f.root, "filter", "set", "--reset", "--json"))
	if filter["exclude_tests"] != true {
		t.Fatalf("exclude_tests after reset = %v", filter["exclude_tests"])
	}
	if include, _ := filter["include_globs"].([]interface{}); len(include) != 0 {
		t.Fatalf("include_globs after reset = %v", include)
	}
	globs, _ := filter["ignore_globs"].([]interface{})
	if len(globs) != len(model.DefaultIgnoreGlobs) {
		t.Fatalf("ignore_globs after reset = %v", globs)
	}
	cfg, err := f.store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExcludeTests != nil {
		t.Fatalf("exclude_tests should be unset after reset: %v", *cfg.ExcludeTests)
	}
}

func TestStatusAndCheckEmitFilter(t *testing.T) {
	f := setupCLI(t)
	mustCLI(t, f.root, "filter", "set", "--exclude-tests=false", "--include", "svc/**")

	for _, cmd := range []string{"status", "check"} {
		out := mustCLI(t, f.root, cmd, "--json")
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("%s json: %v\n%s", cmd, err, out)
		}
		filter, ok := payload["filter"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s missing filter: %s", cmd, out)
		}
		if filter["exclude_tests"] != false {
			t.Fatalf("%s exclude_tests = %v", cmd, filter["exclude_tests"])
		}
		include, _ := filter["include_globs"].([]interface{})
		if len(include) != 1 || include[0] != "svc/**" {
			t.Fatalf("%s include_globs = %v", cmd, include)
		}
	}
}

func TestFilterSetRequiresAChange(t *testing.T) {
	f := setupCLI(t)
	_, _, code := cli(t, f.root, "filter", "set")
	if code == 0 {
		t.Fatal("filter set without flags should fail")
	}
	if _, err := os.Stat(f.root + "/.reqboard/config.json"); err != nil {
		t.Fatal(err)
	}
}
