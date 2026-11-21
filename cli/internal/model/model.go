// Package model defines the on-disk schema for a ReqBoard requirement workspace.
// Everything is plain JSON so humans, agents and the VS Code board can share it.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 1

// Node scopes.
const (
	ScopeMine         = "mine"
	ScopeCollaborator = "collaborator"
	ScopeExternal     = "external"
	ScopeTodo         = "todo"
	ScopeOutOfScope   = "out_of_scope"
)

// Node statuses.
const (
	StatusProposed         = "proposed"
	StatusPending          = "pending"
	StatusInProgress       = "in_progress"
	StatusMocked           = "mocked"
	StatusImplemented      = "implemented"
	StatusChangesRequested = "changes_requested"
	StatusReviewed         = "reviewed"
	StatusDone             = "done"
	StatusDeprecated       = "deprecated"
)

// Edge kinds.
const (
	EdgeHard       = "hard"
	EdgeMocked     = "mocked"
	EdgeStale      = "stale"
	EdgeDiscovered = "discovered"
)

// Binding roles.
const (
	RoleImpl = "impl"
	RoleMock = "mock"
	RoleTest = "test"
)

// Binding granularities.
const (
	GranularitySymbol        = "symbol"
	GranularityPartialSymbol = "partial_symbol"
	GranularityFile          = "file"
)

// Op kinds for the node-level lightweight checklist.
const (
	OpDDL     = "ddl"
	OpGateway = "gateway"
	OpFlag    = "flag"
	OpOther   = "other"
)

// Supplemental source kinds.
const (
	SourceDoc     = "doc"
	SourceMeeting = "meeting"
	SourceChat    = "chat"
	SourceSpec    = "spec"
)

// Node-level external reference kinds.
const (
	RefURL      = "url"
	RefMeeting  = "meeting"
	RefDecision = "decision"
	RefChat     = "chat"
)

// ValidSourceKind reports whether s is a known supplemental source kind.
func ValidSourceKind(s string) bool {
	switch s {
	case SourceDoc, SourceMeeting, SourceChat, SourceSpec:
		return true
	}
	return false
}

// ValidRefKind reports whether s is a known external reference kind.
func ValidRefKind(s string) bool {
	switch s {
	case RefURL, RefMeeting, RefDecision, RefChat:
		return true
	}
	return false
}

// DefaultIgnoreGlobs keeps generated code out of the diff noise.
var DefaultIgnoreGlobs = []string{
	"**/*_mock.go",
	"**/mock_*.go",
	"**/*_gen.go",
	"**/gen/**",
	"go.sum",
	"**/*.pb.go",
}

// DefaultTestGlobs matches the common unit-test file layouts across the
// languages ReqBoard supports out of the box.
var DefaultTestGlobs = []string{
	"**/*_test.go",
	"**/*.test.ts",
	"**/*.test.js",
	"**/*.spec.ts",
	"**/*.spec.js",
	"**/test_*.py",
	"**/*_test.py",
}

// Now returns an RFC3339 local timestamp used everywhere in the state files.
func Now() string { return time.Now().Format(time.RFC3339) }

