package fetch

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed lark_fetch.py
var larkFetchScript []byte

// Artifact describes one generated output path from the embedded fetcher.
type Artifact struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size *int64 `json:"size"`
}

// Result is the structured result returned by RunLark.
type Result struct {
	OK           bool       `json:"ok"`
	URL          string     `json:"url"`
	OutputDir    string     `json:"output_dir"`
	Artifacts    []Artifact `json:"artifacts"`
	CleanMDPath  string     `json:"clean_md_path"`
	OriginalPath string     `json:"original_path,omitempty"`
	MediaDir     string     `json:"media_dir,omitempty"`
	WhiteboardDir string    `json:"whiteboard_dir,omitempty"`
	Stderr       string     `json:"-"`
}

// RunLark fetches a Feishu/Lark Wiki or Docx URL using the embedded fetcher
// (which wraps `lark-cli`) and writes clean Markdown plus local images and
// whiteboard assets into outputDir.
func RunLark(rawURL, outputDir string) (*Result, error) {
	url := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(strings.ToLower(url), "http://") && !strings.HasPrefix(strings.ToLower(url), "https://") {
		return nil, fmt.Errorf("invalid URL %q (expected http(s) Feishu/Lark wiki or docx link)", rawURL)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		return nil, fmt.Errorf("python3 not found on PATH (required for built-in Feishu/Lark fetcher)")
	}
	if _, err := exec.LookPath("lark-cli"); err != nil {
		return nil, fmt.Errorf("lark-cli not found on PATH (install lark-cli and run `lark-cli auth login` first)")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir %s: %w", outputDir, err)
	}

	tmp, err := os.CreateTemp("", "reqboard-lark-fetch-*.py")
	if err != nil {
		return nil, fmt.Errorf("create temp script: %w", err)
	}
	scriptPath := tmp.Name()
	defer os.Remove(scriptPath)
	if _, err := tmp.Write(larkFetchScript); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write temp script: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp script: %w", err)
	}

	cmd := exec.Command("python3", scriptPath, url, "--output-dir", outputDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return nil, fmt.Errorf("fetch failed: %v\n%s", err, msg)
		}
		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	var res Result
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		return nil, fmt.Errorf("parse fetcher output: %w (stdout=%q)", err, strings.TrimSpace(stdout.String()))
	}
	res.Stderr = strings.TrimSpace(stderr.String())
	for _, a := range res.Artifacts {
		switch {
		case strings.HasSuffix(a.Path, "-clean.md"):
			res.CleanMDPath = a.Path
		case strings.HasSuffix(a.Path, "-original.json"):
			res.OriginalPath = a.Path
		case strings.HasSuffix(a.Path, "-media") && a.Type == "directory":
			res.MediaDir = a.Path
		case strings.HasSuffix(a.Path, "-whiteboards") && a.Type == "directory":
			res.WhiteboardDir = a.Path
		}
	}
	if res.CleanMDPath == "" {
		return nil, fmt.Errorf("fetcher did not produce a -clean.md file in %s", outputDir)
	}
	return &res, nil
}

// CleanupIntermediate removes the raw -original.json and temporary -clean.md
// after ReqBoard has imported the Markdown into prd.md or sources/<id>.md,
// while preserving downloaded media and whiteboard directories.
func (r *Result) CleanupIntermediate() {
	if r == nil {
		return
	}
	if r.OriginalPath != "" {
		_ = os.Remove(r.OriginalPath)
	}
	if r.CleanMDPath != "" {
		_ = os.Remove(r.CleanMDPath)
	}
}

// MediaSummary returns a short human-readable summary of downloaded assets.
func (r *Result) MediaSummary() string {
	if r == nil {
		return ""
	}
	var parts []string
	if r.MediaDir != "" {
		if entries, err := os.ReadDir(r.MediaDir); err == nil {
			imgCount := 0
			for _, e := range entries {
				if !e.IsDir() && e.Name() != "index.json" {
					imgCount++
				}
			}
			if imgCount > 0 {
				parts = append(parts, fmt.Sprintf("图片 %d 张 (%s)", imgCount, filepath.Base(r.MediaDir)))
			}
		}
	}
	if r.WhiteboardDir != "" {
		if entries, err := os.ReadDir(r.WhiteboardDir); err == nil {
			wbCount := 0
			for _, e := range entries {
				if !e.IsDir() && strings.Contains(e.Name(), "-preview.") {
					wbCount++
				}
			}
			if wbCount > 0 {
				parts = append(parts, fmt.Sprintf("画板 %d 个 (%s)", wbCount, filepath.Base(r.WhiteboardDir)))
			}
		}
	}
	return strings.Join(parts, "，")
}
