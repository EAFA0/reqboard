// Package sched implements the deterministic Goal Orchestrator dispatcher.
package sched

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"reqboard/internal/audit"
	"reqboard/internal/gate"
	"reqboard/internal/model"
)

// Item is one dispatchable work unit.
type Item struct {
	Priority    int      `json:"priority"`
	Kind        string   `json:"kind"`
	NodeID      string   `json:"node_id"`
	Title       string   `json:"title"`
	Reason      string   `json:"reason"`
	Command     string   `json:"command"`
	Instruction string   `json:"instruction,omitempty"`
	LeaseKeys   []string `json:"lease_keys,omitempty"`
	Deferred    bool     `json:"deferred,omitempty"`
	DeferReason string   `json:"defer_reason,omitempty"`
}

// Blocker is one classified blocked node.
type Blocker struct {
	NodeID string `json:"node_id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Blocked is the QUIESCENT bill of materials.
type Blocked struct {
	WaitingApproval     []Blocker `json:"waiting_approval"`
	WaitingReview       []Blocker `json:"waiting_review"`
	WaitingExternal     []Blocker `json:"waiting_external"`
	WaitingVerification []Blocker `json:"waiting_verification"`
	CircuitBroken       []Blocker `json:"circuit_broken"`
	OutOfScope          []Blocker `json:"out_of_scope"`
}

// ActiveWorker is an in-flight node.
type ActiveWorker struct {
	NodeID    string `json:"node_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
}

// Plan is the dispatch result.
type Plan struct {
	Status      string         `json:"status"` // READY | RUNNING | QUIESCENT
	Items       []Item         `json:"items"`
	Deferred    []Item         `json:"deferred"`
	Active      []ActiveWorker `json:"active"`
	Blocked     Blocked        `json:"blocked"`
	MaxParallel int            `json:"max_parallel"`
	SessionID   string         `json:"session_id,omitempty"`
}

// Options controls dispatch.
type Options struct {
	MaxParallel int
	SessionID   string
}

