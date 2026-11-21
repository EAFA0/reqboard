package cmd

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"reqboard/internal/model"
	"reqboard/internal/prd"
)

var sourceIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func validSourceID(id string) bool {
	return sourceIDRe.MatchString(id) && !strings.EqualFold(id, prd.DefaultPrefix)
}

func sourceFileName(id string) string { return "sources/" + id + ".md" }

func sourceKind(s *model.Source) string {
	if s == nil || s.Kind == "" {
		return model.SourceDoc
	}
	return s.Kind
}

func sourceFile(s *model.Source) string {
	if s != nil && strings.TrimSpace(s.File) != "" {
		return s.File
	}
	if s == nil {
		return ""
	}
	return sourceFileName(s.ID)
}

func runSource(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return &Error{Code: 2, Msg: "usage: reqboard source add|sync|list|remove ...", Text: sourceUsage()}
	}
	switch pos[0] {
	case "add":
		return sourceAdd(a, pos[1:])
	case "sync":
		if len(pos) < 3 {
			return fail("usage: reqboard source sync <id> <new_file.md>")
		}
		return sourceSync(a, pos[1], pos[2])
	case "list", "ls":
		return sourceList(a)
	case "remove", "rm":
		if len(pos) < 2 {
			return fail("usage: reqboard source remove <id> [--force]")
		}
		return sourceRemove(a, pos[1])
	}
	return &Error{Code: 2, Msg: "unknown source subcommand " + pos[0], Text: sourceUsage()}
}

func sourceUsage() string {
	return `用法:
  reqboard source add <file.md|-> --id <slug> [--title "..."] [--kind doc|meeting|chat|spec] [--url "https://..."]
  reqboard source add --text "粘贴的会议纪要/群聊拍板" --id <slug> [--title "..."] [--kind ...]
  reqboard source sync <id> <new_file.md>
  reqboard source list [--json]
  reqboard source remove <id> [--force]
`
}

func readSourceInput(a *Args, pos []string) ([]byte, *Error) {
	if a.Has("text") {
		return []byte(a.Str("text")), nil
	}
	file := strings.TrimSpace(a.Str("file"))
	if file == "" && len(pos) > 0 {
		file = pos[0]
	}
	if file == "" {
		return nil, &Error{Code: 2, Msg: "missing source content", Text: sourceUsage()}
	}
	if file == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fail("read stdin: %v", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fail("read %s: %v", file, err)
	}
	return data, nil
}

func sourceAdd(a *Args, pos []string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	id := strings.TrimSpace(a.Str("id"))
	if id == "" {
		return fail("--id is required (slug, e.g. meeting-review)")
	}
	if !validSourceID(id) {
		return fail("invalid --id %q (want [A-Za-z0-9][A-Za-z0-9_-]*, and not reserved %q)", id, prd.DefaultPrefix)
	}
	kind := a.Str("kind")
	if kind == "" {
		kind = model.SourceDoc
	}
	if !model.ValidSourceKind(kind) {
		return fail("invalid --kind %q (doc|meeting|chat|spec)", kind)
	}
	url := strings.TrimSpace(a.Str("url"))
	if url != "" && !isHTTPURL(url) {
		return fail("invalid --url %q (only http(s) links are accepted)", url)
	}
	raw, re := readSourceInput(a, pos)
	if re != nil {
		return re
	}
	if strings.TrimSpace(string(raw)) == "" {
		return fail("source content is empty")
	}
	doc, perr := prd.InitWithPrefix(raw, id)
	if perr != nil {
		return fail("parse source: %v", perr)
	}
	title := strings.TrimSpace(a.Str("title"))
	if title == "" {
		for _, b := range doc.Blocks {
			if b.Kind == prd.KindHeading {
				title = strings.TrimSpace(strings.TrimLeft(b.Text, "#"))
				break
			}
		}
	}
	if title == "" {
		title = id
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		if dag.Source(id) != nil {
			return fail("source %q already exists (use `reqboard source sync %s <new.md>` to update it)", id, id)
		}
		file := sourceFileName(id)
		if _, statErr := ctx.Store.ReadFile(file); statErr == nil && !a.Bool("force") {
			return fail("%s already exists; pass --force to overwrite and register it", file)
		}
		if err := ctx.Store.WriteFile(file, doc.Render()); err != nil {
			return fail("write %s: %v", file, err)
		}
		dag.Sources = append(dag.Sources, model.Source{
			ID: id, Title: title, Kind: kind, File: file, URL: url, AddedAt: model.Now(),
		})
		if err := ctx.SaveDAG(dag, "source.add", "", fmt.Sprintf("%s (%s, %d blocks)", id, kind, len(doc.Blocks))); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD SOURCE ADD] %s  %s\n", id, title))
		sb.WriteString(fmt.Sprintf("  kind=%s  file=.reqboard/%s  blocks=%d\n", kind, file, len(doc.Blocks)))
		if url != "" {
			sb.WriteString("  url: " + url + "\n")
		}
		sb.WriteString("  锚点示例:\n")
		for i, b := range doc.Blocks {
			if i >= 5 {
				break
			}
			sb.WriteString(fmt.Sprintf("    <!-- @%s --> %s\n", b.Anchor, prd.Summary(b)))
		}
		sb.WriteString("  下一步: 在看板补充来源 Tab 点「+ 建节点」或「+ 挂到当前节点」，或 `reqboard node update <id> --prd-ref <" + id + ":...>`。\n")
		ctx.Emit(sb.String(), map[string]interface{}{
			"ok": true, "source": dag.Source(id), "blocks": len(doc.Blocks), "anchors": doc.Anchors(),
		})
		return nil
	})
}