// Config is .reqboard/config.json.
type Config struct {
	SchemaVersion   int      `json:"schema_version"`
	RequirementName string   `json:"requirement_name"`
	Root            string   `json:"root"`
	Repos           []Repo   `json:"repos"`
	IgnoreGlobs     []string `json:"ignore_globs"`
	IncludeGlobs    []string `json:"include_globs,omitempty"`
	ExcludeTests    *bool    `json:"exclude_tests,omitempty"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

// ShouldExcludeTests reports whether unit-test files are filtered out of the
// audit. It defaults to true so existing workspaces get the noise reduction
// without editing config.json.
func (c *Config) ShouldExcludeTests() bool {
	if c == nil || c.ExcludeTests == nil {
		return true
	}
	return *c.ExcludeTests
}

// Repo is one git repository participating in the requirement.
type Repo struct {
	Name    string `json:"name"`
	Path    string `json:"path"` // relative to the requirement root (or absolute)
	BaseRef string `json:"base_ref"`
}

// Position is the node coordinate on the DAG canvas.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ReviewNote is a human review comment. Open notes block done.
type ReviewNote struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Status     string `json:"status"` // open | resolved
	Target     string `json:"target,omitempty"`
	Comment    string `json:"comment"`
	CreatedAt  string `json:"created_at"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

// SelfMock records a mock debt owned by the node itself.
type SelfMock struct {
	ID         string `json:"id"`
	Desc       string `json:"desc"`
	CreatedAt  string `json:"created_at"`
	Resolved   bool   `json:"resolved"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

// Evidence stores completion evidence.
type Evidence struct {
	TraceIDs  []string `json:"trace_ids,omitempty"`
	TestCases []string `json:"test_cases,omitempty"`
	Extra     []string `json:"extra,omitempty"`
}

// OpItem is a lightweight delivery checklist item attached to a node. DDL,
// gateway configuration and feature-flag chores stay with the business node
// they unblock and never inflate the DAG node count.
type OpItem struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // ddl | gateway | flag | other
	Title string `json:"title"`
	Done  bool   `json:"done"`
	Note  string `json:"note,omitempty"`
}

// Source is one supplemental requirement source (meeting notes, chat decision,
// spec). The markdown body lives at .reqboard/sources/<id>.md and its anchors
// use the <id>:... prefix.
type Source struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"` // doc | meeting | chat | spec
	File      string `json:"file"` // sources/<id>.md
	URL       string `json:"url,omitempty"`
	AddedAt   string `json:"added_at"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ExternalRef is a node-level lightweight reference / decision excerpt. It is
// deliberately small: title + optional URL + optional verbatim quote.
type ExternalRef struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // url | meeting | decision | chat
	Title   string `json:"title"`
	URL     string `json:"url,omitempty"`
	Quote   string `json:"quote,omitempty"`
	AddedAt string `json:"added_at"`
}

// Node is one business function / rule in the DAG.
type Node struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Description  string        `json:"description,omitempty"`
	Scope        string        `json:"scope"`
	Status       string        `json:"status"`
	Stale        bool          `json:"stale"`
	StaleReason  string        `json:"stale_reason,omitempty"`
	PRDRefs      []string      `json:"prd_refs"`
	Refs         []ExternalRef `json:"refs,omitempty"`
	Tags         []string      `json:"tags,omitempty"`
	SelfMocks    []SelfMock    `json:"self_mocks"`
	ReviewNotes  []ReviewNote  `json:"review_notes"`
	Ops          []OpItem      `json:"ops,omitempty"`
	Evidence     Evidence      `json:"evidence"`
	SessionID    string        `json:"session_id,omitempty"`
	Hold         bool          `json:"hold"`
	Attempts     int           `json:"attempts"`
	LastError    string        `json:"last_error,omitempty"`
	HumanVersion int           `json:"human_version"`
	Position     Position      `json:"position"`
	UpdatedAt    string        `json:"updated_at"`
}

// Edge is a dependency / contract between nodes.
type Edge struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Kind      string `json:"kind"`
	Note      string `json:"note,omitempty"`
	UpdatedBy string `json:"updated_by"`
	UpdatedAt string `json:"updated_at"`
}

// Orchestrator is the long-running /goal coordinator state.
type Orchestrator struct {
	SessionID      string `json:"session_id,omitempty"`
	State          string `json:"state"` // running | quiescent
	MaxParallel    int    `json:"max_parallel"`
	LastDispatchAt string `json:"last_dispatch_at,omitempty"`
}

// DAG is .reqboard/dag.json.
type DAG struct {
	SchemaVersion   int          `json:"schema_version"`
	Revision        int          `json:"revision"`
	RequirementName string       `json:"requirement_name"`
	Nodes           []*Node      `json:"nodes"`
	Edges           []*Edge      `json:"edges"`
	Sources         []Source     `json:"sources,omitempty"`
	IgnoredPRDRefs  []string     `json:"ignored_prd_refs"`
	Orchestrator    Orchestrator `json:"orchestrator"`
}

// Lease is a soft, path-scoped claim used to avoid concurrent workers stepping on
// the same package.
type Lease struct {
	NodeID    string   `json:"node_id"`
	SessionID string   `json:"session_id"`
	Patterns  []string `json:"patterns"`
	ClaimedAt string   `json:"claimed_at"`
}

// Binding attributes a repo file/symbol to a node.
type Binding struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id"`
	Repo        string `json:"repo"`
	File        string `json:"file"`
	Granularity string `json:"granularity"`
	Symbol      string `json:"symbol"`
	AnchorHint  string `json:"anchor_hint,omitempty"`
	Role        string `json:"role"`
	Why         string `json:"why"`
	SessionID   string `json:"session_id,omitempty"`
	BoundAt     string `json:"bound_at"`
}

// Bindings is .reqboard/bindings.json.
type Bindings struct {
	SchemaVersion int       `json:"schema_version"`
	Leases        []Lease   `json:"leases"`
	Bindings      []Binding `json:"bindings"`
}

// NewID builds a deterministic short id (rb-xxxx / ed-xxxx / bd-xxxx).
// It extends the hash length when the short form collides.
func NewID(prefix string, used map[string]bool, seeds ...string) string {
	h := sha256.Sum256([]byte(strings.Join(seeds, "\x00")))
	full := hex.EncodeToString(h[:])
	for n := 4; n <= 12; n++ {
		id := prefix + "-" + full[:n]
		if used == nil || !used[id] {
			return id
		}
	}
	return prefix + "-" + full
}

// NewSequentialID builds bd-01 style ids used by the reference schema.
func NewSequentialID(prefix string, used map[string]bool) string {
	for i := 1; i < 10000; i++ {
		id := fmt.Sprintf("%s-%02d", prefix, i)
		if used == nil || !used[id] {
			return id
		}
	}
	return NewID(prefix, used, fmt.Sprintf("%d", time.Now().UnixNano()))
}

// ValidScope reports whether s is a known scope.
func ValidScope(s string) bool {
	switch s {
	case ScopeMine, ScopeCollaborator, ScopeExternal, ScopeTodo, ScopeOutOfScope:
		return true
	}
	return false
}

// IsPrerequisiteScope reports whether the scope is a first-class prerequisite
// that lives outside the business implementation tree: external parties,
// collaborators and engineering TODOs (DDL / gateway config / feature flag).
// Prerequisite nodes never carry our code bindings; they only block downstream
// completion (and optionally startup via the `block-start` tag).
func IsPrerequisiteScope(scope string) bool {
	switch scope {
	case ScopeExternal, ScopeCollaborator, ScopeTodo:
		return true
	}
	return false
}

// HasTag reports whether the node carries the tag (case-insensitive, trimmed).
func (n *Node) HasTag(tag string) bool {
	if n == nil {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(tag))
	for _, t := range n.Tags {
		if strings.ToLower(strings.TrimSpace(t)) == want {
			return true
		}
	}
	return false
}

// TodoKind returns the TODO kind tag (ddl|gateway|flag|other), defaulting to
// "other" when the node carries none of the known kind tags.
func (n *Node) TodoKind() string {
	if n == nil {
		return OpOther
	}
	for _, kind := range []string{OpDDL, OpGateway, OpFlag} {
		if n.HasTag(kind) {
			return kind
		}
	}
	return OpOther
}

// ValidStatus reports whether s is a known node status.
func ValidStatus(s string) bool {
	switch s {
	case StatusProposed, StatusPending, StatusInProgress, StatusMocked,
		StatusImplemented, StatusChangesRequested, StatusReviewed,
		StatusDone, StatusDeprecated:
		return true
	}
	return false
}

// ValidEdgeKind reports whether s is a known edge kind.
func ValidEdgeKind(s string) bool {
	switch s {
	case EdgeHard, EdgeMocked, EdgeStale, EdgeDiscovered:
		return true
	}
	return false
}

// ValidRole reports whether s is a known binding role.
func ValidRole(s string) bool {
	switch s {
	case RoleImpl, RoleMock, RoleTest:
		return true
	}
	return false
}

// ValidOpKind reports whether s is a known node op kind.
func ValidOpKind(s string) bool {
	switch s {
	case OpDDL, OpGateway, OpFlag, OpOther:
		return true
	}
	return false
}

// BlockingEdge reports whether the edge participates in done gating.
func BlockingEdge(kind string) bool {
	return kind == EdgeHard || kind == EdgeMocked || kind == EdgeStale
}

// Active reports whether the node still owns live code (deprecated nodes keep
// historical bindings for debris detection but are not "active").
func (n *Node) Active() bool { return n.Status != StatusDeprecated }

// OpenNotes returns unresolved review notes.
func (n *Node) OpenNotes() []ReviewNote {
	var out []ReviewNote
	for _, r := range n.ReviewNotes {
		if r.Status == "open" {
			out = append(out, r)
		}
	}
	return out
}

// PendingMocks returns unresolved self mocks.
func (n *Node) PendingMocks() []SelfMock {
	var out []SelfMock
	for _, m := range n.SelfMocks {
		if !m.Resolved {
			out = append(out, m)
		}
	}
	return out
}

// MockDebt counts unresolved self mocks.
func (n *Node) MockDebt() int { return len(n.PendingMocks()) }

// SetStale marks the node stale. It returns true when the state changed.
func (n *Node) SetStale(reason string) bool {
	changed := !n.Stale || (reason != "" && n.StaleReason != reason)
	n.Stale = true
	if reason != "" {
		n.StaleReason = reason
	}
	return changed
}

// Node returns a node by id or nil.
func (d *DAG) Node(id string) *Node {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// Source returns a supplemental source by id or nil.
func (d *DAG) Source(id string) *Source {
	for i := range d.Sources {
		if d.Sources[i].ID == id {
			return &d.Sources[i]
		}
	}
	return nil
}

// SourceIDSet returns the set of registered supplemental source ids.
func (d *DAG) SourceIDSet() map[string]bool {
	m := make(map[string]bool, len(d.Sources))
	for _, s := range d.Sources {
		m[s.ID] = true
	}
	return m
}

// Ref returns a node-level external reference by id or nil.
func (n *Node) Ref(id string) *ExternalRef {
	if n == nil {
		return nil
	}
	for i := range n.Refs {
		if n.Refs[i].ID == id {
			return &n.Refs[i]
		}
	}
	return nil
}

// Edge returns an edge by id or nil.
func (d *DAG) Edge(id string) *Edge {
	for _, e := range d.Edges {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// NodeIDSet returns the set of node ids.
func (d *DAG) NodeIDSet() map[string]bool {
	m := make(map[string]bool, len(d.Nodes))
	for _, n := range d.Nodes {
		m[n.ID] = true
	}
	return m
}

// EdgeIDSet returns the set of edge ids.
func (d *DAG) EdgeIDSet() map[string]bool {
	m := make(map[string]bool, len(d.Edges))
	for _, e := range d.Edges {
		m[e.ID] = true
	}
	return m
}

// InEdges returns all incoming edges of a node in declaration order.
func (d *DAG) InEdges(id string) []*Edge {
	var out []*Edge
	for _, e := range d.Edges {
		if e.To == id {
			out = append(out, e)
		}
	}
	return out
}

// OutEdges returns all outgoing edges of a node in declaration order.
func (d *DAG) OutEdges(id string) []*Edge {
	var out []*Edge
	for _, e := range d.Edges {
		if e.From == id {
			out = append(out, e)
		}
	}
	return out
}

// Indegree counts blocking in-edges (discovered edges are provenance only).
func (d *DAG) Indegree(id string) int {
	n := 0
	for _, e := range d.InEdges(id) {
		if BlockingEdge(e.Kind) {
			n++
		}
	}
	return n
}

// Outdegree counts blocking out-edges.
func (d *DAG) Outdegree(id string) int {
	n := 0
	for _, e := range d.OutEdges(id) {
		if BlockingEdge(e.Kind) {
			n++
		}
	}
	return n
}

// Downstream returns every transitive downstream node id (excluding start),
// in deterministic BFS order.
func (d *DAG) Downstream(start string) []string {
	seen := map[string]bool{start: true}
	queue := []string{start}
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range d.Edges {
			if e.From != cur || seen[e.To] {
				continue
			}
			seen[e.To] = true
			out = append(out, e.To)
			queue = append(queue, e.To)
		}
	}
	return out
}

// TopoSort returns a deterministic Kahn topological order.
func (d *DAG) TopoSort() ([]string, error) {
	indeg := map[string]int{}
	ids := make([]string, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		indeg[n.ID] = 0
		ids = append(ids, n.ID)
	}
	for _, e := range d.Edges {
		if _, ok := indeg[e.From]; !ok {
			return nil, fmt.Errorf("edge %s references unknown from node %q", e.ID, e.From)
		}
		if _, ok := indeg[e.To]; !ok {
			return nil, fmt.Errorf("edge %s references unknown to node %q", e.ID, e.To)
		}
		indeg[e.To]++
	}
	sort.Strings(ids)
	var ready []string
	for _, id := range ids {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	var out []string
	for len(ready) > 0 {
		sort.Strings(ready)
		cur := ready[0]
		ready = ready[1:]
		out = append(out, cur)
		for _, e := range d.Edges {
			if e.From != cur {
				continue
			}
			indeg[e.To]--
			if indeg[e.To] == 0 {
				ready = append(ready, e.To)
			}
		}
	}
	if len(out) != len(d.Nodes) {
		var left []string
		for _, id := range ids {
			if indeg[id] > 0 {
				left = append(left, id)
			}
		}
		return out, fmt.Errorf("cycle detected among nodes: %s", strings.Join(left, ", "))
	}
	return out, nil
}

// AddNode appends a node.
func (d *DAG) AddNode(n *Node) { d.Nodes = append(d.Nodes, n) }

// AddEdge appends an edge.
func (d *DAG) AddEdge(e *Edge) { d.Edges = append(d.Edges, e) }

// RemoveEdge removes an edge by id and reports whether it existed.
func (d *DAG) RemoveEdge(id string) bool {
	for i, e := range d.Edges {
		if e.ID == id {
			d.Edges = append(d.Edges[:i], d.Edges[i+1:]...)
			return true
		}
	}
	return false
}

// Touch bumps the DAG revision.
func (d *DAG) Touch() { d.Revision++ }

// NewDAG builds an empty DAG for a requirement.
func NewDAG(name string) *DAG {
	return &DAG{
		SchemaVersion:   SchemaVersion,
		Revision:        1,
		RequirementName: name,
		Nodes:           []*Node{},
		Edges:           []*Edge{},
		IgnoredPRDRefs:  []string{},
		Orchestrator:    Orchestrator{State: "quiescent", MaxParallel: 4},
	}
}

// NewBindings builds an empty bindings document.
func NewBindings() *Bindings {
	return &Bindings{SchemaVersion: SchemaVersion, Leases: []Lease{}, Bindings: []Binding{}}
}

// FindBinding returns a binding by id or nil.
func (b *Bindings) FindBinding(id string) *Binding {
	for i := range b.Bindings {
		if b.Bindings[i].ID == id {
			return &b.Bindings[i]
		}
	}
	return nil
}

// BindingsForNode returns bindings belonging to a node.
func (b *Bindings) BindingsForNode(nodeID string) []Binding {
	var out []Binding
	for _, x := range b.Bindings {
		if x.NodeID == nodeID {
			out = append(out, x)
		}
	}
	return out
}

// LeaseFor returns the lease of a node, if any.
func (b *Bindings) LeaseFor(nodeID string) *Lease {
	for i := range b.Leases {
		if b.Leases[i].NodeID == nodeID {
			return &b.Leases[i]
		}
	}
	return nil
}

// RemoveLeases drops all leases for a node.
func (b *Bindings) RemoveLeases(nodeID string) {
	var out []Lease
	for _, l := range b.Leases {
		if l.NodeID != nodeID {
			out = append(out, l)
		}
	}
	b.Leases = out
}
