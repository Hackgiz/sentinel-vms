package onvif

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNoPTZ means the camera has no ONVIF PTZ service.
var ErrNoPTZ = errors.New("the camera does not support PTZ")

// Preset is a saved PTZ position.
type Preset struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

// PTZ controls one camera's pan/tilt/zoom over the ONVIF PTZ service. It is
// safe to keep and reuse; calls are independent SOAP requests.
type PTZ struct {
	c       *client
	svc     *url.URL
	profile string
}

const (
	ptzNS   = ` xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"`
	ptzBase = "http://www.onvif.org/ver20/ptz/wsdl/"
)

func newPTZClient(serviceURL, user, pass string) (*client, *url.URL, error) {
	dev, err := url.Parse(serviceURL)
	if err != nil || (dev.Scheme != "http" && dev.Scheme != "https") || dev.Host == "" {
		return nil, nil, fmt.Errorf("invalid ONVIF address %q", serviceURL)
	}
	return &client{http: newHTTPClient(8 * time.Second), user: strings.TrimSpace(user), pass: pass}, dev, nil
}

// ptzURL asks the device service where the PTZ service lives.
func (c *client) ptzURL(ctx context.Context, dev *url.URL) (*url.URL, error) {
	root, err := c.call(ctx, dev, "http://www.onvif.org/ver10/device/wsdl/GetCapabilities",
		"<tds:GetCapabilities><tds:Category>PTZ</tds:Category></tds:GetCapabilities>")
	if err != nil {
		return nil, err
	}
	p := root.first("PTZ")
	if p == nil {
		return nil, ErrNoPTZ
	}
	u, err := url.Parse(p.value("XAddr"))
	if err != nil || u.Host == "" {
		return nil, ErrNoPTZ
	}
	return u, nil
}

// PTZSupported is the cheap capability probe: one clock sync and one
// GetCapabilities. A camera that answers but has no PTZ service is
// (false, nil); an unreachable one or bad credentials is an error.
func PTZSupported(ctx context.Context, serviceURL, user, pass string) (bool, error) {
	c, dev, err := newPTZClient(serviceURL, user, pass)
	if err != nil {
		return false, err
	}
	c.syncClock(ctx, dev)
	if _, err := c.ptzURL(ctx, dev); err != nil {
		if errors.Is(err, ErrNoPTZ) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// OpenPTZ finds the camera's PTZ service and the media profile to drive
// (the first with a PTZ configuration, else the first profile).
func OpenPTZ(ctx context.Context, serviceURL, user, pass string) (*PTZ, error) {
	c, dev, err := newPTZClient(serviceURL, user, pass)
	if err != nil {
		return nil, err
	}
	c.syncClock(ctx, dev)
	svc, err := c.ptzURL(ctx, dev)
	if err != nil {
		return nil, err
	}
	const act = "http://www.onvif.org/ver10/media/wsdl/GetProfiles"
	media := c.mediaURL(ctx, dev)
	root, err := c.call(ctx, media, act, "<trt:GetProfiles/>")
	if err != nil && media.String() != dev.String() {
		root, err = c.call(ctx, dev, act, "<trt:GetProfiles/>")
	}
	if err != nil {
		return nil, err
	}
	token := ""
	for _, n := range root.all("Profiles") {
		t := n.attr("token")
		if t == "" {
			continue
		}
		if token == "" {
			token = t
		}
		if n.first("PTZConfiguration") != nil {
			token = t
			break
		}
	}
	if token == "" {
		return nil, errors.New("the camera did not return any media profiles")
	}
	return &PTZ{c: c, svc: svc, profile: token}, nil
}

func (p *PTZ) call(ctx context.Context, op, inner string) (*node, error) {
	body := "<tptz:" + op + ptzNS + "><tptz:ProfileToken>" + escapeXML(p.profile) + "</tptz:ProfileToken>" + inner + "</tptz:" + op + ">"
	return p.c.call(ctx, p.svc, ptzBase+op, body)
}

// ClampVelocity limits a velocity to -1..1 (NaN becomes 0).
func ClampVelocity(v float64) float64 {
	switch {
	case v != v:
		return 0
	case v > 1:
		return 1
	case v < -1:
		return -1
	}
	return v
}

func vel(v float64) string { return strconv.FormatFloat(ClampVelocity(v), 'f', 3, 64) }

// ContinuousMove starts moving at the given velocities (each -1..1; values
// outside are clamped) until Stop.
func (p *PTZ) ContinuousMove(ctx context.Context, pan, tilt, zoom float64) error {
	_, err := p.call(ctx, "ContinuousMove", `<tptz:Velocity><tt:PanTilt x="`+vel(pan)+`" y="`+vel(tilt)+`"/><tt:Zoom x="`+vel(zoom)+`"/></tptz:Velocity>`)
	return err
}

// Stop halts pan/tilt and zoom.
func (p *PTZ) Stop(ctx context.Context) error {
	_, err := p.call(ctx, "Stop", `<tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>true</tptz:Zoom>`)
	return err
}

// GetPresets lists the camera's saved positions.
func (p *PTZ) GetPresets(ctx context.Context) ([]Preset, error) {
	root, err := p.call(ctx, "GetPresets", "")
	if err != nil {
		return nil, err
	}
	out := []Preset{}
	for _, n := range root.all("Preset") {
		tok := n.attr("token")
		if tok == "" {
			continue
		}
		name := n.value("Name")
		if name == "" {
			name = "Preset " + tok
		}
		out = append(out, Preset{Token: tok, Name: name})
	}
	return out, nil
}

// GotoPreset moves to a saved position.
func (p *PTZ) GotoPreset(ctx context.Context, token string) error {
	_, err := p.call(ctx, "GotoPreset", `<tptz:PresetToken>`+escapeXML(token)+`</tptz:PresetToken>`)
	return err
}

// SetPreset saves the current position under name and returns it.
func (p *PTZ) SetPreset(ctx context.Context, name string) (Preset, error) {
	root, err := p.call(ctx, "SetPreset", `<tptz:PresetName>`+escapeXML(name)+`</tptz:PresetName>`)
	if err != nil {
		return Preset{}, err
	}
	tok := root.value("PresetToken")
	if tok == "" {
		return Preset{}, errors.New("the camera did not return a preset token")
	}
	return Preset{Token: tok, Name: name}, nil
}

// RemovePreset deletes a saved position.
func (p *PTZ) RemovePreset(ctx context.Context, token string) error {
	_, err := p.call(ctx, "RemovePreset", `<tptz:PresetToken>`+escapeXML(token)+`</tptz:PresetToken>`)
	return err
}
