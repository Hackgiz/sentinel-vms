package onvif

// Video encoder configuration (resolution, frame rate, bitrate, GOP,
// quality) over the ONVIF media service ver10, for Sentinel Admin's
// stream-settings dialog. The camera stays the source of truth: nothing is
// cached here.
//
// TODO(stream-settings follow-up): codec switching and Media2 (ver20),
// which H.265-only cameras need; those render "not supported" today.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Resolution is one selectable frame size.
type Resolution struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// EncoderConfig is one video encoder configuration as the camera reports it.
type EncoderConfig struct {
	ConfigToken  string `json:"configToken"`
	ProfileToken string `json:"profileToken"`
	Name         string `json:"name"`
	Encoding     string `json:"encoding"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FPS          int    `json:"fps"`
	Bitrate      int    `json:"bitrate"` // kbit/s; 0 = not reported
	GovLength    int    `json:"govLength"`
	Quality      int    `json:"quality"`
	// InUse is "main" or "sub" when the profile's stream address is the
	// camera's stored main/sub RTSP URL, else "".
	InUse string `json:"inUse"`
}

// EncoderOptions are the ranges the camera accepts for the current
// encoding. A zero range (min and max both 0) means the camera did not say:
// treat it as unbounded, free-form input.
type EncoderOptions struct {
	Resolutions []Resolution `json:"resolutions"`
	FPSMin      int          `json:"fpsMin"`
	FPSMax      int          `json:"fpsMax"`
	BitrateMin  int          `json:"bitrateMin"`
	BitrateMax  int          `json:"bitrateMax"`
	GovMin      int          `json:"govMin"`
	GovMax      int          `json:"govMax"`
	QualityMin  int          `json:"qualityMin"`
	QualityMax  int          `json:"qualityMax"`
}

// EncoderStream pairs a configuration with the options it may be changed within.
type EncoderStream struct {
	EncoderConfig
	Options EncoderOptions `json:"options"`
}

func newEncoderClient(user, pass string) *client {
	return &client{http: newHTTPClient(8 * time.Second), user: strings.TrimSpace(user), pass: pass}
}

// openMedia resolves the media service the way FetchDetails does.
func (c *client) openMedia(ctx context.Context, serviceURL string) (dev, media *url.URL, err error) {
	dev, err = url.Parse(serviceURL)
	if err != nil || (dev.Scheme != "http" && dev.Scheme != "https") || dev.Host == "" {
		return nil, nil, fmt.Errorf("invalid ONVIF address %q", serviceURL)
	}
	c.syncClock(ctx, dev)
	return dev, c.mediaURL(ctx, dev), nil
}

// callMedia calls the media service, falling back to the device service
// address for cameras that serve everything from one endpoint.
func (c *client) callMedia(ctx context.Context, media, dev *url.URL, action, body string) (*node, []byte, error) {
	root, raw, err := c.callWithHeaders(ctx, media, action, soapHeader{}, body)
	if err != nil && !errors.Is(err, ErrAuth) && media.String() != dev.String() {
		root, raw, err = c.callWithHeaders(ctx, dev, action, soapHeader{}, body)
	}
	return root, raw, err
}

const mediaAction = "http://www.onvif.org/ver10/media/wsdl/"

// GetEncoders lists the camera's video encoder configurations with their
// options. mainURL/subURL are the camera's stored RTSP addresses, used only
// to label which profile feeds which stream.
func GetEncoders(ctx context.Context, serviceURL, user, pass, mainURL, subURL string) ([]EncoderStream, error) {
	c := newEncoderClient(user, pass)
	dev, media, err := c.openMedia(ctx, serviceURL)
	if err != nil {
		return nil, err
	}
	root, _, err := c.callMedia(ctx, media, dev, mediaAction+"GetProfiles", "<trt:GetProfiles/>")
	if err != nil {
		return nil, err
	}
	var out []EncoderStream
	byToken := map[string]int{}
	for _, p := range root.all("Profiles") {
		ptoken := p.attr("token")
		v := p.first("VideoEncoderConfiguration")
		if ptoken == "" || v == nil || v.attr("token") == "" {
			continue
		}
		cfg := parseEncoderConfig(v)
		cfg.ProfileToken = ptoken
		if cfg.Name == "" {
			cfg.Name = p.value("Name")
		}
		if uri, err := c.streamURI(ctx, media, ptoken); err == nil {
			cfg.InUse = streamRole(uri, mainURL, subURL)
		}
		if i, dup := byToken[cfg.ConfigToken]; dup {
			if out[i].InUse == "" {
				out[i].InUse, out[i].ProfileToken = cfg.InUse, cfg.ProfileToken
			}
			continue
		}
		byToken[cfg.ConfigToken] = len(out)
		out = append(out, EncoderStream{EncoderConfig: cfg})
	}
	if len(out) == 0 {
		return nil, errors.New("the camera did not report any video encoder configurations")
	}
	for i := range out {
		body := `<trt:GetVideoEncoderConfigurationOptions><trt:ConfigurationToken>` + escapeXML(out[i].ConfigToken) + `</trt:ConfigurationToken></trt:GetVideoEncoderConfigurationOptions>`
		oroot, _, err := c.callMedia(ctx, media, dev, mediaAction+"GetVideoEncoderConfigurationOptions", body)
		if err != nil {
			if errors.Is(err, ErrAuth) {
				return nil, err
			}
			continue // options unavailable: the stream stays listed, unbounded
		}
		out[i].Options = parseEncoderOptions(oroot, out[i].Encoding)
	}
	return out, nil
}

func parseEncoderConfig(v *node) EncoderConfig {
	atoi := func(s string) int {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0
		}
		return int(f + 0.5)
	}
	return EncoderConfig{
		ConfigToken: v.attr("token"),
		Name:        v.value("Name"),
		Encoding:    v.value("Encoding"),
		Width:       atoi(v.value("Width")),
		Height:      atoi(v.value("Height")),
		FPS:         atoi(v.value("FrameRateLimit")),
		Bitrate:     atoi(v.value("BitrateLimit")),
		GovLength:   atoi(v.value("GovLength")),
		Quality:     atoi(v.value("Quality")),
	}
}

func parseEncoderOptions(root *node, encoding string) EncoderOptions {
	atoi := func(s string) int {
		f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return int(f)
	}
	rng := func(n *node) (lo, hi int) {
		if n == nil {
			return 0, 0
		}
		return atoi(n.value("Min")), atoi(n.value("Max"))
	}
	group := "H264"
	switch e := strings.ToUpper(encoding); {
	case strings.Contains(e, "JPEG"):
		group = "JPEG"
	case strings.Contains(e, "MPEG4"):
		group = "MPEG4"
	case e == "H265" || e == "HEVC":
		group = "H265"
	}
	var o EncoderOptions
	g := root.first(group)
	if g == nil {
		g = &node{}
	}
	for _, r := range g.all("ResolutionsAvailable") {
		w, h := atoi(r.value("Width")), atoi(r.value("Height"))
		if w > 0 && h > 0 {
			o.Resolutions = append(o.Resolutions, Resolution{w, h})
		}
	}
	o.FPSMin, o.FPSMax = rng(g.first("FrameRateRange"))
	o.GovMin, o.GovMax = rng(g.first("GovLengthRange"))
	// Bitrate bounds sit in the group on some cameras, under Extension on many.
	br := g.first("BitrateRange")
	if br == nil {
		br = root.first("BitrateRange")
	}
	o.BitrateMin, o.BitrateMax = rng(br)
	o.QualityMin, o.QualityMax = rng(root.first("QualityRange"))
	return o
}

// streamRole says whether uri is the camera's main or sub stream: scheme,
// host and credentials are ignored, path and query must match.
func streamRole(uri, mainURL, subURL string) string {
	key := func(raw string) string {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || strings.TrimSpace(raw) == "" {
			return ""
		}
		return u.EscapedPath() + "?" + u.RawQuery
	}
	k := key(uri)
	if k == "" {
		return ""
	}
	if k == key(mainURL) {
		return "main"
	}
	if subURL != "" && k == key(subURL) {
		return "sub"
	}
	return ""
}

// SetEncoder changes width, height, FPS, bitrate, GOP length and quality of
// one video encoder configuration (zero fields are left alone). ONVIF wants
// the whole configuration back, so the live one is fetched fresh and only
// those fields are overwritten; the encoding and everything else stay as
// the camera has them.
func SetEncoder(ctx context.Context, serviceURL, user, pass string, cfg EncoderConfig) error {
	if cfg.ConfigToken == "" {
		return errors.New("missing encoder configuration token")
	}
	c := newEncoderClient(user, pass)
	dev, media, err := c.openMedia(ctx, serviceURL)
	if err != nil {
		return err
	}
	_, raw, err := c.callMedia(ctx, media, dev, mediaAction+"GetVideoEncoderConfiguration",
		`<trt:GetVideoEncoderConfiguration><trt:ConfigurationToken>`+escapeXML(cfg.ConfigToken)+`</trt:ConfigurationToken></trt:GetVideoEncoderConfiguration>`)
	if err != nil {
		return err
	}
	tree, err := parseTree(raw)
	if err != nil {
		return err
	}
	conf := tree.find("Configuration")
	if conf == nil {
		return errors.New("the camera did not return the encoder configuration")
	}
	set := func(parent *xmlElem, name string, v int) {
		if v > 0 {
			parent.child(name).text = strconv.Itoa(v)
		}
	}
	res := conf.child("Resolution")
	set(res, "Width", cfg.Width)
	set(res, "Height", cfg.Height)
	set(conf, "Quality", cfg.Quality)
	rc := conf.child("RateControl")
	set(rc, "FrameRateLimit", cfg.FPS)
	set(rc, "BitrateLimit", cfg.Bitrate)
	if cfg.GovLength > 0 {
		// GovLength lives under the codec element (H264/MPEG4); JPEG has none.
		for _, k := range conf.kids {
			if k.name == "H264" || k.name == "MPEG4" || k.name == "H265" {
				k.child("GovLength").text = strconv.Itoa(cfg.GovLength)
			}
		}
	}
	var b bytes.Buffer
	conf.write(&b, "trt:Configuration")
	body := `<trt:SetVideoEncoderConfiguration>` + b.String() + `<trt:ForcePersistence>true</trt:ForcePersistence></trt:SetVideoEncoderConfiguration>`
	_, _, err = c.callMedia(ctx, media, dev, mediaAction+"SetVideoEncoderConfiguration", body)
	return err
}

// xmlElem is a minimal order-preserving XML tree, used to echo a camera's
// configuration back with a few values changed (node lower-cases attribute
// names, which strict cameras would refuse).
type xmlElem struct {
	name  string
	attrs []xml.Attr
	kids  []*xmlElem
	text  string
}

func parseTree(raw []byte) (*xmlElem, error) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	d.Strict = false
	root := &xmlElem{name: "#root"}
	stack := []*xmlElem{root}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			e := &xmlElem{name: t.Name.Local}
			for _, a := range t.Attr {
				if a.Name.Space != "" || a.Name.Local == "xmlns" {
					continue // namespaces are declared by our own envelope
				}
				e.attrs = append(e.attrs, xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: a.Value})
			}
			p := stack[len(stack)-1]
			p.kids = append(p.kids, e)
			stack = append(stack, e)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if cur := stack[len(stack)-1]; len(cur.kids) == 0 {
				cur.text += string(t)
			}
		}
	}
	return root, nil
}

func (e *xmlElem) find(name string) *xmlElem {
	for _, k := range e.kids {
		if k.name == name {
			return k
		}
		if f := k.find(name); f != nil {
			return f
		}
	}
	return nil
}

// child returns the direct child called name, creating it when absent.
func (e *xmlElem) child(name string) *xmlElem {
	for _, k := range e.kids {
		if k.name == name {
			return k
		}
	}
	k := &xmlElem{name: name}
	e.kids = append(e.kids, k)
	return k
}

// write serialises e; rootName overrides the root's tag, every descendant is
// in the ONVIF schema namespace (tt:).
func (e *xmlElem) write(b *bytes.Buffer, rootName string) {
	name := rootName
	if name == "" {
		name = "tt:" + e.name
	}
	b.WriteString("<" + name)
	for _, a := range e.attrs {
		b.WriteString(" " + a.Name.Local + `="` + escapeXML(a.Value) + `"`)
	}
	b.WriteString(">")
	if len(e.kids) == 0 {
		b.WriteString(escapeXML(strings.TrimSpace(e.text)))
	}
	for _, k := range e.kids {
		k.write(b, "")
	}
	b.WriteString("</" + name + ">")
}