// Compute builds the deterministic dispatch plan. It never writes state.
func Compute(dag *model.DAG, bindings *model.Bindings, rep *audit.Report, opts Options) *Plan {
	if rep == nil {
		rep = &audit.Report{ToDelete: []audit.Entry{}}
	}
	maxParallel := opts.MaxParallel
	if maxParallel <= 0 {
		maxParallel = dag.Orchestrator.MaxParallel
	}
	if maxParallel <= 0 {
		maxParallel = 4
	}
	p := &Plan{
		Items:       []Item{},
		Deferred:    []Item{},
		Active:      []ActiveWorker{},
		MaxParallel: maxParallel,
		SessionID:   opts.SessionID,
		Blocked: Blocked{
			WaitingApproval:     []Blocker{},
			WaitingReview:       []Blocker{},
			WaitingExternal:     []Blocker{},
			WaitingVerification: []Blocker{},
			CircuitBroken:       []Blocker{},
			OutOfScope:          []Blocker{},
		},
	}

	var candidates []Item
	seen := map[string]bool{}
	hold := map[string]bool{}
	for _, n := range dag.Nodes {
		hold[n.ID] = n.Hold
	}

	addCandidate := func(it Item) {
		if seen[it.NodeID] {
			return
		}
		seen[it.NodeID] = true
		candidates = append(candidates, it)
	}

	// P0: debris cleanup, review fixes, stale refreshes.
	debris := map[string][]string{}
	for _, e := range rep.ToDelete {
		for _, id := range e.NodeIDs {
			debris[id] = appendUnique(debris[id], fmt.Sprintf("%s:%s::%s", e.Repo, e.File, e.Symbol))
		}
	}
	var deprecatedIDs []string
	for id := range debris {
		deprecatedIDs = append(deprecatedIDs, id)
	}
	sort.Strings(deprecatedIDs)
	for _, id := range deprecatedIDs {
		n := dag.Node(id)
		if n == nil {
			continue
		}
		addCandidate(Item{
			Priority: 0, Kind: "cleanup_debris", NodeID: id, Title: n.Title,
			Reason:      "deprecated 节点仍有残留改动：" + strings.Join(debris[id], ", "),
			Command:     "reqboard check --all",
			Instruction: fmt.Sprintf("删除上述残留代码后运行 `reqboard unbind <binding-id>`，直到 `reqboard check --all` 不再报 To-Delete（节点 %s 已 deprecated，不可 claim）", id),
		})
	}
	for _, n := range dag.Nodes {
		if n.Status == model.StatusDeprecated || n.Status == model.StatusDone || n.Status == model.StatusProposed || hold[n.ID] {
			continue
		}
		if n.Status == model.StatusInProgress || n.Status == model.StatusMocked {
			continue // already has an active worker
		}
		if n.Attempts >= 2 {
			continue
		}
		switch {
		case n.Status == model.StatusChangesRequested:
			addCandidate(Item{
				Priority: 0, Kind: "fix_review", NodeID: n.ID, Title: n.Title,
				Reason:  fmt.Sprintf("人类 Review 打回，存在 %d 条批注", len(n.OpenNotes())),
				Command: "reqboard claim " + n.ID,
			})
		case n.Stale:
			reason := n.StaleReason
			if reason == "" {
				reason = "PRD 或上游方案变更"
			}
			addCandidate(Item{
				Priority: 0, Kind: "refresh_stale", NodeID: n.ID, Title: n.Title,
				Reason:  "stale：" + reason,
				Command: "reqboard claim " + n.ID,
			})
		}
	}

	// P1: parallel implementation, left-first (low indegree, high fan-out).
	var p1 []*model.Node
	for _, n := range dag.Nodes {
		if n.Scope != model.ScopeMine || n.Hold || n.Attempts >= 2 || n.Stale || isVerificationNode(n) {
			continue
		}
		if n.Status != model.StatusPending && n.Status != model.StatusMocked {
			continue
		}
		if n.Status == model.StatusMocked && n.SessionID != "" {
			continue // already has an active worker
		}
		// External hard dependencies gate the start of work: waiting for the
		// real counterpart is cheaper than writing throwaway integration code.
		// A mocked edge is an explicit decision to parallelize on a stub, so it
		// stays dispatchable (done gates still apply).
		if n.Status == model.StatusPending && externalHardBlocker(dag, n.ID) != "" {
			continue
		}
		p1 = append(p1, n)
	}
	sort.SliceStable(p1, func(i, j int) bool {
		di, dj := dag.Indegree(p1[i].ID), dag.Indegree(p1[j].ID)
		if di != dj {
			return di < dj
		}
		oi, oj := dag.Outdegree(p1[i].ID), dag.Outdegree(p1[j].ID)
		if oi != oj {
			return oi > oj
		}
		if p1[i].Position.X != p1[j].Position.X {
			return p1[i].Position.X < p1[j].Position.X
		}
		return p1[i].Title < p1[j].Title
	})
	for _, n := range p1 {
		kind, reason := "implement", fmt.Sprintf("mine/pending 且未 hold（入度 %d，扇出 %d）", dag.Indegree(n.ID), dag.Outdegree(n.ID))
		if n.Status == model.StatusMocked {
			kind = "resolve_mocks"
			reason = fmt.Sprintf("存在 %d 条未销 self_mocks，需拆 Mock 后完结", len(n.PendingMocks()))
		}
		addCandidate(Item{
			Priority: 1, Kind: kind, NodeID: n.ID, Title: n.Title,
			Reason:  reason,
			Command: "reqboard claim " + n.ID,
		})
	}

	// P2: fast review and auto-done.
	for _, n := range dag.Nodes {
		if n.Hold || n.Attempts >= 2 || n.Status == model.StatusDeprecated || n.Status == model.StatusDone || n.Status == model.StatusProposed {
			continue
		}
		if n.Status == model.StatusImplemented && len(n.OpenNotes()) == 0 && !n.Stale {
			addCandidate(Item{
				Priority: 2, Kind: "fast_review", NodeID: n.ID, Title: n.Title,
				Reason:  "implemented 且无 open review_notes，可跑反膨胀快审",
				Command: "reqboard review-pack " + n.ID,
			})
			continue
		}
		if n.Status == model.StatusReviewed && !n.Stale {
			vs := gate.Evaluate(dag, bindings, rep, n.ID)
			if gate.AllPassed(vs) {
				addCandidate(Item{
					Priority: 2, Kind: "auto_done", NodeID: n.ID, Title: n.Title,
					Reason:  "reviewed 且 4 道完结门禁全部通过",
					Command: "reqboard done " + n.ID,
				})
			}
		}
	}

	// Lease-aware selection.
	activeKeys := map[string]string{}
	for _, l := range bindings.Leases {
		if ln := dag.Node(l.NodeID); ln != nil && (ln.Status == model.StatusDone || ln.Status == model.StatusDeprecated) {
			continue
		}
		for _, ptn := range l.Patterns {
			repo, dir := patternKey(ptn)
			activeKeys[repo+"|"+dir] = l.NodeID
		}
	}
	selected := map[string]bool{}
	selectedKeys := map[string]string{}
	for _, it := range candidates {
		n := dag.Node(it.NodeID)
		keys := packageKeys(n, bindings)
		it.LeaseKeys = keys
		conflict := ""
		for _, k := range keys {
			for ak, owner := range activeKeys {
				if owner != it.NodeID && keysConflict(k, ak) {
					conflict = fmt.Sprintf("与活跃租约冲突：%s 由 %s 持有", k, owner)
					break
				}
			}
			if conflict != "" {
				break
			}
			for sk, owner := range selectedKeys {
				if keysConflict(k, sk) {
					conflict = fmt.Sprintf("与本次已选节点 %s 落在同一包 %s，串行到下一波", owner, k)
					break
				}
			}
			if conflict != "" {
				break
			}
		}
		if conflict != "" {
			it.Deferred = true
			it.DeferReason = conflict
			p.Deferred = append(p.Deferred, it)
			continue
		}
		if len(p.Items) >= maxParallel {
			it.Deferred = true
			it.DeferReason = fmt.Sprintf("超出 --max-parallel=%d，排入下一波", maxParallel)
			p.Deferred = append(p.Deferred, it)
			continue
		}
		selected[it.NodeID] = true
		for _, k := range keys {
			selectedKeys[k] = it.NodeID
		}
		p.Items = append(p.Items, it)
	}

	// Active workers.
	for _, n := range dag.Nodes {
		if n.Status == model.StatusInProgress || n.Status == model.StatusMocked {
			p.Active = append(p.Active, ActiveWorker{NodeID: n.ID, Title: n.Title, Status: n.Status, SessionID: n.SessionID})
		}
	}
	sort.SliceStable(p.Active, func(i, j int) bool { return p.Active[i].NodeID < p.Active[j].NodeID })

	// Blocked bill of materials.
	blockedBy := map[string]string{}
	addBlock := func(cat string, n *model.Node, reason string) {
		if _, ok := blockedBy[n.ID]; ok {
			return
		}
		blockedBy[n.ID] = cat
		b := Blocker{NodeID: n.ID, Title: n.Title, Reason: reason}
		switch cat {
		case "approval":
			p.Blocked.WaitingApproval = append(p.Blocked.WaitingApproval, b)
		case "external":
			p.Blocked.WaitingExternal = append(p.Blocked.WaitingExternal, b)
		case "circuit":
			p.Blocked.CircuitBroken = append(p.Blocked.CircuitBroken, b)
		case "verification":
			p.Blocked.WaitingVerification = append(p.Blocked.WaitingVerification, b)
		case "out_of_scope":
			p.Blocked.OutOfScope = append(p.Blocked.OutOfScope, b)
		default:
			p.Blocked.WaitingReview = append(p.Blocked.WaitingReview, b)
		}
	}
	for _, n := range dag.Nodes {
		if n.Status == model.StatusDone || n.Status == model.StatusDeprecated {
			continue
		}
		if n.Status == model.StatusProposed {
			addBlock("approval", n, "proposed 节点待 owner 在看板批准")
			continue
		}
		if n.Attempts >= 2 {
			addBlock("circuit", n, fmt.Sprintf("attempts=%d 已熔断：%s", n.Attempts, n.LastError))
			continue
		}
		if n.Scope == model.ScopeOutOfScope {
			addBlock("out_of_scope", n, "红线：PRD 明确本期不做，禁止派发/脑补")
			continue
		}
		if n.Hold {
			addBlock("review", n, "人工 hold 暂停自动推进")
			continue
		}
		if n.Scope == model.ScopeExternal || n.Scope == model.ScopeCollaborator {
			addBlock("external", n, fmt.Sprintf("scope=%s，需外部/协作者推进", n.Scope))
			continue
		}
		if n.Status == model.StatusInProgress || n.Status == model.StatusMocked {
			continue // active
		}
		if selected[n.ID] || isDeferred(p.Deferred, n.ID) {
			continue
		}
		if n.Scope == model.ScopeMine && n.Status == model.StatusPending {
			if reason := externalHardBlocker(dag, n.ID); reason != "" {
				addBlock("external", n, reason)
				continue
			}
		}
		if isVerificationNode(n) {
			addBlock("verification", n, "端到端/验收/范围节点，待前置功能完成后由 owner/QA 验收")
			continue
		}
		if n.Status == model.StatusImplemented || n.Status == model.StatusReviewed {
			vs := gate.Evaluate(dag, bindings, rep, n.ID)
			if len(vs) == 0 {
				continue
			}
			cat := "review"
			reasons := []string{}
			for _, v := range vs {
				reasons = append(reasons, v.Message)
				if v.Code == "mocked_edge" || v.Code == "stale_edge" || v.Code == "upstream_missing" {
					cat = "external"
				}
				if v.Code == "upstream_not_done" {
					up := dag.Node(edgeFrom(dag, n.ID))
					if up != nil && (up.Scope == model.ScopeExternal || up.Scope == model.ScopeCollaborator) {
						cat = "external"
					}
				}
			}
			addBlock(cat, n, strings.Join(reasons, "；"))
			continue
		}
		if n.Status == model.StatusPending || n.Status == model.StatusChangesRequested {
			addBlock("review", n, "无合法可派发项（检查 hold/租约/依赖）")
		}
	}
	sortBlockers(p.Blocked.WaitingApproval)
	sortBlockers(p.Blocked.WaitingReview)
	sortBlockers(p.Blocked.WaitingExternal)
	sortBlockers(p.Blocked.WaitingVerification)
	sortBlockers(p.Blocked.CircuitBroken)
	sortBlockers(p.Blocked.OutOfScope)

	switch {
	case len(p.Items) > 0:
		p.Status = "READY"
	case len(p.Active) > 0 || len(p.Deferred) > 0:
		p.Status = "RUNNING"
	default:
		p.Status = "QUIESCENT"
	}
	return p
}

