package rules

import "strings"

// Kind aliases (WAVE8-DETECT-EVIDENCE.md, Stream A). The contract names the
// line-crossing kind "linecross"; the alarm kind our own tracker and the
// camera analytics both raise is "Line crossing" (rules.Kinds). A rule may
// use either spelling: Normalize rewrites aliases to the canonical kind and
// Matches compares canonical forms.
var kindAliases = map[string]string{
	"linecross":    "Line crossing",
	"linecrossing": "Line crossing",
}

// CanonicalKind maps a kind alias to the kind alarms carry; anything else is
// returned unchanged.
func CanonicalKind(k string) string {
	key := strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.TrimSpace(k)))
	if c, ok := kindAliases[key]; ok {
		return c
	}
	return k
}

// kindListed reports whether kind is in list, comparing canonical forms and
// ignoring case (so a rule saved with an alias still matches).
func kindListed(list []string, kind string) bool {
	want := CanonicalKind(kind)
	for _, k := range list {
		if strings.EqualFold(CanonicalKind(k), want) {
			return true
		}
	}
	return false
}
