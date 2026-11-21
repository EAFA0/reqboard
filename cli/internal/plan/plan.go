// Package plan validates and imports a planner-produced DAG.
package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"reqboard/internal/model"
	"reqboard/internal/prd"
)

// File is the plan.json input schema. `depends_on` is a convenience that
// creates hard edges; explicit edges take precedence.
type File struct {
	RequirementName string `json:"requirement_name,omitempty"`
	Nodes           []Node `json:"nodes"`
	Edges           []Edge `json:"edges,omitempty"`
}

// Node is one planned DAG node.
type Node struct {
	ID          string          `json:"id,omitempty"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Scope       string          `json:"scope,omitempty"`
	Status      string          `json:"status,omitempty"`
	PRDRefs     []string        `json:"prd_refs,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	DependsOn   []string        `json:"depends_on,omitempty"`
	Position    *model.Position `json:"position,omitempty"`
}

// Edge is one planned edge.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"`
	Note string `json:"note,omitempty"`
}

// Options controls import behaviour.
type Options struct {
	Replace          bool
	Layout           bool
	AllowUnknownRefs bool
}

// Result is the import report.
type Result struct {
	AddedNodes   []string `json:"added_nodes"`
	UpdatedNodes []string `json:"updated_nodes"`
	AddedEdges   []string `json:"added_edges"`
	Warnings     []string `json:"warnings"`
	Coverage     Coverage `json:"coverage"`
	Layout       bool     `json:"layout"`
}

// Coverage is the PRD coverage snapshot after import.
type Coverage struct {
	Total    int      `json:"total"`
	Covered  int      `json:"covered"`
	Ignored  int      `json:"ignored"`
	Unmapped []string `json:"unmapped"`
	Percent  float64  `json:"percent"`
}

// Load reads a plan file.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse plan %s: %w", path, err)
	}
	if len(f.Nodes) == 0 {
		return nil, fmt.Errorf("plan has no nodes")
	}
	return &f, nil
}

