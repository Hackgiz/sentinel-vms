// Package onvif finds IP cameras on the local network (WS-Discovery multicast
// plus an optional TCP sweep) and asks a camera for its stream addresses over
// ONVIF SOAP. It is a port of the Mac app's ONVIFDiscovery/ONVIFSOAPClient.
package onvif

import (
	"bytes"
	"encoding/xml"
	"strings"
)

// node is a namespace-agnostic XML element: ONVIF devices disagree wildly on
// prefixes, so everything is matched by local name, case-insensitively.
type node struct {
	local    string
	attrs    map[string]string // by local name
	children []*node
	text     strings.Builder
}

func parseXML(data []byte) *node {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	root := &node{local: "#root"}
	stack := []*node{root}
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{local: t.Name.Local, attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.attrs[strings.ToLower(a.Name.Local)] = a.Value
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			stack[len(stack)-1].text.Write(t)
		}
	}
	return root
}

// all returns every descendant (depth-first, document order) named local.
func (n *node) all(local string) []*node {
	var out []*node
	var walk func(*node)
	walk = func(p *node) {
		for _, c := range p.children {
			if strings.EqualFold(c.local, local) {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

func (n *node) first(local string) *node {
	if found := n.all(local); len(found) > 0 {
		return found[0]
	}
	return nil
}

// value is the trimmed text of the first descendant named local, or "".
func (n *node) value(local string) string {
	if f := n.first(local); f != nil {
		return strings.TrimSpace(f.text.String())
	}
	return ""
}

func (n *node) attr(local string) string { return n.attrs[strings.ToLower(local)] }

func escapeXML(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
