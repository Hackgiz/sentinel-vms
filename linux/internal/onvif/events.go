package onvif

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Camera analytics events (ONVIF Profile M, also Profile S/T motion and rule
// events) over a PullPoint subscription: the camera runs its own detection
// and we long-poll it for what it saw.

// AnalyticsEvent is one thing a camera's built-in analytics reported.
type AnalyticsEvent struct {
	Kind   string    `json:"kind"`  // Person | Vehicle | Face | License plate | Line crossing | Intrusion | Loitering | Tamper | Motion
	Label  string    `json:"label"` // object class ("person", "car") or the camera's rule name
	Rule   string    `json:"rule"`  // rule name configured on the camera, if any
	Topic  string    `json:"topic"` // namespace prefixes stripped, e.g. RuleEngine/LineDetector/Crossed
	Active bool      `json:"active"`
	Score  float64   `json:"score"` // camera-reported confidence 0–1; 0 = not reported
	At     time.Time `json:"at"`    // when we received it (camera clocks drift)
}

const (
	actionCreatePullPoint = "http://www.onvif.org/ver10/events/wsdl/EventPortType/CreatePullPointSubscriptionRequest"
	actionPullMessages    = "http://www.onvif.org/ver10/events/wsdl/PullPointSubscription/PullMessagesRequest"
	actionRenew           = "http://docs.oasis-open.org/wsn/bw-2/SubscriptionManager/RenewRequest"
	actionUnsubscribe     = "http://docs.oasis-open.org/wsn/bw-2/SubscriptionManager/UnsubscribeRequest"

	subscriptionTTL = "PT120S"
	renewEvery      = 60 * time.Second
	pullTimeout     = "PT10S"
)

// WatchEvents keeps a PullPoint subscription open on one camera until ctx
// ends, reconnecting with backoff. onEvent gets every recognised analytics
// event; status gets a short human-readable connection state.
func WatchEvents(ctx context.Context, serviceURL, user, pass string, onEvent func(AnalyticsEvent), status func(string)) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := watchOnce(ctx, serviceURL, user, pass, onEvent, status)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 2*time.Minute {
			backoff = 5 * time.Second
		}
		if errors.Is(err, ErrAuth) {
			status("Camera rejected the login for analytics events")
			backoff = 5 * time.Minute
		} else {
			status("Not connected: " + err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Minute {
			backoff *= 2
		}
	}
}

func watchOnce(ctx context.Context, serviceURL, user, pass string, onEvent func(AnalyticsEvent), status func(string)) error {
	dev, err := url.Parse(serviceURL)
	if err != nil || (dev.Scheme != "http" && dev.Scheme != "https") || dev.Host == "" {
		return fmt.Errorf("invalid ONVIF address %q", serviceURL)
	}
	// PullMessages long-polls for up to pullTimeout, so allow well beyond it.
	c := &client{http: &http.Client{Timeout: 30 * time.Second}, user: strings.TrimSpace(user), pass: pass}
	c.syncClock(ctx, dev)
	p, err := c.subscribe(ctx, c.eventsURL(ctx, dev))
	if err != nil {
		return err
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		p.unsubscribe(uctx)
	}()
	status("Connected")

	renewAt := time.Now().Add(renewEvery)
	for ctx.Err() == nil {
		if time.Now().After(renewAt) {
			// Some cameras only extend on PullMessages and reject Renew; if
			// the subscription really lapses, the next pull fails and we
			// resubscribe.
			_ = p.renew(ctx)
			renewAt = time.Now().Add(renewEvery)
		}
		t0 := time.Now()
		msgs, err := p.pull(ctx)
		if err != nil {
			return err
		}
		for _, n := range msgs {
			if ev, ok := classify(n); ok {
				ev.At = time.Now()
				onEvent(ev)
			}
		}
		// A camera that ignores the long-poll timeout must not be hammered.
		if len(msgs) == 0 && time.Since(t0) < time.Second {
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
	return ctx.Err()
}

// eventsURL asks the device where its event service lives; many cameras
// serve every service on the device URL, so that's the fallback.
func (c *client) eventsURL(ctx context.Context, dev *url.URL) *url.URL {
	root, err := c.call(ctx, dev, "http://www.onvif.org/ver10/device/wsdl/GetCapabilities",
		"<tds:GetCapabilities><tds:Category>Events</tds:Category></tds:GetCapabilities>")
	if err == nil {
		if ev := root.first("Events"); ev != nil {
			if u, err := url.Parse(ev.value("XAddr")); err == nil && u.Host != "" {
				return u
			}
		}
	}
	return dev
}

// pullPoint is one live subscription.
type pullPoint struct {
	c    *client
	addr *url.URL
	refs string // the camera's ReferenceParameters, echoed as headers
	ns   string // namespace declarations those parameters rely on
}

var (
	refParamsPattern = regexp.MustCompile(`(?s)<(?:[\w.-]+:)?ReferenceParameters\b[^>]*>(.*?)</(?:[\w.-]+:)?ReferenceParameters>`)
	nsDeclPattern    = regexp.MustCompile(`xmlns:([\w.-]+)\s*=\s*"([^"]*)"`)
	prefixUsePattern = regexp.MustCompile(`</?([\w.-]+):`)
)

func (c *client) subscribe(ctx context.Context, events *url.URL) (*pullPoint, error) {
	body := `<tev:CreatePullPointSubscription><tev:InitialTerminationTime>` + subscriptionTTL + `</tev:InitialTerminationTime></tev:CreatePullPointSubscription>`
	root, raw, err := c.callWithHeaders(ctx, events, actionCreatePullPoint, addressing(actionCreatePullPoint, events.String()), body)
	if err != nil {
		return nil, err
	}
	ref := root.first("SubscriptionReference")
	if ref == nil {
		return nil, errors.New("the camera did not open an event subscription")
	}
	addr, err := url.Parse(ref.value("Address"))
	if err != nil || addr.Host == "" {
		return nil, errors.New("the camera returned an unusable event subscription address")
	}
	// Some cameras answer with an address for an interface we can't reach
	// (or "localhost"); keep their path but talk to the host we already use.
	if addr.Hostname() != events.Hostname() {
		addr.Host = events.Host
	}
	p := &pullPoint{c: c, addr: addr}
	if m := refParamsPattern.FindSubmatch(raw); m != nil {
		p.refs = strings.TrimSpace(string(m[1]))
		p.ns = neededNamespaces(raw, p.refs)
	}
	return p, nil
}

// neededNamespaces declares, on our <s:Header>, any prefix the echoed
// reference parameters use without declaring it themselves.
func neededNamespaces(response []byte, refs string) string {
	decls := map[string]string{}
	for _, m := range nsDeclPattern.FindAllSubmatch(response, -1) {
		if _, ok := decls[string(m[1])]; !ok {
			decls[string(m[1])] = string(m[2])
		}
	}
	local := map[string]bool{}
	for _, m := range nsDeclPattern.FindAllStringSubmatch(refs, -1) {
		local[m[1]] = true
	}
	ours := map[string]bool{"s": true, "tds": true, "trt": true, "tt": true, "tev": true, "wsnt": true, "wsa": true}
	var b strings.Builder
	seen := map[string]bool{}
	for _, m := range prefixUsePattern.FindAllStringSubmatch(refs, -1) {
		pfx := m[1]
		if seen[pfx] || local[pfx] || ours[pfx] || decls[pfx] == "" {
			continue
		}
		seen[pfx] = true
		b.WriteString(` xmlns:` + pfx + `="` + escapeXML(decls[pfx]) + `"`)
	}
	return b.String()
}

func addressing(action, to string) soapHeader {
	return soapHeader{elements: `<wsa:Action>` + action + `</wsa:Action><wsa:To>` + escapeXML(to) + `</wsa:To>`}
}

func (p *pullPoint) header(action string) soapHeader {
	h := addressing(action, p.addr.String())
	h.elements += p.refs
	h.nsDecls = p.ns
	return h
}

func (p *pullPoint) pull(ctx context.Context) ([]notification, error) {
	body := `<tev:PullMessages><tev:Timeout>` + pullTimeout + `</tev:Timeout><tev:MessageLimit>32</tev:MessageLimit></tev:PullMessages>`
	root, _, err := p.c.callWithHeaders(ctx, p.addr, actionPullMessages, p.header(actionPullMessages), body)
	if err != nil {
		return nil, err
	}
	return parseNotifications(root), nil
}

func (p *pullPoint) renew(ctx context.Context) error {
	body := `<wsnt:Renew><wsnt:TerminationTime>` + subscriptionTTL + `</wsnt:TerminationTime></wsnt:Renew>`
	_, _, err := p.c.callWithHeaders(ctx, p.addr, actionRenew, p.header(actionRenew), body)
	return err
}

func (p *pullPoint) unsubscribe(ctx context.Context) {
	_, _, _ = p.c.callWithHeaders(ctx, p.addr, actionUnsubscribe, p.header(actionUnsubscribe), `<wsnt:Unsubscribe/>`)
}

// MARK: - Parsing

// notification is one wsnt:NotificationMessage, vendor-neutral.
type notification struct {
	topic  string // prefixes stripped
	op     string // Initialized | Changed | Deleted | ""
	source map[string]string
	data   map[string]string
}

func parseNotifications(root *node) []notification {
	var out []notification
	for _, nm := range root.all("NotificationMessage") {
		n := notification{topic: cleanTopic(nm.value("Topic")), source: map[string]string{}, data: map[string]string{}}
		// The payload is the innermost Message (tt:Message inside wsnt:Message).
		msg := nm
		for _, m := range nm.all("Message") {
			if m.first("Data") != nil || m.first("Source") != nil {
				msg = m
			}
		}
		n.op = msg.attr("PropertyOperation")
		if src := msg.first("Source"); src != nil {
			for _, si := range src.all("SimpleItem") {
				n.source[si.attr("Name")] = si.attr("Value")
			}
		}
		if data := msg.first("Data"); data != nil {
			for _, si := range data.all("SimpleItem") {
				n.data[si.attr("Name")] = si.attr("Value")
			}
			// Profile M object classification: <tt:Class><tt:Type Likelihood="0.9">Human</tt:Type></tt:Class>
			if t := data.first("Type"); t != nil {
				if v := strings.TrimSpace(t.text.String()); v != "" {
					n.data["ObjectType"] = v
					if l := t.attr("Likelihood"); l != "" {
						n.data["Likelihood"] = l
					}
				}
			}
		}
		out = append(out, n)
	}
	return out
}

// cleanTopic turns "tns1:RuleEngine/FieldDetector/ObjectsInside" into
// "RuleEngine/FieldDetector/ObjectsInside".
func cleanTopic(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '|'); i >= 0 { // ConcreteSet dialect: first topic
		t = t[:i]
	}
	parts := strings.Split(t, "/")
	for i, p := range parts {
		if j := strings.IndexByte(p, ':'); j >= 0 {
			parts[i] = p[j+1:]
		}
	}
	return strings.Join(parts, "/")
}

func containsAny(s string, keys ...string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// classify maps a vendor's topic and payload onto the kinds Sentinel knows.
// Rule events (lines, zones) win over object class: "a person crossed the
// gate line" is a line crossing labelled "person".
func classify(n notification) (AnalyticsEvent, bool) {
	if n.op == "Initialized" || n.op == "Deleted" {
		// Initialized replays current state when we subscribe; acting on it
		// would re-raise alarms after every reconnect.
		return AnalyticsEvent{}, false
	}
	topic := strings.ToLower(n.topic)
	class := ""
	for k, v := range n.data {
		switch strings.ToLower(k) {
		case "classtypes", "classtype", "objecttype", "objectclass", "class", "type":
			class = strings.ToLower(strings.TrimSpace(v))
		}
	}
	isPerson := containsAny(topic, "human", "people", "person", "pedestrian") || containsAny(class, "human", "people", "person", "pedestrian")
	isVehicle := strings.Contains(topic, "vehicle") || containsAny(class, "vehicle", "car", "truck", "bus", "motor", "bike", "bicycle")
	kind := ""
	switch {
	case containsAny(topic, "linedetector", "linecross", "tripwire", "/crossed"):
		kind = "Line crossing"
	case strings.Contains(topic, "loiter"):
		kind = "Loitering"
	case containsAny(topic, "fielddetector", "intrusion", "objectsinside", "regionentr", "regionexit", "fielddetection", "areaentr"):
		kind = "Intrusion"
	case containsAny(topic, "tamper", "globalscenechange", "defocus", "imagetoo"):
		kind = "Tamper"
	case strings.Contains(topic, "face") || strings.Contains(class, "face"):
		kind = "Face"
	case containsAny(topic, "licenseplate", "licenceplate", "numberplate", "/lpr", "anpr") || containsAny(class, "plate"):
		kind = "License plate"
	case isPerson:
		kind = "Person"
	case isVehicle:
		kind = "Vehicle"
	case strings.Contains(topic, "motion"):
		kind = "Motion"
	default:
		return AnalyticsEvent{}, false
	}

	ev := AnalyticsEvent{Kind: kind, Topic: n.topic, Active: true}
	for _, k := range []string{"Rule", "RuleName"} {
		if v := n.source[k]; v != "" {
			ev.Rule = v
			break
		}
	}
	switch {
	case isPerson:
		ev.Label = "person"
	case isVehicle && class != "":
		ev.Label = class
	case isVehicle:
		ev.Label = "vehicle"
	case class != "":
		ev.Label = class
	default:
		ev.Label = ev.Rule
	}

	// Stateful events say on/off in a boolean field (IsMotion, IsInside,
	// State…); events without one (LineDetector/Crossed) are momentary.
	sawState := false
	ev.Active = false
	for k, v := range n.data {
		lk := strings.ToLower(k)
		if !(strings.HasPrefix(lk, "is") || lk == "state" || lk == "active" || lk == "alarm") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1":
			sawState, ev.Active = true, true
		case "false", "0":
			sawState = true
		}
	}
	if !sawState {
		ev.Active = true
	}

	for _, k := range []string{"Likelihood", "Confidence", "Score"} {
		if f, err := strconv.ParseFloat(n.data[k], 64); err == nil && f > 0 {
			if f > 1 {
				f /= 100
			}
			ev.Score = min(f, 1)
			break
		}
	}
	return ev, true
}
