package onvif

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var deviceServicePath = regexp.MustCompile(`(?i)device_service`)

// ErrAuth means the camera answered but refused the username/password.
var ErrAuth = errors.New("the camera rejected the username or password")

// Profile is one ONVIF media profile with its RTSP address (no credentials).
type Profile struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FPS      int    `json:"fps"`
	Encoding string `json:"encoding"` // H264, H265, JPEG, or ""
	RTSPURL  string `json:"rtspURL"`
}

func (p Profile) pixels() int { return p.Width * p.Height }

// codecRank: real video codecs beat MJPEG/unknown (MJPEG can't be HLS-muxed).
func (p Profile) codecRank() int {
	switch e := strings.ToUpper(p.Encoding); {
	case e == "H265" || e == "HEVC":
		return 3
	case e == "H264":
		return 2
	case strings.Contains(e, "JPEG"):
		return 0
	default:
		return 1
	}
}

// Details is what a camera reports about itself once we're signed in.
type Details struct {
	Manufacturer string    `json:"manufacturer"`
	Model        string    `json:"model"`
	Firmware     string    `json:"firmware"`
	Serial       string    `json:"serial"`
	ServiceURL   string    `json:"serviceURL"`
	Profiles     []Profile `json:"profiles"`
}

// ChooseStreams picks the recording stream (best codec, then highest
// resolution) and a distinct lower-resolution viewing stream, if any.
func ChooseStreams(profiles []Profile) (main, sub *Profile) {
	for i := range profiles {
		p := &profiles[i]
		if main == nil || p.codecRank() > main.codecRank() ||
			(p.codecRank() == main.codecRank() && p.pixels() > main.pixels()) {
			main = p
		}
	}
	if main == nil {
		return nil, nil
	}
	for i := range profiles {
		p := &profiles[i]
		if p.RTSPURL == main.RTSPURL || p.codecRank() < 2 || p.pixels() == 0 || p.pixels() >= main.pixels() {
			continue
		}
		if sub == nil || p.pixels() < sub.pixels() {
			sub = p
		}
	}
	return main, sub
}

type client struct {
	http       *http.Client
	user, pass string
	offset     time.Duration // camera clock minus ours
}

const maxSOAPBody = 2 << 20

// FetchDetails signs in to the camera's ONVIF device service and returns its
// identity and every profile that has an RTSP stream address.
func FetchDetails(ctx context.Context, serviceURL, user, pass string) (*Details, error) {
	dev, err := url.Parse(serviceURL)
	if err != nil || (dev.Scheme != "http" && dev.Scheme != "https") || dev.Host == "" {
		return nil, fmt.Errorf("invalid ONVIF address %q", serviceURL)
	}
	c := &client{http: &http.Client{Timeout: 8 * time.Second}, user: strings.TrimSpace(user), pass: pass}
	c.syncClock(ctx, dev)

	info, err := c.call(ctx, dev, "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation", "<tds:GetDeviceInformation/>")
	if err != nil {
		return nil, err
	}
	out := &Details{
		Manufacturer: info.value("Manufacturer"),
		Model:        info.value("Model"),
		Firmware:     info.value("FirmwareVersion"),
		Serial:       info.value("SerialNumber"),
		ServiceURL:   serviceURL,
	}

	media := c.mediaURL(ctx, dev)
	descs, err := c.profiles(ctx, media)
	if err != nil && media.String() != dev.String() {
		media = dev
		descs, err = c.profiles(ctx, media)
	}
	if err != nil {
		return nil, err
	}
	for _, p := range descs {
		uri, err := c.streamURI(ctx, media, p.Token)
		if err != nil && media.String() != dev.String() {
			uri, err = c.streamURI(ctx, dev, p.Token)
		}
		if err != nil {
			continue
		}
		p.RTSPURL = uri
		out.Profiles = append(out.Profiles, p)
	}
	if len(out.Profiles) == 0 {
		return nil, errors.New("the camera did not return any RTSP stream addresses")
	}
	return out, nil
}

