package server

import (
	"bufio"
	"bytes"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sentinel-linux/api"
)

var (
	specPathLine = regexp.MustCompile(`^  (/api/v2/\S*):\s*$`)
	specOpLine   = regexp.MustCompile(`^    (get|put|post|patch|delete):\s*$`)
	specTagsLine = regexp.MustCompile(`^      tags: \[(\w[\w-]*)\]`)
	pathParam    = regexp.MustCompile(`\{[^}]+\}`)
)

// specOps reads "METHOD /path" → tag from api/openapi.yaml (path
// parameters normalized to {}). The file's layout is regular enough for a
// line scan.
func specOps(t *testing.T) map[string]string {
	t.Helper()
	ops := map[string]string{}
	var path, op string
	sc := bufio.NewScanner(bytes.NewReader(api.OpenAPI))
	for sc.Scan() {
		line := sc.Text()
		if m := specPathLine.FindStringSubmatch(line); m != nil {
			path, op = pathParam.ReplaceAllString(m[1], "{}"), ""
			continue
		}
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "components:") {
			if line != "" && !strings.HasPrefix(line, " ") {
				path = ""
			}
		}
		if path == "" {
			continue
		}
		if m := specOpLine.FindStringSubmatch(line); m != nil {
			op = strings.ToUpper(m[1]) + " " + path
			ops[op] = ""
			continue
		}
		if m := specTagsLine.FindStringSubmatch(line); m != nil && op != "" {
			ops[op] = m[1]
		}
	}
	if len(ops) == 0 {
		t.Fatal("no operations found in api/openapi.yaml")
	}
	for op, tag := range ops {
		if tag == "" {
			t.Errorf("%s has no tags line", op)
		}
	}
	return ops
}

// pendingTags are spec tags still being built (phases 2–3): their
// operations may be missing. Set SPEC_STRICT_TAGS=groups,audit to check
// some of them strictly anyway. Emptied as the work lands.
var pendingTags = map[string]bool{}

// TestOpenAPIMatchesRoutes: every v2 route the server registers is in the
// spec, and every spec operation is served (except pendingTags).
func TestOpenAPIMatchesRoutes(t *testing.T) {
	s := newCompanionServer(t)
	_ = s.Handler()
	spec := specOps(t)
	routes := map[string]bool{}
	for r := range v2Routes {
		routes[pathParam.ReplaceAllString(r, "{}")] = true
	}
	for r := range routes {
		if _, ok := spec[r]; !ok {
			t.Errorf("route %s is not in api/openapi.yaml", r)
		}
	}
	strict := map[string]bool{}
	for _, t := range strings.Split(os.Getenv("SPEC_STRICT_TAGS"), ",") {
		strict[strings.TrimSpace(t)] = true
	}
	var missing []string
	for op, tag := range spec {
		if pendingTags[tag] && !strict[tag] {
			continue
		}
		if !routes[op] {
			missing = append(missing, op)
		}
	}
	sort.Strings(missing)
	for _, op := range missing {
		t.Errorf("api/openapi.yaml has %s but no route serves it", op)
	}
}
