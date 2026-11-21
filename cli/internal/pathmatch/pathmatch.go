// Package pathmatch implements the small glob subset used by ignore_globs.
package pathmatch

import (
	"regexp"
	"strings"
	"sync"
)

var cache sync.Map // pattern -> *regexp.Regexp

// Match reports whether path (slash-separated) matches pattern.
// `**` crosses directories, `*` and `?` do not.
func Match(pattern, path string) bool {
	path = strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		// Basename-style pattern matches at any depth, like .gitignore.
		base := path
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		return matchRegex(pattern, base)
	}
	return matchRegex(pattern, path)
}

func matchRegex(pattern, s string) bool {
	if v, ok := cache.Load(pattern); ok {
		return v.(*regexp.Regexp).MatchString(s)
	}
	re := regexp.MustCompile("^" + translate(pattern) + "$")
	cache.Store(pattern, re)
	return re.MatchString(s)
}

func translate(pattern string) string {
	var sb strings.Builder
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				// **/ matches zero or more directories.
				if i+2 < len(runes) && runes[i+2] == '/' {
					sb.WriteString(`(?:.*/)?`)
					i += 2
				} else {
					sb.WriteString(`.*`)
					i++
				}
			} else {
				sb.WriteString(`[^/]*`)
			}
		case '?':
			sb.WriteString(`[^/]`)
		case '.', '+', '(', ')', '|', '[', ']', '{', '}', '^', '$', '\\':
			sb.WriteString(`\`)
			sb.WriteRune(r)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// MatchAny reports whether path matches any pattern.
func MatchAny(patterns []string, path string) bool {
	for _, p := range patterns {
		if Match(p, path) {
			return true
		}
	}
	return false
}