func sourceSync(a *Args, id, newPath string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	newRaw, rerr := os.ReadFile(newPath)
	if rerr != nil {
		return fail("read %s: %v", newPath, rerr)
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		src := dag.Source(id)
		if src == nil {
			return fail("source %q not found (run `reqboard source list`)", id)
		}
		file := sourceFile(src)
		oldRaw, rerr := ctx.Store.ReadFile(file)
		if rerr != nil {
			return fail("read .reqboard/%s: %v", file, rerr)
		}
		oldDoc, perr := prd.Parse(oldRaw)
		if perr != nil {
			return fail("parse existing %s: %v", file, perr)
		}
		res, serr := prd.SyncWithPrefix(oldDoc, newRaw, id)
		if serr != nil {
			return fail("sync source: %v", serr)
		}
		reasonByAnchor := map[string]string{}
		for _, p := range res.Modified {
			reasonByAnchor[p.Old.Anchor] = fmt.Sprintf("补充来源 %s 段落 %s 已修改: %s", id, p.Old.Anchor, prd.Summary(p.New))
		}
		for _, b := range res.Deleted {
			reasonByAnchor[b.Anchor] = fmt.Sprintf("补充来源 %s 段落 %s 已删除，请确认关联节点是否需调整", id, b.Anchor)
		}
		affected := cascadeStale(dag, reasonByAnchor)

		if err := ctx.Store.WriteFile(file, res.Document.Render()); err != nil {
			return fail("write %s: %v", file, err)
		}
		if s := dag.Source(id); s != nil {
			s.UpdatedAt = model.Now()
		}
		if err := ctx.SaveDAG(dag, "source.sync", "", fmt.Sprintf("%s modified=%d added=%d deleted=%d stale=%d", id, len(res.Modified), len(res.Added), len(res.Deleted), len(affected))); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD SOURCE SYNC] %s <- %s\n", id, newPath))
		sb.WriteString(fmt.Sprintf("  未变 %d 段，修改 %d 段，新增 %d 段，删除 %d 段\n", res.Unchanged, len(res.Modified), len(res.Added), len(res.Deleted)))
		if len(res.Modified) > 0 {
			sb.WriteString("\n  修改段落（关联节点及下游已级联 stale）:\n")
			for _, p := range res.Modified {
				sb.WriteString(fmt.Sprintf("    - %s  %s\n", p.New.Anchor, prd.Summary(p.New)))
			}
		}
		if len(res.Added) > 0 {
			sb.WriteString("\n  新增段落（可挂到节点或忽略）:\n")
			for _, b := range res.Added {
				if b.Kind == prd.KindHeading {
					continue
				}
				sb.WriteString(fmt.Sprintf("    - %s  %s\n", b.Anchor, prd.Summary(b)))
			}
		}
		if len(affected) > 0 {
			sb.WriteString("\n  已标 stale 节点:\n")
			for _, nid := range sortedKeys(affected) {
				sb.WriteString(fmt.Sprintf("    - %s: %s\n", nid, affected[nid]))
			}
		}
		modified := []map[string]interface{}{}
		for _, p := range res.Modified {
			modified = append(modified, map[string]interface{}{"anchor": p.New.Anchor, "old_text": p.Old.Text, "new_text": p.New.Text})
		}
		added := []map[string]interface{}{}
		for _, b := range res.Added {
			added = append(added, map[string]interface{}{"anchor": b.Anchor, "text": b.Text, "kind": b.Kind})
		}
		deleted := []map[string]interface{}{}
		for _, b := range res.Deleted {
			deleted = append(deleted, map[string]interface{}{"anchor": b.Anchor, "text": b.Text, "kind": b.Kind})
		}
		ctx.Emit(sb.String(), map[string]interface{}{
			"ok": true, "source": dag.Source(id), "unchanged": res.Unchanged,
			"modified": modified, "added": added, "deleted": deleted, "stale_nodes": affected,
		})
		return nil
	})
}