// isVerificationNode reports whether a node is a scope baseline, end-to-end
// flow or acceptance milestone. Those are reviewed by the owner/QA after the
// implementation nodes land; dispatching them as code tasks wastes workers.
func isVerificationNode(n *model.Node) bool {
	for _, t := range n.Tags {
		switch t {
		case "scope", "e2e", "milestone":
			return true
		}
	}
	return false
}

// externalHardBlocker explains why a mine node cannot start yet: it has a hard
// in-edge from an external/collaborator node that is not done. Mocked edges are
// deliberately excluded — they are the "develop in parallel behind a stub"
// escape hatch. Internal mine -> mine edges never block the start either; they
// only block completion.
func externalHardBlocker(dag *model.DAG, nodeID string) string {
	var reasons []string
	for _, e := range dag.InEdges(nodeID) {
		if e.Kind != model.EdgeHard {
			continue
		}
		from := dag.Node(e.From)
		if from == nil {
			continue
		}
		if from.Scope != model.ScopeExternal && from.Scope != model.ScopeCollaborator {
			continue
		}
		if from.Status == model.StatusDone || from.Status == model.StatusDeprecated {
			continue
		}
		reasons = append(reasons, fmt.Sprintf(
			"等待外部节点 %s (%s) 标记 done，或将该边改为 mocked 后再并行开发",
			from.ID, from.Title,
		))
	}
	return strings.Join(reasons, "；")
}

