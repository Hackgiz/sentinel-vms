// Package logring keeps the last N lines the server logged, with secrets
// stripped, so a bug report can include recent activity.
package logring

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
)

type Ring struct {
	mu    sync.Mutex
	lines []string // circular once full; next is the oldest
	next  int
	max   int
	part  []byte
}

func New(max int) *Ring {
	if max < 1 {
		max = 1
	}
	return &Ring{max: max}
}

// Write implements io.Writer (tee it next to the normal log output).
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.part = append(r.part, p...)
	for {
		i := bytes.IndexByte(r.part, '\n')
		if i < 0 {
			break
		}
		r.add(Redact(string(r.part[:i])))
		r.part = r.part[i+1:]
	}
	if len(r.part) == 0 {
		r.part = nil // don't pin a large buffer
	}
	return len(p), nil
}

func (r *Ring) add(line string) {
	if len(r.lines) < r.max {
		r.lines = append(r.lines, line)
		return
	}
	r.lines[r.next] = line
	r.next = (r.next + 1) % r.max
}

// Text returns the retained lines, oldest first.
func (r *Ring) Text() string { return r.Tail(r.max) }

// Tail returns the last n retained lines, oldest first.
func (r *Ring) Tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := len(r.lines)
	if n > total {
		n = total
	}
	if n <= 0 {
		return ""
	}
	out := make([]string, 0, n)
	for i := total - n; i < total; i++ {
		out = append(out, r.lines[(r.next+i)%total])
	}
	return strings.Join(out, "\n")
}

var (
	urlCreds = regexp.MustCompile(`([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`)
	keyVals  = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|api[_-]?key|authorization)["']?\s*[=:]\s*["']?)[^\s"',&]+`)
	antKey   = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]+`)
	tunnel   = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
)

// Redact removes credentials in URLs, key=value secrets, API keys and the
// remote-access address from a log line.
func Redact(s string) string {
	s = urlCreds.ReplaceAllString(s, "${1}***:***@")
	s = keyVals.ReplaceAllString(s, "${1}***")
	s = antKey.ReplaceAllString(s, "sk-ant-***")
	s = tunnel.ReplaceAllString(s, "https://***.trycloudflare.com")
	return s
}