// syncClock aligns WS-Security timestamps with the camera's own clock; many
// cameras (Hanwha especially) reject tokens outside a tight window. Best-effort.
func (c *client) syncClock(ctx context.Context, dev *url.URL) {
	saved := c.user
	c.user = "" // unauthenticated call
	root, err := c.call(ctx, dev, "http://www.onvif.org/ver10/device/wsdl/GetSystemDateAndTime", "<tds:GetSystemDateAndTime/>")
	c.user = saved
	if err != nil {
		return
	}
	utc := root.first("UTCDateTime")
	if utc == nil {
		return
	}
	n := func(k string) int { v, _ := strconv.Atoi(utc.value(k)); return v }
	if n("Year") == 0 {
		return
	}
	cam := time.Date(n("Year"), time.Month(n("Month")), n("Day"), n("Hour"), n("Minute"), n("Second"), 0, time.UTC)
	c.offset = time.Until(cam)
}

func (c *client) mediaURL(ctx context.Context, dev *url.URL) *url.URL {
	root, err := c.call(ctx, dev, "http://www.onvif.org/ver10/device/wsdl/GetCapabilities",
		"<tds:GetCapabilities><tds:Category>Media</tds:Category></tds:GetCapabilities>")
	if err == nil {
		x := ""
		if m := root.first("Media"); m != nil {
			x = m.value("XAddr")
		}
		if x == "" {
			x = root.value("XAddr")
		}
		if u, err := url.Parse(x); err == nil && u.Host != "" {
			return u
		}
	}
	// Fallback: …/device_service → …/media_service
	u := *dev
	if deviceServicePath.MatchString(u.Path) {
		u.Path = deviceServicePath.ReplaceAllString(u.Path, "media_service")
	} else {
		u.Path = "/onvif/media_service"
	}
	return &u
}

func (c *client) profiles(ctx context.Context, media *url.URL) ([]Profile, error) {
	root, err := c.call(ctx, media, "http://www.onvif.org/ver10/media/wsdl/GetProfiles", "<trt:GetProfiles/>")
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, n := range root.all("Profiles") {
		token := n.attr("token")
		if token == "" {
			continue
		}
		p := Profile{Token: token, Name: n.value("Name")}
		if p.Name == "" {
			p.Name = "Profile " + token
		}
		// Scope to the video encoder: audio configs also carry <Encoding>.
		if v := n.first("VideoEncoderConfiguration"); v != nil {
			p.Encoding = v.value("Encoding")
			p.Width, _ = strconv.Atoi(v.value("Width"))
			p.Height, _ = strconv.Atoi(v.value("Height"))
			if f, err := strconv.ParseFloat(v.value("FrameRateLimit"), 64); err == nil {
				p.FPS = int(f + 0.5)
			}
		}
		if p.Width == 0 {
			p.Width, _ = strconv.Atoi(n.value("Width"))
			p.Height, _ = strconv.Atoi(n.value("Height"))
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("the camera did not return any media profiles")
	}
	return out, nil
}

func (c *client) streamURI(ctx context.Context, media *url.URL, token string) (string, error) {
	body := `<trt:GetStreamUri><trt:StreamSetup><tt:Stream>RTP-Unicast</tt:Stream><tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trt:StreamSetup><trt:ProfileToken>` +
		escapeXML(token) + `</trt:ProfileToken></trt:GetStreamUri>`
	root, err := c.call(ctx, media, "http://www.onvif.org/ver10/media/wsdl/GetStreamUri", body)
	if err != nil {
		return "", err
	}
	uri := root.value("Uri")
	if !strings.HasPrefix(strings.ToLower(uri), "rtsp") {
		return "", errors.New("no RTSP address for profile " + token)
	}
	return uri, nil
}

// call POSTs one SOAP request. Auth is WS-Security UsernameToken digest (the
// ONVIF standard); if the camera instead demands HTTP Digest, retry with that.
func (c *client) call(ctx context.Context, u *url.URL, action, body string) (*node, error) {
	envelope := c.envelope(body)
	resp, err := c.post(ctx, u, action, envelope, "")
	if err != nil {
		return nil, err
	}
	if resp.status == http.StatusUnauthorized && c.user != "" {
		if chal := resp.header.Get("WWW-Authenticate"); strings.HasPrefix(strings.ToLower(chal), "digest") {
			authz := digestAuthorization(chal, c.user, c.pass, "POST", u.RequestURI())
			resp, err = c.post(ctx, u, action, envelope, authz)
			if err != nil {
				return nil, err
			}
		}
	}
	root := parseXML(resp.body)
	if resp.status >= 200 && resp.status < 300 {
		return root, nil
	}
	detail := firstNonEmpty(root.value("Text"), root.value("Reason"))
	if resp.status == http.StatusUnauthorized || isAuthFault(root, detail) {
		return nil, ErrAuth
	}
	if detail == "" {
		detail = http.StatusText(resp.status)
	}
	return nil, fmt.Errorf("camera replied HTTP %d: %s", resp.status, detail)
}

// isAuthFault recognises a credentials fault. The code sits in the nested
// Fault/Code/Subcode/Value (e.g. ter:NotAuthorized); the human text varies by
// vendor ("Sender not Authorized", Tapo's "Authority failure", …).
func isAuthFault(root *node, detail string) bool {
	var codes []string
	if code := root.first("Code"); code != nil {
		for _, v := range code.all("Value") {
			codes = append(codes, strings.ToLower(strings.TrimSpace(v.text.String())))
		}
	}
	for _, c := range codes {
		if strings.HasSuffix(c, "notauthorized") || strings.HasSuffix(c, "failedauthentication") {
			return true
		}
	}
	d := strings.ToLower(detail)
	for _, k := range []string{"not authorized", "notauthorized", "unauthorized", "authenticat", "authority failure", "invalid username", "wrong password"} {
		if strings.Contains(d, k) {
			return true
		}
	}
	return false
}

type soapResponse struct {
	status int
	header http.Header
	body   []byte
}

func (c *client) post(ctx context.Context, u *url.URL, action string, envelope []byte, authz string) (*soapResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `application/soap+xml; charset=utf-8; action="`+action+`"`)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach the camera's ONVIF service: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxSOAPBody))
	if err != nil {
		return nil, err
	}
	return &soapResponse{status: res.StatusCode, header: res.Header, body: b}, nil
}