func isDeferred(def []Item, id string) bool {
	for _, d := range def {
		if d.NodeID == id {
			return true
		}
	}
	return false
}

func edgeFrom(dag *model.DAG, to string) string {
	for _, e := range dag.InEdges(to) {
		if model.BlockingEdge(e.Kind) {
			return e.From
		}
	}
	return ""
}

func sortBlockers(bs []Blocker) {
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].NodeID < bs[j].NodeID })
}

func appendUnique(in []string, s string) []string {
	for _, x := range in {
		if x == s {
			return in
		}
	}
	return append(in, s)
}

// packageKeys returns repo|package keys used for mutual exclusion.
func packageKeys(n *model.Node, bindings *model.Bindings) []string {
	set := map[string]bool{}
	for _, b := range bindings.BindingsForNode(n.ID) {
		dir := path.Dir(strings.TrimPrefix(b.File, "./"))
		if dir == "." {
			dir = ""
		}
		set[b.Repo+"|"+dir] = true
	}
	if l := bindings.LeaseFor(n.ID); l != nil {
		for _, ptn := range l.Patterns {
			repo, dir := patternKey(ptn)
			set[repo+"|"+dir] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysConflict(a, b string) bool {
	ar, ad := splitKey(a)
	br, bd := splitKey(b)
	if ar != "*" && br != "*" && ar != br {
		return false
	}
	if ad == bd {
		return true
	}
	// A broad lease such as `repo:src/**` must also exclude nested packages.
	if ad != "" && strings.HasPrefix(bd, ad+"/") {
		return true
	}
	if bd != "" && strings.HasPrefix(ad, bd+"/") {
		return true
	}
	return false
}

func splitKey(k string) (string, string) {
	parts := strings.SplitN(k, "|", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

func patternKey(pattern string) (string, string) {
	pattern = strings.TrimSpace(pattern)
	parts := strings.SplitN(pattern, ":", 2)
	repo, p := "*", pattern
	if len(parts) == 2 {
		repo, p = parts[0], parts[1]
	}
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		prefix := p[:i]
		if j := strings.LastIndex(prefix, "/"); j >= 0 {
			p = prefix[:j]
		} else {
			p = ""
		}
	} else {
		p = path.Dir(p)
		if p == "." {
			p = ""
		}
	}
	return repo, strings.Trim(p, "/")
}
