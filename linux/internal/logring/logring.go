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
	lines []string
	max   int
	part  []byte
}

func New(max int) *Ring { return &Ring{max: max} }

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
		r.lines = append(r.lines, Redact(string(r.part[:i])))
		r.part = r.part[i+1:]
	}
	if over := len(r.lines) - r.max; over > 0 {
		r.lines = append([]string(nil), r.lines[over:]...)
	}
	return len(p), nil
}

// Text returns the retained lines, oldest first.
func (r *Ring) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
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
