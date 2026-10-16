package fakecam

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
)

// PTZ service: the camera keeps a preset list and logs every PTZ call as a
// short string ("move 0.500 -0.500 0.000", "stop", "goto 2", "set Door",
// "remove 2") for tests to assert on. Profiles it reports carry a
// PTZConfiguration only when PTZ is on.

var (
	reVelocity = regexp.MustCompile(`PanTilt x="([^"]*)" y="([^"]*)"/><tt:Zoom x="([^"]*)"`)
	reToken    = regexp.MustCompile(`<tptz:PresetToken>([^<]*)</tptz:PresetToken>`)
	reName     = regexp.MustCompile(`<tptz:PresetName>([^<]*)</tptz:PresetName>`)
)

// PTZLog returns the PTZ calls the camera has received.
func (c *Camera) PTZLog() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ptzLog...)
}

// PTZPresets returns the camera's current presets as {token, name} pairs.
func (c *Camera) PTZPresets() [][2]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]string(nil), c.presets...)
}

// SeedPreset adds a preset without going through SOAP.
func (c *Camera) SeedPreset(token, name string) {
	c.mu.Lock()
	c.presets = append(c.presets, [2]string{token, name})
	c.mu.Unlock()
}

// ptzProfileConfig marks only the "main" profile as PTZ-capable, so tests
// can check the right profile token is driven.
func (c *Camera) ptzProfileConfig(token string) string {
	if !c.PTZ || token != "main" {
		return ""
	}
	return `<tt:PTZConfiguration token="ptz1"><tt:Name>PTZ</tt:Name></tt:PTZConfiguration>`
}

func (c *Camera) ptzCapability(host string) string {
	if !c.PTZ {
		return ""
	}
	return `<tt:PTZ><tt:XAddr>http://` + host + `/onvif/ptz_service</tt:XAddr></tt:PTZ>`
}

// servePTZ answers a PTZ request s and reports whether it was one.
func (c *Camera) servePTZ(w http.ResponseWriter, s string) bool {
	op := ""
	for _, o := range []string{"ContinuousMove", "Stop", "GetPresets", "GotoPreset", "SetPreset", "RemovePreset"} {
		if strings.Contains(s, "<tptz:"+o) {
			op = o
			break
		}
	}
	if op == "" {
		return false
	}
	if !c.PTZ || !strings.Contains(s, "<tptz:ProfileToken>main</tptz:ProfileToken>") {
		w.WriteHeader(http.StatusBadRequest)
		reply(w, `<s:Fault><s:Reason><s:Text>no ptz</s:Text></s:Reason></s:Fault>`)
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	match := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(s); m != nil {
			return html.UnescapeString(m[1])
		}
		return ""
	}
	switch op {
	case "ContinuousMove":
		v := reVelocity.FindStringSubmatch(s)
		if v == nil {
			v = []string{"", "?", "?", "?"}
		}
		c.ptzLog = append(c.ptzLog, fmt.Sprintf("move %s %s %s", v[1], v[2], v[3]))
		reply(w, `<tptz:ContinuousMoveResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/>`)
	case "Stop":
		c.ptzLog = append(c.ptzLog, "stop")
		reply(w, `<tptz:StopResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/>`)
	case "GetPresets":
		var b strings.Builder
		b.WriteString(`<tptz:GetPresetsResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl">`)
		for _, p := range c.presets {
			fmt.Fprintf(&b, `<tptz:Preset token="%s"><tt:Name>%s</tt:Name></tptz:Preset>`, xmlEscape(p[0]), xmlEscape(p[1]))
		}
		b.WriteString(`</tptz:GetPresetsResponse>`)
		reply(w, b.String())
	case "GotoPreset":
		c.ptzLog = append(c.ptzLog, "goto "+match(reToken))
		reply(w, `<tptz:GotoPresetResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/>`)
	case "SetPreset":
		name := match(reName)
		tok := fmt.Sprint(len(c.presets) + 1)
		c.presets = append(c.presets, [2]string{tok, name})
		c.ptzLog = append(c.ptzLog, "set "+name)
		reply(w, `<tptz:SetPresetResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:PresetToken>`+tok+`</tptz:PresetToken></tptz:SetPresetResponse>`)
	case "RemovePreset":
		tok := match(reToken)
		for i, p := range c.presets {
			if p[0] == tok {
				c.presets = append(c.presets[:i], c.presets[i+1:]...)
				break
			}
		}
		c.ptzLog = append(c.ptzLog, "remove "+tok)
		reply(w, `<tptz:RemovePresetResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/>`)
	}
	return true
}