func (c *client) envelope(body string) []byte {
	header := "<s:Header/>"
	if c.user != "" {
		header = "<s:Header>" + c.security() + "</s:Header>"
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">` +
		header + `<s:Body>` + body + `</s:Body></s:Envelope>`)
}

// security builds a WS-Security UsernameToken with PasswordDigest =
// Base64(SHA1(nonce + created + password)) — SHA-1 is mandated by the profile.
func (c *client) security() string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	created := time.Now().Add(c.offset).UTC().Format("2006-01-02T15:04:05Z")
	return `<wsse:Security s:mustUnderstand="1" xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"><wsse:UsernameToken>` +
		`<wsse:Username>` + escapeXML(c.user) + `</wsse:Username>` +
		`<wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">` + passwordDigest(nonce, created, c.pass) + `</wsse:Password>` +
		`<wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">` + base64.StdEncoding.EncodeToString(nonce) + `</wsse:Nonce>` +
		`<wsu:Created>` + created + `</wsu:Created></wsse:UsernameToken></wsse:Security>`
}

func passwordDigest(nonce []byte, created, password string) string {
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(password))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// digestAuthorization answers an RFC 2617 HTTP Digest challenge (MD5, qop=auth).
func digestAuthorization(challenge, user, pass, method, uri string) string {
	params := map[string]string{}
	for _, part := range splitChallenge(strings.TrimSpace(challenge[len("digest"):])) {
		if k, v, ok := strings.Cut(part, "="); ok {
			params[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	md5hex := func(s string) string { s16 := md5.Sum([]byte(s)); return hex.EncodeToString(s16[:]) }
	ha1 := md5hex(user + ":" + params["realm"] + ":" + pass)
	ha2 := md5hex(method + ":" + uri)
	cnonceBytes := make([]byte, 8)
	_, _ = rand.Read(cnonceBytes)
	cnonce := hex.EncodeToString(cnonceBytes)
	var response, qopPart string
	if strings.Contains(params["qop"], "auth") {
		response = md5hex(ha1 + ":" + params["nonce"] + ":00000001:" + cnonce + ":auth:" + ha2)
		qopPart = `, qop=auth, nc=00000001, cnonce="` + cnonce + `"`
	} else {
		response = md5hex(ha1 + ":" + params["nonce"] + ":" + ha2)
	}
	out := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"%s`,
		user, params["realm"], params["nonce"], uri, response, qopPart)
	if op := params["opaque"]; op != "" {
		out += `, opaque="` + op + `"`
	}
	return out
}

// splitChallenge splits on commas that aren't inside quotes.
func splitChallenge(s string) []string {
	var parts []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ',' && !quoted:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}