func sourceList(a *Args) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	dag, derr := ctx.Store.LoadDAG()
	if derr != nil {
		return fail("%v", derr)
	}
	referenced := map[string]bool{}
	for _, n := range dag.Nodes {
		for _, r := range n.PRDRefs {
			referenced[r] = true
		}
	}
	type sourceView struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		Kind             string `json:"kind"`
		File             string `json:"file"`
		URL              string `json:"url,omitempty"`
		Blocks           int    `json:"blocks"`
		ReferencedBlocks int    `json:"referenced_blocks"`
		AddedAt          string `json:"added_at"`
		UpdatedAt        string `json:"updated_at,omitempty"`
	}
	views := []sourceView{}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[REQBOARD SOURCES] %d 个补充来源\n", len(dag.Sources)))
	if len(dag.Sources) == 0 {
		sb.WriteString("  （无）用 `reqboard source add <file.md> --id <slug>` 添加会议纪要/群聊拍板。\n")
	}
	sorted := append([]model.Source{}, dag.Sources...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, s := range sorted {
		blocks, refN := 0, 0
		if raw, err := ctx.Store.ReadFile(sourceFile(&s)); err == nil {
			if doc, err := prd.Parse(raw); err == nil {
				blocks = len(doc.Blocks)
				for _, b := range doc.Blocks {
					if b.Kind != prd.KindHeading && referenced[b.Anchor] {
						refN++
					}
				}
			}
		}
		views = append(views, sourceView{
			ID: s.ID, Title: s.Title, Kind: s.Kind, File: sourceFile(&s), URL: s.URL,
			Blocks: blocks, ReferencedBlocks: refN, AddedAt: s.AddedAt, UpdatedAt: s.UpdatedAt,
		})
		sb.WriteString(fmt.Sprintf("  - %s [%s] %s  blocks=%d referenced=%d  .reqboard/%s\n", s.ID, s.Kind, s.Title, blocks, refN, sourceFile(&s)))
		if s.URL != "" {
			sb.WriteString("      url: " + s.URL + "\n")
		}
	}
	ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "sources": views})
	return nil
}

func sourceRemove(a *Args, id string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		src := dag.Source(id)
		if src == nil {
			return fail("source %q not found (run `reqboard source list`)", id)
		}
		prefix := id + ":"
		var owners []string
		for _, n := range dag.Nodes {
			for _, r := range n.PRDRefs {
				if strings.HasPrefix(r, prefix) {
					owners = append(owners, n.ID)
					break
				}
			}
		}
		sort.Strings(owners)
		if len(owners) > 0 && !a.Bool("force") {
			return &Error{Code: 1, Msg: "source is still referenced", Text: fmt.Sprintf("[REQBOARD BLOCKED] 补充来源 %s 仍被 %d 个节点引用：%s\n  -> 先改掉这些节点的 prd_refs，或加 --force 连同锚点引用一并清理。\n", id, len(owners), strings.Join(owners, ", "))}
		}
		file := sourceFile(src)
		stripped := 0
		if len(owners) > 0 {
			for _, n := range dag.Nodes {
				var kept []string
				for _, r := range n.PRDRefs {
					if strings.HasPrefix(r, prefix) {
						stripped++
						continue
					}
					kept = append(kept, r)
				}
				n.PRDRefs = kept
			}
		}
		var keptSources []model.Source
		for _, s := range dag.Sources {
			if s.ID != id {
				keptSources = append(keptSources, s)
			}
		}
		dag.Sources = keptSources
		if err := ctx.SaveDAG(dag, "source.remove", "", fmt.Sprintf("%s (stripped %d refs)", id, stripped)); err != nil {
			return err
		}
		if err := os.Remove(ctx.Store.Path(file)); err != nil && !os.IsNotExist(err) {
			return fail("remove .reqboard/%s: %v", file, err)
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD SOURCE REMOVE] %s  %s\n", id, src.Title))
		sb.WriteString(fmt.Sprintf("  已删除 .reqboard/%s；清理节点锚点引用 %d 条。\n", file, stripped))
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "source_id": id, "stripped_refs": stripped, "owners": owners})
		return nil
	})
}

func isHTTPURL(u string) bool {
	lower := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// loadSourceDocs parses every registered supplemental source.
func loadSourceDocs(ctx *Ctx, dag *model.DAG) map[string]*prd.Document {
	out := map[string]*prd.Document{}
	for i := range dag.Sources {
		s := &dag.Sources[i]
		raw, err := ctx.Store.ReadFile(sourceFile(s))
		if err != nil {
			continue
		}
		doc, err := prd.Parse(raw)
		if err != nil {
			continue
		}
		out[s.ID] = doc
	}
	return out
}

// anchorBlock resolves an anchor against the main PRD and supplemental sources.
func anchorBlock(anchor string, mainDoc *prd.Document, sources map[string]*prd.Document) (*prd.Block, string) {
	if mainDoc != nil {
		if b := mainDoc.Anchor(anchor); b != nil {
			return b, prd.DefaultPrefix
		}
	}
	prefix := prd.PrefixOf(anchor)
	if prefix == "" || prefix == prd.DefaultPrefix {
		return nil, prefix
	}
	if doc := sources[prefix]; doc != nil {
		if b := doc.Anchor(anchor); b != nil {
			return b, prefix
		}
	}
	return nil, prefix
}
