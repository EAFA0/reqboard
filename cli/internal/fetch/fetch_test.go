package fetch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedScriptValidAndDynamicOrigin(t *testing.T) {
	if len(larkFetchScript) == 0 {
		t.Fatal("embedded lark_fetch.py is empty")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "lark_fetch.py")
	if err := os.WriteFile(script, larkFetchScript, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("python3", script, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("python3 --help failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--output-dir") {
		t.Fatalf("unexpected help output:\n%s", out)
	}

	// Verify step2_clean derives BASE_ORIGIN dynamically from the input URL.
	orig := filepath.Join(dir, "demo-original.json")
	payload := `{"title":"Demo","markdown":"<mention-doc type=\"wiki\" token=\"tok123\">Linked</mention-doc>"}`
	if err := os.WriteFile(orig, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("python3", script, "https://custom.feishu.cn/wiki/demo", "--steps", "step2", "--output-dir", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("step2 failed: %v\n%s", err, out)
	}
	cleanBytes, err := os.ReadFile(filepath.Join(dir, "demo-clean.md"))
	if err != nil {
		t.Fatalf("missing clean md: %v", err)
	}
	if !strings.Contains(string(cleanBytes), "https://custom.feishu.cn/wiki/tok123") {
		t.Fatalf("expected dynamic BASE_ORIGIN link, got:\n%s", string(cleanBytes))
	}
}

func TestRunLarkRejectsBadURL(t *testing.T) {
	if _, err := RunLark("file:///etc/passwd", t.TempDir()); err == nil {
		t.Fatal("expected error for non-http URL")
	}
}
