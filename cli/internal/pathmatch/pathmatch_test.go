package pathmatch

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/*_mock.go", "order_mock.go", true},
		{"**/*_mock.go", "internal/service/order_mock.go", true},
		{"**/mock_*.go", "a/b/mock_order.go", true},
		{"go.sum", "go.sum", true},
		{"go.sum", "sub/go.sum", true},
		{"**/*.pb.go", "api/order.pb.go", true},
		{"**/gen/**", "gen/api/api.go", true},
		{"**/gen/**", "service/gen/api/api.go", true},
		{"*.go", "a/b.go", true},
		{"*.go", "a/b.ts", false},
		{"**/*_mock.go", "a/b.go", false},
		{"go.sum", "go.mod", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.path); got != c.want {
			t.Fatalf("Match(%q,%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestMatchAny(t *testing.T) {
	if !MatchAny([]string{"*.md", "**/*.pb.go"}, "x/y.pb.go") {
		t.Fatal("MatchAny should match second pattern")
	}
	if MatchAny([]string{"*.md"}, "x/y.go") {
		t.Fatal("MatchAny false positive")
	}
}