// Import validates the plan and merges it into the DAG.
func Import(dag *model.DAG, doc *prd.Document, f *File, opts Options) (*Result, error) {
	if f == nil || len(f.Nodes) == 0 {
		return nil, fmt.Errorf("plan has no nodes")
	}
	res := &Result{Layout: opts.Layout}
	if opts.Replace {
		dag.Nodes = []*model.Node{}
		dag.Edges = []*model.Edge{}
	}

	used := dag.NodeIDSet()
	planNodeID := map[int]string{}

	for i, pn := range f.Nodes {
		title := strings.TrimSpace(pn.Title)
		if title == "" {
			return nil, fmt.Errorf("node #%d has empty title", i+1)
		}
		scope := pn.Scope
		if scope == "" {
			scope = model.ScopeMine
		}
		if !model.ValidScope(scope) {
			return nil, fmt.Errorf("node %q has invalid scope %q", title, scope)
		}
		status := pn.Status
		if status == "" {
			status = model.StatusPending
		}
		if !model.ValidStatus(status) {
			return nil, fmt.Errorf("node %q has invalid status %q", title, status)
		}
		id := strings.TrimSpace(pn.ID)
		if id == "" {
			id = model.NewID("rb", used, fmt.Sprintf("%s#%d", title, i))
		}
		if used[id] {
			if existing := dag.Node(id); existing != nil {
				// Merge into the existing node.
				existing.Title = title
				existing.Description = pn.Description
				existing.Scope = scope
				existing.PRDRefs = dedup(pn.PRDRefs)
				existing.Tags = dedup(pn.Tags)
				existing.UpdatedAt = model.Now()
				res.UpdatedNodes = append(res.UpdatedNodes, id)
				planNodeID[i] = id
				continue
			}
			return nil, fmt.Errorf("duplicate node id %q in plan", id)
		}
		used[id] = true
		pos := model.Position{}
		if pn.Position != nil {
			pos = *pn.Position
		}
		n := &model.Node{
			ID:          id,
			Title:       title,
			Description: pn.Description,
			Scope:       scope,
			Status:      status,
			PRDRefs:     dedup(pn.PRDRefs),
			Tags:        dedup(pn.Tags),
			SelfMocks:   []model.SelfMock{},
			ReviewNotes: []model.ReviewNote{},
			Ops:         []model.OpItem{},
			Position:    pos,
			UpdatedAt:   model.Now(),
		}
		dag.AddNode(n)
		res.AddedNodes = append(res.AddedNodes, id)
		planNodeID[i] = id
	}

	// Build edge set.
	edgeKey := func(from, to string) string { return from + "\x00" + to }
	existingEdges := map[string]bool{}
	edgeIDs := dag.EdgeIDSet()
	for _, e := range dag.Edges {
		existingEdges[edgeKey(e.From, e.To)] = true
	}
	addEdge := func(from, to, kind, note string) error {
		if kind == "" {
			kind = model.EdgeHard
		}
		if !model.ValidEdgeKind(kind) {
			return fmt.Errorf("edge %s->%s has invalid kind %q", from, to, kind)
		}
		if dag.Node(from) == nil {
			return fmt.Errorf("edge references unknown from node %q", from)
		}
		if dag.Node(to) == nil {
			return fmt.Errorf("edge references unknown to node %q", to)
		}
		if from == to {
			return fmt.Errorf("self edge on node %q is not allowed", from)
		}
		if existingEdges[edgeKey(from, to)] {
			return nil
		}
		id := model.NewID("ed", edgeIDs, from, to, kind)
		edgeIDs[id] = true
		existingEdges[edgeKey(from, to)] = true
		dag.AddEdge(&model.Edge{
			ID: id, From: from, To: to, Kind: kind, Note: note,
			UpdatedBy: "planner", UpdatedAt: model.Now(),
		})
		res.AddedEdges = append(res.AddedEdges, id)
		return nil
	}

	for _, pe := range f.Edges {
		if err := addEdge(pe.From, pe.To, pe.Kind, pe.Note); err != nil {
			return nil, err
		}
	}
	for i, pn := range f.Nodes {
		from := planNodeID[i]
		if from == "" {
			from = findIDByTitle(dag, pn.Title)
		}
		if from == "" {
			return nil, fmt.Errorf("cannot resolve node id for %q", pn.Title)
		}
		for _, dep := range pn.DependsOn {
			to := dep
			if dag.Node(to) == nil {
				to = findIDByTitle(dag, dep)
			}
			if to == "" {
				return nil, fmt.Errorf("node %q depends_on unknown node %q", pn.Title, dep)
			}
			// `depends_on` means the declared node depends on `to`, so the
			// dependency edge points from the prerequisite to the node.
			if err := addEdge(to, from, model.EdgeHard, ""); err != nil {
				return nil, err
			}
		}
	}

	// Validate DAG: cycles and dangling endpoints.
	if _, err := dag.TopoSort(); err != nil {
		return nil, err
	}

	// Business leaves must be anchored to PRD paragraphs.
	var missing []string
	for _, n := range dag.Nodes {
		if n.Scope == model.ScopeMine && dag.Outdegree(n.ID) == 0 && len(n.PRDRefs) == 0 {
			missing = append(missing, fmt.Sprintf("%s (%s)", n.ID, n.Title))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("business leaf nodes must contain prd_refs: %s", strings.Join(missing, ", "))
	}

	// Validate PRD anchors.
	if doc != nil {
		anchors := doc.AnchorSet()
		for _, n := range dag.Nodes {
			for _, ref := range n.PRDRefs {
				if anchors[ref] == nil {
					msg := fmt.Sprintf("node %s references unknown PRD anchor %q", n.ID, ref)
					if opts.AllowUnknownRefs {
						res.Warnings = append(res.Warnings, msg)
					} else {
						return nil, fmt.Errorf("%s (pass --allow-unknown-refs to import anyway)", msg)
					}
				}
			}
		}
	}

	if opts.Layout {
		Layout(dag)
	}
	res.Coverage = ComputeCoverage(dag, doc)
	return res, nil
}

func findIDByTitle(dag *model.DAG, title string) string {
	for _, n := range dag.Nodes {
		if n.Title == title {
			return n.ID
		}
	}
	return ""
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// Layout assigns compact left-to-right layered coordinates to the main canvas
// (mine or proposed nodes). External / collaborator / out-of-scope nodes are
// parked in a separate compact shelf above the main grid: they must never add
// phantom columns that stretch the main DAG.
func Layout(dag *model.DAG) { LayoutWith(dag, false) }

// LayoutWith is Layout plus an optional full-graph mode. When includeExternal
// is true, external / collaborator nodes are placed in the leftmost source
// column (Column 0) and flow naturally into the left-to-right topology instead
// of sitting in a long shelf above the canvas.
func LayoutWith(dag *model.DAG, includeExternal bool) {
	if len(dag.Nodes) == 0 {
		return
	}
	order, err := dag.TopoSort()
	if err != nil {
		return
	}
	if includeExternal {
		layoutFull(dag, order)
		return
	}
	isMain := func(n *model.Node) bool {
		return n.Scope == model.ScopeMine || n.Status == model.StatusProposed
	}
	mainSet := map[string]bool{}
	var main, parked []*model.Node
	for _, n := range dag.Nodes {
		if isMain(n) {
			mainSet[n.ID] = true
			main = append(main, n)
		} else {
			parked = append(parked, n)
		}
	}

	// Depth only follows main -> main dependencies; external upstreams must not
	// add columns to the main canvas.
	depth := map[string]int{}
	for _, id := range order {
		if !mainSet[id] {
			continue
		}
		for _, e := range dag.Edges {
			if e.From != id || !mainSet[e.To] {
				continue
			}
			if depth[id]+1 > depth[e.To] {
				depth[e.To] = depth[id] + 1
			}
		}
	}
	rightAlignDepths(dag, order, mainSet, nil, nil, depth)
	byDepth := map[int][]*model.Node{}
	maxDepth := 0
	for _, n := range main {
		d := depth[n.ID]
		byDepth[d] = append(byDepth[d], n)
		if d > maxDepth {
			maxDepth = d
		}
	}
	maxCount := 1
	for _, group := range byDepth {
		if len(group) > maxCount {
			maxCount = len(group)
		}
	}

	// Park non-main nodes in a compact shelf above the main grid. Eight per row
	// keeps the shelf shallow even for real PRDs with dozens of external nodes.
	const parkedCols = 8
	const parkedGapX = 280.0
	const parkedGapY = 150.0
	parkedRows := 0
	if len(parked) > 0 {
		sort.SliceStable(parked, func(i, j int) bool {
			if parked[i].Scope != parked[j].Scope {
				return parked[i].Scope < parked[j].Scope
			}
			return parked[i].Title < parked[j].Title
		})
		parkedRows = (len(parked) + parkedCols - 1) / parkedCols
		for i, n := range parked {
			n.Position = model.Position{
				X: 80.0 + float64(i%parkedCols)*parkedGapX,
				Y: 60.0 + float64(i/parkedCols)*parkedGapY,
			}
		}
	}
	mainTop := 60.0
	if parkedRows > 0 {
		mainTop = 60.0 + float64(parkedRows)*parkedGapY + 80.0
	}

	for d := 0; d <= maxDepth; d++ {
		nodes := byDepth[d]
		sort.Slice(nodes, func(i, j int) bool {
			di, dj := dag.Indegree(nodes[i].ID), dag.Indegree(nodes[j].ID)
			if di != dj {
				return di < dj
			}
			return nodes[i].Title < nodes[j].Title
		})
		// Node cards render up to three title lines plus badges/proposed actions.
		// Keep enough vertical room so long Chinese titles never overlap.
		startY := mainTop + float64(maxCount-len(nodes))*90.0
		for i, n := range nodes {
			n.Position = model.Position{
				X: 80.0 + float64(d)*320.0,
				Y: startY + float64(i)*180.0,
			}
		}
	}
}

// layoutFull lays out the complete graph. External / collaborator nodes are
// forced into Column 0; mine nodes are layered after their blocking upstreams.
// out_of_scope nodes stay in a compact shelf below the flow so they never add
// phantom columns.
func layoutFull(dag *model.DAG, order []string) {
	isSource := func(n *model.Node) bool {
		return n.Scope == model.ScopeExternal || n.Scope == model.ScopeCollaborator
	}
	isRedline := func(n *model.Node) bool { return n.Scope == model.ScopeOutOfScope }

	depth := map[string]int{}
	inFlow := map[string]bool{}
	var redlines []*model.Node
	for _, n := range dag.Nodes {
		if isRedline(n) {
			redlines = append(redlines, n)
			continue
		}
		inFlow[n.ID] = true
	}
	pinned := map[string]bool{}
	for _, id := range order {
		n := dag.Node(id)
		if n == nil || !inFlow[id] {
			continue
		}
		if isSource(n) {
			depth[id] = 0
			pinned[id] = true
			continue
		}
		for _, e := range dag.InEdges(id) {
			if !model.BlockingEdge(e.Kind) || !inFlow[e.From] {
				continue
			}
			if depth[e.From]+1 > depth[id] {
				depth[id] = depth[e.From] + 1
			}
		}
	}
	rightAlignDepths(dag, order, inFlow, pinned, func(e *model.Edge) bool {
		return model.BlockingEdge(e.Kind)
	}, depth)

	byDepth := map[int][]*model.Node{}
	maxDepth, maxCount := 0, 1
	for _, n := range dag.Nodes {
		if !inFlow[n.ID] {
			continue
		}
		d := depth[n.ID]
		byDepth[d] = append(byDepth[d], n)
		if d > maxDepth {
			maxDepth = d
		}
		if len(byDepth[d]) > maxCount {
			maxCount = len(byDepth[d])
		}
	}
	scopeRank := func(n *model.Node) int {
		switch {
		case isSource(n):
			return 0
		case n.Status == model.StatusProposed:
			return 1
		default:
			return 2
		}
	}
	mainBottom := 0.0
	for d := 0; d <= maxDepth; d++ {
		nodes := byDepth[d]
		sort.Slice(nodes, func(i, j int) bool {
			ri, rj := scopeRank(nodes[i]), scopeRank(nodes[j])
			if ri != rj {
				return ri < rj
			}
			di, dj := dag.Indegree(nodes[i].ID), dag.Indegree(nodes[j].ID)
			if di != dj {
				return di < dj
			}
			return nodes[i].Title < nodes[j].Title
		})
		startY := 60.0 + float64(maxCount-len(nodes))*90.0
		for i, n := range nodes {
			n.Position = model.Position{
				X: 80.0 + float64(d)*320.0,
				Y: startY + float64(i)*180.0,
			}
			if n.Position.Y+180.0 > mainBottom {
				mainBottom = n.Position.Y + 180.0
			}
		}
	}
	if len(redlines) > 0 {
		sort.SliceStable(redlines, func(i, j int) bool { return redlines[i].Title < redlines[j].Title })
		const cols = 8
		const gapX = 280.0
		const gapY = 150.0
		for i, n := range redlines {
			n.Position = model.Position{
				X: 80.0 + float64(i%cols)*gapX,
				Y: mainBottom + 80.0 + float64(i/cols)*gapY,
			}
		}
	}
}

// ComputeCoverage calculates PRD coverage for non-heading blocks.
func ComputeCoverage(dag *model.DAG, doc *prd.Document) Coverage {
	c := Coverage{Unmapped: []string{}}
	if doc == nil {
		return c
	}
	refs := map[string]bool{}
	covered := map[string]bool{}
	outOfScope := map[string]bool{}
	for _, n := range dag.Nodes {
		for _, r := range n.PRDRefs {
			refs[r] = true
			if n.Scope == model.ScopeOutOfScope {
				outOfScope[r] = true
			} else {
				covered[r] = true
			}
		}
	}
	ignored := map[string]bool{}
	for _, r := range dag.IgnoredPRDRefs {
		ignored[r] = true
	}
	for _, b := range doc.Blocks {
		if b.Kind == prd.KindHeading {
			continue
		}
		c.Total++
		switch {
		case ignored[b.Anchor]:
			c.Ignored++
		case covered[b.Anchor]:
			c.Covered++
		case refs[b.Anchor] && outOfScope[b.Anchor]:
			c.Ignored++ // red-line-only nodes count as intentionally excluded
		default:
			c.Unmapped = append(c.Unmapped, b.Anchor)
		}
	}
	if c.Total > 0 {
		c.Percent = float64(c.Covered+c.Ignored) / float64(c.Total) * 100
	}
	return c
}

func isMilestoneNode(n *model.Node) bool {
	if n == nil {
		return false
	}
	for _, t := range n.Tags {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "milestone", "e2e":
			return true
		}
	}
	return false
}

// rightAlignDepths pulls connected terminal nodes (in > 0, out == 0) to the
// rightmost feature column and pins terminal milestone/e2e nodes to the far
// right, then propagates ALAP backwards so short chains converge naturally
// on the right instead of crowding Column 0/1. Truly isolated nodes stay left.
func rightAlignDepths(dag *model.DAG, order []string, active map[string]bool, pinned map[string]bool, edgeFilter func(*model.Edge) bool, depth map[string]int) {
	inCount := map[string]int{}
	outTargets := map[string][]string{}
	for _, e := range dag.Edges {
		if !active[e.From] || !active[e.To] {
			continue
		}
		if edgeFilter != nil && !edgeFilter(e) {
			continue
		}
		inCount[e.To]++
		outTargets[e.From] = append(outTargets[e.From], e.To)
	}
	featureMax := 0
	hasTerminalMilestone := false
	hasNonMilestoneAtMax := false
	for id := range active {
		if pinned != nil && pinned[id] {
			continue
		}
		if len(outTargets[id]) == 0 && isMilestoneNode(dag.Node(id)) {
			hasTerminalMilestone = true
			continue
		}
		if depth[id] > featureMax {
			featureMax = depth[id]
		}
	}
	for id := range active {
		if pinned != nil && pinned[id] {
			continue
		}
		if !(len(outTargets[id]) == 0 && isMilestoneNode(dag.Node(id))) && depth[id] == featureMax && (inCount[id] > 0 || len(outTargets[id]) > 0) {
			hasNonMilestoneAtMax = true
			break
		}
	}
	milestoneCol := featureMax
	if hasTerminalMilestone && hasNonMilestoneAtMax {
		milestoneCol = featureMax + 1
	}
	for id := range active {
		if pinned != nil && pinned[id] {
			continue
		}
		if len(outTargets[id]) == 0 && isMilestoneNode(dag.Node(id)) && depth[id] > milestoneCol {
			milestoneCol = depth[id]
		}
	}

	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		if !active[id] || (pinned != nil && pinned[id]) {
			continue
		}
		outs := outTargets[id]
		if len(outs) == 0 {
			if isMilestoneNode(dag.Node(id)) {
				if milestoneCol > depth[id] {
					depth[id] = milestoneCol
				}
			} else if inCount[id] > 0 {
				if featureMax > depth[id] {
					depth[id] = featureMax
				}
			}
			continue
		}
		minChild := -1
		for _, to := range outs {
			if minChild < 0 || depth[to] < minChild {
				minChild = depth[to]
			}
		}
		if minChild-1 > depth[id] {
			depth[id] = minChild - 1
		}
	}
}
