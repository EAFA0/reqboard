// Package prd parses markdown PRDs into stable, anchorable blocks and keeps
// anchors stable across edits (heading path + text similarity).
package prd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Block kinds.
const (
	KindHeading   = "heading"
	KindParagraph = "paragraph"
	KindList      = "list"
	KindTable     = "table"
	KindCode      = "code"
)

var (
	anchorRe      = regexp.MustCompile(`^<!--\s*@(p:[^\s>]+)\s*-->\s*$`)
	headingRe     = regexp.MustCompile(`^(#{1,6})\s+(.*\S)\s*$`)
	numericHeadRe = regexp.MustCompile(`^(\d+(?:\.\d+)*)\.?(?:\s+|$)`)
	listRe        = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	fenceRe       = regexp.MustCompile("^\\s*(```|~~~)")
)

// Block is one anchored unit of the PRD: a heading, paragraph, list item,
// table row or code fence.
type Block struct {
	Anchor     string `json:"anchor"`
	Kind       string `json:"kind"`
	Level      int    `json:"level,omitempty"`
	Section    string `json:"section"`
	SectionKey string `json:"section_key"`
	SectionIdx int    `json:"-"`
	Ordinal    int    `json:"ordinal"`
	Text       string `json:"text"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
}

// Document is a parsed PRD.
type Document struct {
	Blocks  []*Block `json:"blocks"`
	Heading []*Block `json:"-"`
}

// AnchorSet returns anchor -> block.
func (d *Document) AnchorSet() map[string]*Block {
	m := make(map[string]*Block, len(d.Blocks))
	for _, b := range d.Blocks {
		m[b.Anchor] = b
	}
	return m
}

// Anchor returns one block by anchor.
func (d *Document) Anchor(anchor string) *Block {
	for _, b := range d.Blocks {
		if b.Anchor == anchor {
			return b
		}
	}
	return nil
}

// Anchors returns all anchors in document order.
func (d *Document) Anchors() []string {
	out := make([]string, 0, len(d.Blocks))
	for _, b := range d.Blocks {
		out = append(out, b.Anchor)
	}
	return out
}

type rawBlock struct {
	kind       string
	level      int
	text       string
	start, end int
	oldAnchor  string
}

// Parse converts markdown source into blocks. Existing @p anchors are honoured
// when present, otherwise new p:<section>-<ordinal> anchors are generated.
func Parse(src []byte) (*Document, error) {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var raws []rawBlock

	var cur *rawBlock
	flush := func(end int) {
		if cur == nil {
			return
		}
		cur.text = strings.TrimRight(cur.text, "\n")
		cur.end = end
		if strings.TrimSpace(cur.text) != "" || cur.kind == KindHeading {
			raws = append(raws, *cur)
		}
		cur = nil
	}

	pendingAnchor := ""
	inFence := false
	fenceMark := ""
	fenceStart := 0
	var fenceLines []string
	inTable := false
	prevWasTableSep := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)

		// Anchor lines are metadata, never content (but keep them inside code
		// fences as literal text).
		if !inFence {
			if m := anchorRe.FindStringSubmatch(trimmed); m != nil {
				flush(lineNo - 1)
				pendingAnchor = m[1]
				continue
			}
		}

		if inFence {
			fenceLines = append(fenceLines, line)
			if strings.HasPrefix(trimmed, fenceMark) {
				raws = append(raws, rawBlock{
					kind:      KindCode,
					text:      strings.Join(fenceLines, "\n"),
					start:     fenceStart,
					end:       lineNo,
					oldAnchor: pendingAnchor,
				})
				pendingAnchor = ""
				inFence = false
			}
			continue
		}
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			flush(lineNo - 1)
			inFence = true
			fenceMark = m[1]
			fenceStart = lineNo
			fenceLines = []string{line}
			inTable = false
			continue
		}

		if trimmed == "" {
			flush(lineNo - 1)
			inTable = false
			prevWasTableSep = false
			continue
		}

		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush(lineNo - 1)
			inTable = false
			raws = append(raws, rawBlock{
				kind:      KindHeading,
				level:     len(m[1]),
				text:      line,
				start:     lineNo,
				end:       lineNo,
				oldAnchor: pendingAnchor,
			})
			pendingAnchor = ""
			continue
		}

		if isTableRow(trimmed) {
			if inTable && cur != nil && cur.kind == KindTable {
				// A separator row belongs to the header row so the rendered
				// markdown table stays valid.
				if isTableSeparator(trimmed) && !prevWasTableSep {
					cur.text += "\n" + line
					cur.end = lineNo
					prevWasTableSep = true
					continue
				}
				flush(lineNo - 1)
			} else {
				flush(lineNo - 1)
			}
			cur = &rawBlock{kind: KindTable, text: line, start: lineNo, end: lineNo, oldAnchor: pendingAnchor}
			pendingAnchor = ""
			inTable = true
			prevWasTableSep = isTableSeparator(trimmed)
			continue
		}
		inTable = false
		prevWasTableSep = false

		if m := listRe.FindStringSubmatch(line); m != nil {
			flush(lineNo - 1)
			cur = &rawBlock{kind: KindList, text: line, start: lineNo, end: lineNo, oldAnchor: pendingAnchor}
			pendingAnchor = ""
			continue
		}

		// Continuation of a list item or paragraph.
		if cur != nil && (cur.kind == KindList || cur.kind == KindParagraph) {
			cur.text += "\n" + line
			cur.end = lineNo
			continue
		}
		flush(lineNo - 1)
		cur = &rawBlock{kind: KindParagraph, text: line, start: lineNo, end: lineNo, oldAnchor: pendingAnchor}
		pendingAnchor = ""
	}
	if inFence {
		raws = append(raws, rawBlock{
			kind:      KindCode,
			text:      strings.Join(fenceLines, "\n"),
			start:     fenceStart,
			end:       len(lines),
			oldAnchor: pendingAnchor,
		})
	}
	flush(len(lines))

	doc := &Document{Blocks: []*Block{}}
	used := map[string]bool{}
	usedSection := map[string]int{}
	ordinals := map[string]int{}
	var headingStack []struct {
		level int
		text  string
		key   string
	}
	var counters []int
	var headingIdx = -1
	sectionKey := "0"
	sectionText := ""

	for _, rb := range raws {
		if rb.kind == KindHeading {
			headingIdx++
			htext := strings.TrimSpace(strings.TrimLeft(rb.text, "#"))
			level := rb.level
			if level < 1 {
				level = 1
			}
			for len(headingStack) > 0 && headingStack[len(headingStack)-1].level >= level {
				headingStack = headingStack[:len(headingStack)-1]
			}
			if len(counters) < level {
				counters = append(counters, make([]int, level-len(counters))...)
			}
			counters = counters[:level]
			counters[level-1]++
			for i := level; i < len(counters); i++ {
				counters[i] = 0
			}
			key := ""
			if m := numericHeadRe.FindStringSubmatch(htext); m != nil {
				key = m[1]
			} else {
				var parts []string
				for _, c := range counters[:level] {
					parts = append(parts, fmt.Sprintf("%d", c))
				}
				key = "h" + strings.Join(parts, ".")
			}
			if n := usedSection[key]; n > 0 {
				usedSection[key] = n + 1
				key = fmt.Sprintf("%s~%d", key, n+1)
			} else {
				usedSection[key] = 1
			}
			headingStack = append(headingStack, struct {
				level int
				text  string
				key   string
			}{level, htext, key})
			sectionKey = key
			sectionText = htext
		}
		ord := ordinals[sectionKey] + 1
		ordinals[sectionKey] = ord
		anchor := rb.oldAnchor
		if anchor == "" || used[anchor] {
			base := fmt.Sprintf("p:%s-%d", sectionKey, ord)
			anchor = base
			for n := 2; used[anchor]; n++ {
				anchor = fmt.Sprintf("%s.%d", base, n)
			}
		}
		used[anchor] = true
		b := &Block{
			Anchor:     anchor,
			Kind:       rb.kind,
			Level:      rb.level,
			Section:    sectionText,
			SectionKey: sectionKey,
			SectionIdx: headingIdx,
			Ordinal:    ord,
			Text:       rb.text,
			StartLine:  rb.start,
			EndLine:    rb.end,
		}
		doc.Blocks = append(doc.Blocks, b)
		if b.Kind == KindHeading {
			doc.Heading = append(doc.Heading, b)
		}
	}
	return doc, nil
}

func isTableRow(s string) bool {
	return strings.HasPrefix(s, "|") && strings.HasSuffix(s, "|") && len(s) >= 2
}

func isTableSeparator(s string) bool {
	if !strings.HasPrefix(s, "|") {
		return false
	}
	core := strings.Trim(s, "| \t")
	if core == "" || !strings.Contains(core, "-") {
		return false
	}
	for _, r := range core {
		if r != '|' && r != '-' && r != ':' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

// Render emits prd.md with anchors in place.
func (d *Document) Render() []byte {
	var sb strings.Builder
	for i, b := range d.Blocks {
		if i > 0 {
			if tight(d.Blocks[i-1], b) {
				sb.WriteString("\n")
			} else {
				sb.WriteString("\n\n")
			}
		}
		sb.WriteString("<!-- @")
		sb.WriteString(b.Anchor)
		sb.WriteString(" -->\n")
		sb.WriteString(b.Text)
	}
	if len(d.Blocks) > 0 {
		sb.WriteString("\n")
	}
	return []byte(sb.String())
}

func tight(a, b *Block) bool {
	if a.SectionIdx != b.SectionIdx {
		return false
	}
	if a.Kind == KindList && b.Kind == KindList {
		return true
	}
	if a.Kind == KindTable && b.Kind == KindTable {
		return true
	}
	return false
}

// Pair links an old and new block that were matched during sync.
type Pair struct {
	Old *Block `json:"old"`
	New *Block `json:"new"`
}

// SyncResult reports how a PRD update changed the anchored document.
type SyncResult struct {
	Document  *Document `json:"-"`
	Modified  []Pair    `json:"modified"`
	Added     []*Block  `json:"added"`
	Deleted   []*Block  `json:"deleted"`
	Unchanged int       `json:"unchanged"`
}

// Sync aligns new raw markdown against the previous document, preserving
// anchors for matched blocks and emitting modified/added/deleted blocks.
func Sync(old *Document, newDoc *Document) *SyncResult {
	res := &SyncResult{Document: newDoc}
	if old == nil {
		for _, b := range newDoc.Blocks {
			res.Added = append(res.Added, b)
		}
		return res
	}

	oldHead := old.Heading
	newHead := newDoc.Heading
	headingMap := map[int]int{} // new heading index -> old heading index
	oldHeadUsed := map[int]bool{}

	// Pass 1: exact heading text (same level).
	for ni, nh := range newHead {
		for oi, oh := range oldHead {
			if oldHeadUsed[oi] || oh.Level != nh.Level {
				continue
			}
			if Normalize(oh.Text) == Normalize(nh.Text) {
				headingMap[ni] = oi
				oldHeadUsed[oi] = true
				break
			}
		}
	}
	// Pass 2: same level, nearby position, fuzzy text.
	for ni, nh := range newHead {
		if _, ok := headingMap[ni]; ok {
			continue
		}
		best, bestScore := -1, 0.0
		for oi, oh := range oldHead {
			if oldHeadUsed[oi] || oh.Level != nh.Level {
				continue
			}
			if abs(ni-oi) > 3 {
				continue
			}
			sc := Similarity(oh.Text, nh.Text)
			if sc > bestScore {
				best, bestScore = oi, sc
			}
		}
		if best >= 0 && bestScore >= 0.35 {
			headingMap[ni] = best
			oldHeadUsed[best] = true
		}
	}

	// oldSectionForNewSection maps a new section heading index (-1 = root) to
	// the corresponding old section index.
	oldSectionForNew := func(newIdx int) int {
		if newIdx < 0 {
			return -1
		}
		if oi, ok := headingMap[newIdx]; ok {
			return oi
		}
		return -2 // new section: no old counterpart
	}

	matchedOld := map[int]bool{}
	matchedNew := map[int]bool{}
	newToOld := map[int]int{}

	// Headings matched above.
	for ni, oi := range headingMap {
		if ni < len(newHead) && oi < len(oldHead) {
			// Locate the block index of each heading.
			nb := newHead[ni]
			ob := oldHead[oi]
			for i, b := range newDoc.Blocks {
				if b == nb {
					for j, o := range old.Blocks {
						if o == ob {
							newToOld[i] = j
							matchedNew[i] = true
							matchedOld[j] = true
							break
						}
					}
					break
				}
			}
		}
	}

	oldIndex := map[*Block]int{}
	for i, b := range old.Blocks {
		oldIndex[b] = i
	}
	newIndex := map[*Block]int{}
	for i, b := range newDoc.Blocks {
		newIndex[b] = i
	}

	// Pass 3: exact text within the corresponding old section.
	for i, nb := range newDoc.Blocks {
		if matchedNew[i] || nb.Kind == KindHeading {
			continue
		}
		wantSection := oldSectionForNew(nb.SectionIdx)
		if wantSection == -2 {
			continue
		}
		for j, ob := range old.Blocks {
			if matchedOld[j] || ob.Kind != nb.Kind {
				continue
			}
			if ob.SectionIdx != wantSection {
				continue
			}
			if Normalize(ob.Text) == Normalize(nb.Text) {
				newToOld[i] = j
				matchedNew[i] = true
				matchedOld[j] = true
				break
			}
		}
	}

	// Pass 4: fuzzy text within the corresponding old section.
	for i, nb := range newDoc.Blocks {
		if matchedNew[i] || nb.Kind == KindHeading {
			continue
		}
		wantSection := oldSectionForNew(nb.SectionIdx)
		if wantSection == -2 {
			continue
		}
		best, bestScore := -1, 0.0
		for j, ob := range old.Blocks {
			if matchedOld[j] || ob.Kind != nb.Kind || ob.SectionIdx != wantSection {
				continue
			}
			sc := Similarity(ob.Text, nb.Text)
			if sc > bestScore {
				best, bestScore = j, sc
			}
		}
		if best >= 0 && bestScore >= 0.45 {
			newToOld[i] = best
			matchedNew[i] = true
			matchedOld[best] = true
		}
	}

	// Pass 5: moved block, very high similarity anywhere.
	for i, nb := range newDoc.Blocks {
		if matchedNew[i] || nb.Kind == KindHeading {
			continue
		}
		best, bestScore := -1, 0.0
		for j, ob := range old.Blocks {
			if matchedOld[j] || ob.Kind != nb.Kind {
				continue
			}
			sc := Similarity(ob.Text, nb.Text)
			if sc > bestScore {
				best, bestScore = j, sc
			}
		}
		if best >= 0 && bestScore >= 0.8 {
			newToOld[i] = best
			matchedNew[i] = true
			matchedOld[best] = true
		}
	}

	// Preserve anchors for matched blocks; collect the used set.
	usedAnchors := map[string]bool{}
	for ni, oi := range newToOld {
		nb := newDoc.Blocks[ni]
		ob := old.Blocks[oi]
		nb.Anchor = ob.Anchor
		usedAnchors[ob.Anchor] = true
	}
	// Deleted blocks keep their old anchor in the report.
	for j, ob := range old.Blocks {
		if !matchedOld[j] {
			res.Deleted = append(res.Deleted, ob)
		}
	}
	// Reassign anchors for unmatched new blocks to avoid collisions with
	// preserved anchors.
	ordinals := map[string]int{}
	for i, nb := range newDoc.Blocks {
		if matchedNew[i] {
			ord := ordinals[nb.SectionKey] + 1
			ordinals[nb.SectionKey] = ord
			continue
		}
		ord := ordinals[nb.SectionKey] + 1
		ordinals[nb.SectionKey] = ord
		if usedAnchors[nb.Anchor] {
			base := fmt.Sprintf("p:%s-%d", nb.SectionKey, ord)
			anchor := base
			for n := 2; usedAnchors[anchor]; n++ {
				anchor = fmt.Sprintf("%s.%d", base, n)
			}
			nb.Anchor = anchor
		}
		usedAnchors[nb.Anchor] = true
	}

	for i, nb := range newDoc.Blocks {
		if !matchedNew[i] {
			res.Added = append(res.Added, nb)
			continue
		}
		ob := old.Blocks[newToOld[i]]
		if Normalize(ob.Text) != Normalize(nb.Text) || ob.Kind != nb.Kind {
			res.Modified = append(res.Modified, Pair{Old: ob, New: nb})
		} else {
			res.Unchanged++
		}
	}
	sort.SliceStable(res.Modified, func(i, j int) bool {
		return newIndex[res.Modified[i].New] < newIndex[res.Modified[j].New]
	})
	sort.SliceStable(res.Deleted, func(i, j int) bool {
		return oldIndex[res.Deleted[i]] < oldIndex[res.Deleted[j]]
	})
	res.Document = newDoc
	return res
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Normalize lowercases and removes whitespace/punctuation, keeping letters,
// digits and CJK runes.
func Normalize(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// Similarity returns a 0..1 score combining rune-bigram Jaccard and common
// prefix ratio. It is robust for Chinese PRDs (no word segmentation needed).
func Similarity(a, b string) float64 {
	na, nb := Normalize(a), Normalize(b)
	if na == "" && nb == "" {
		return 1
	}
	if na == nb {
		return 1
	}
	if na == "" || nb == "" {
		return 0
	}
	ra, rb := []rune(na), []rune(nb)
	ga, gb := bigrams(ra), bigrams(rb)
	inter := 0
	for g, c := range ga {
		if d, ok := gb[g]; ok {
			if c < d {
				inter += c
			} else {
				inter += d
			}
		}
	}
	total := 0
	for _, c := range ga {
		total += c
	}
	for _, c := range gb {
		total += c
	}
	jac := 0.0
	if total > 0 {
		jac = 2 * float64(inter) / float64(total)
	}
	prefix := 0
	for prefix < len(ra) && prefix < len(rb) && ra[prefix] == rb[prefix] {
		prefix++
	}
	maxLen := len(ra)
	if len(rb) > maxLen {
		maxLen = len(rb)
	}
	pref := 0.0
	if maxLen > 0 {
		pref = float64(prefix) / float64(maxLen)
	}
	return 0.7*jac + 0.3*pref
}

func bigrams(r []rune) map[string]int {
	m := map[string]int{}
	if len(r) == 1 {
		m[string(r)] = 1
		return m
	}
	for i := 0; i+1 < len(r); i++ {
		m[string(r[i:i+2])]++
	}
	return m
}

// Summary returns a short single-line preview of a block.
func Summary(b *Block) string {
	s := strings.Join(strings.Fields(b.Text), " ")
	if len([]rune(s)) > 60 {
		return string([]rune(s)[:60]) + "…"
	}
	return s
}
