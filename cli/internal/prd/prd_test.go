package prd

import (
	"strings"
	"testing"
)

const samplePRD = `# 订单可见性需求

## 3.1 创单

### 3.1.1 基础创单
- 用户可创建订单。
- 创单需校验库存。

### 3.1.2 退订
退订不在本期范围。

| 场景 | 规则 |
| --- | --- |
| 线上单 | 待确认可见 |
| 线下单 | 待支付后可见 |

` + "```go\n// <!-- @p:fake-anchor -->\nfunc Demo() {}\n```\n" + `
## 3.2 查询
订单列表需要展示状态。
`

func TestParseAnchorsStable(t *testing.T) {
	doc, err := Parse([]byte(samplePRD))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) == 0 {
		t.Fatal("no blocks")
	}
	anchors := doc.Anchors()
	want := []string{"p:h1-1", "p:3.1-1", "p:3.1.1-1", "p:3.1.1-2", "p:3.1.1-3", "p:3.1.2-1", "p:3.1.2-2", "p:3.1.2-3", "p:3.1.2-4", "p:3.1.2-5", "p:3.1.2-6", "p:3.2-1", "p:3.2-2"}
	if len(anchors) != len(want) {
		t.Fatalf("anchors = %v, want %d entries", anchors, len(want))
	}
	for i := range want {
		if anchors[i] != want[i] {
			t.Fatalf("anchor[%d] = %q, want %q (all=%v)", i, anchors[i], want[i], anchors)
		}
	}
	// Anchor-like text inside a code fence must survive as content.
	found := false
	for _, b := range doc.Blocks {
		if b.Kind == KindCode && strings.Contains(b.Text, "@p:fake-anchor") {
			found = true
		}
	}
	if !found {
		t.Fatal("code fence lost anchor-like content")
	}

	rendered := doc.Render()
	again, err := Parse(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(again.Anchors(), ",") != strings.Join(anchors, ",") {
		t.Fatalf("anchors not stable across render/parse:\n%s", rendered)
	}
	// Table separator must be preserved inside the header block.
	if !strings.Contains(string(rendered), "| 场景 | 规则 |\n| --- | --- |") {
		t.Fatalf("table separator lost:\n%s", rendered)
	}
}

func TestSyncModifiedAddedDeleted(t *testing.T) {
	oldDoc, err := Parse([]byte(samplePRD))
	if err != nil {
		t.Fatal(err)
	}
	newMarkdown := strings.Replace(samplePRD, "- 创单需校验库存。", "- 创单需校验库存与风控。", 1)
	newMarkdown = strings.Replace(newMarkdown, "订单列表需要展示状态。", "订单列表需要展示状态与筛选。", 1)
	newMarkdown += "\n### 3.3 新增规则\n这是新增段落。\n"
	newMarkdown = strings.Replace(newMarkdown, "退订不在本期范围。\n", "", 1)
	newDoc, err := Parse([]byte(newMarkdown))
	if err != nil {
		t.Fatal(err)
	}
	res := Sync(oldDoc, newDoc)
	if len(res.Modified) != 2 {
		t.Fatalf("modified = %d (%+v), want 2", len(res.Modified), res.Modified)
	}
	if len(res.Added) == 0 {
		t.Fatal("expected added blocks")
	}
	if len(res.Deleted) != 1 {
		t.Fatalf("deleted = %d, want 1", len(res.Deleted))
	}
	if res.Deleted[0].Anchor != "p:3.1.2-2" {
		t.Fatalf("deleted anchor = %s, want p:3.1.2-2", res.Deleted[0].Anchor)
	}
	// Modified anchors must be preserved.
	for _, p := range res.Modified {
		if p.Old.Anchor != p.New.Anchor {
			t.Fatalf("anchor changed on modify: %s -> %s", p.Old.Anchor, p.New.Anchor)
		}
	}
	// The deleted paragraph's anchor must not be reused.
	for _, b := range newDoc.Blocks {
		if b.Anchor == "p:3.1.2-2" && strings.Contains(b.Text, "新增") {
			t.Fatalf("reused deleted anchor: %+v", b)
		}
	}
}

func TestSyncHeadingRenameKeepsChildrenAnchors(t *testing.T) {
	oldMarkdown := "# 需求\n\n## 3.1 创单\n\n- 用户可以创建订单。\n- 创单需校验库存。\n"
	newMarkdown := "# 需求\n\n## 3.1 创单流程\n\n- 用户可以创建订单。\n- 创单需校验库存。\n"
	oldDoc, _ := Parse([]byte(oldMarkdown))
	newDoc, _ := Parse([]byte(newMarkdown))
	res := Sync(oldDoc, newDoc)
	if len(res.Modified) != 1 {
		t.Fatalf("modified = %d, want 1 (heading only): %+v", len(res.Modified), res.Modified)
	}
	if res.Modified[0].Old.Anchor != res.Modified[0].New.Anchor {
		t.Fatal("heading anchor not preserved")
	}
	for _, b := range newDoc.Blocks {
		if strings.HasPrefix(b.Text, "- ") {
			if b.Anchor == "" || strings.Contains(b.Anchor, "~") {
				t.Fatalf("child anchor not preserved: %+v", b)
			}
		}
	}
	// Children must keep their old anchors.
	if newDoc.Blocks[2].Anchor != oldDoc.Blocks[2].Anchor || newDoc.Blocks[3].Anchor != oldDoc.Blocks[3].Anchor {
		t.Fatalf("child anchors changed: old=%v new=%v", oldDoc.Anchors(), newDoc.Anchors())
	}
}

func TestSyncWhitespaceOnlyIsUnchanged(t *testing.T) {
	oldMarkdown := "## 1 规则\n\n订单必须校验库存。\n"
	newMarkdown := "## 1 规则\n\n订单必须校验库存。   \n"
	oldDoc, _ := Parse([]byte(oldMarkdown))
	newDoc, _ := Parse([]byte(newMarkdown))
	res := Sync(oldDoc, newDoc)
	if len(res.Modified) != 0 || len(res.Added) != 0 || len(res.Deleted) != 0 {
		t.Fatalf("whitespace-only change should be unchanged: %+v", res)
	}
}

func TestSimilarity(t *testing.T) {
	if Similarity("订单必须校验库存", "订单必须校验库存") != 1 {
		t.Fatal("identical similarity != 1")
	}
	if Similarity("订单必须校验库存", "完全不同的内容") >= 0.4 {
		t.Fatal("unrelated text scored too high")
	}
	if Similarity("订单必须校验库存", "订单必须校验库存与风控") <= 0.5 {
		t.Fatal("near-identical text scored too low")
	}
}

func TestParseListContinuation(t *testing.T) {
	md := "## 1 规则\n\n- 第一条规则\n  续行内容\n- 第二条规则\n"
	doc, _ := Parse([]byte(md))
	if len(doc.Blocks) != 3 {
		t.Fatalf("blocks = %d, want 3: %+v", len(doc.Blocks), doc.Blocks)
	}
	if !strings.Contains(doc.Blocks[1].Text, "续行内容") {
		t.Fatalf("list continuation lost: %+v", doc.Blocks[1])
	}
}
