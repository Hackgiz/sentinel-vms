// Package fakecam is a minimal ONVIF camera for tests and demos: it answers
// WS-Discovery probes and the SOAP calls the discovery flow makes, and checks
// WS-Security digests like a real device would.
package fakecam

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Stream struct {
	Token, Name, Encoding string
	Width, Height, FPS    int
	RTSPURL               string
}

type Camera struct {
	User, Pass   string
	Manufacturer string
	Model        string
	Streams      []Stream
	ClockSkew    time.Duration // simulate a camera whose clock is off
}

// ServeHTTP implements the ONVIF device + media services on any path.
func (c *Camera) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	s := string(body)
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	if strings.Contains(s, "GetSystemDateAndTime") {
		t := time.Now().UTC().Add(c.ClockSkew)
		reply(w, fmt.Sprintf(`<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime><tt:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime></tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`,
			t.Hour(), t.Minute(), t.Second(), t.Year(), int(t.Month()), t.Day()))
		return
	}
	if !c.authorized(body) {
		w.WriteHeader(http.StatusBadRequest)
		reply(w, `<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code><s:Reason><s:Text xml:lang="en">Authority failure</s:Text></s:Reason></s:Fault>`)
		return
	}
	switch {
	case strings.Contains(s, "GetDeviceInformation"):
		reply(w, `<tds:GetDeviceInformationResponse><tds:Manufacturer>`+c.Manufacturer+`</tds:Manufacturer><tds:Model>`+c.Model+`</tds:Model><tds:FirmwareVersion>1.0</tds:FirmwareVersion><tds:SerialNumber>FAKE0001</tds:SerialNumber><tds:HardwareId>1</tds:HardwareId></tds:GetDeviceInformationResponse>`)
	case strings.Contains(s, "GetCapabilities"):
		reply(w, `<tds:GetCapabilitiesResponse><tds:Capabilities><tt:Media><tt:XAddr>http://`+r.Host+`/onvif/media_service</tt:XAddr></tt:Media></tds:Capabilities></tds:GetCapabilitiesResponse>`)
	case strings.Contains(s, "GetProfiles"):
		var b strings.Builder
		b.WriteString(`<trt:GetProfilesResponse>`)
		for _, st := range c.Streams {
			fmt.Fprintf(&b, `<trt:Profiles token="%s" fixed="true"><tt:Name>%s</tt:Name><tt:VideoEncoderConfiguration token="v%s"><tt:Name>v</tt:Name><tt:Encoding>%s</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit></tt:RateControl></tt:VideoEncoderConfiguration><tt:AudioEncoderConfiguration><tt:Encoding>G711</tt:Encoding></tt:AudioEncoderConfiguration></trt:Profiles>`,
				st.Token, st.Name, st.Token, st.Encoding, st.Width, st.Height, st.FPS)
		}
		b.WriteString(`</trt:GetProfilesResponse>`)
		reply(w, b.String())
	case strings.Contains(s, "GetStreamUri"):
		for _, st := range c.Streams {
			if strings.Contains(s, ">"+st.Token+"<") {
				reply(w, `<trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>`+xmlEscape(st.RTSPURL)+`</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect></trt:MediaUri></trt:GetStreamUriResponse>`)
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest)
		reply(w, `<s:Fault><s:Reason><s:Text>no such profile</s:Text></s:Reason></s:Fault>`)
	default:
		w.WriteHeader(http.StatusBadRequest)
		reply(w, `<s:Fault><s:Reason><s:Text>unsupported</s:Text></s:Reason></s:Fault>`)
	}
}

// authorized checks the WS-Security UsernameToken and, like real cameras,
// rejects a Created timestamp more than 5s from the camera's own clock.
func (c *Camera) authorized(body []byte) bool {
	var env struct {
		Header struct {
			Security struct {
				Token struct {
					Username string `xml:"Username"`
					Password string `xml:"Password"`
					Nonce    string `xml:"Nonce"`
					Created  string `xml:"Created"`
				} `xml:"UsernameToken"`
			} `xml:"Security"`
		} `xml:"Header"`
	}
	if xml.NewDecoder(bytes.NewReader(body)).Decode(&env) != nil {
		return false
	}
	t := env.Header.Security.Token
	if t.Username != c.User {
		return false
	}
	created, err := time.Parse("2006-01-02T15:04:05Z", t.Created)
	if err != nil {
		return false
	}
	if d := created.Sub(time.Now().Add(c.ClockSkew)); d > 5*time.Second || d < -5*time.Second {
		return false
	}
	nonce, err := base64.StdEncoding.DecodeString(t.Nonce)
	if err != nil {
		return false
	}
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(t.Created))
	h.Write([]byte(c.Pass))
	return base64.StdEncoding.EncodeToString(h.Sum(nil)) == t.Password
}

func reply(w io.Writer, body string) {
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:ter="http://www.onvif.org/ver10/error"><s:Body>`+body+`</s:Body></s:Envelope>`)
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ProbeMatch is the WS-Discovery reply a camera sends to a Probe.
func ProbeMatch(serviceURL, name, hardware, mfr string) []byte {
	esc := func(s string) string { return strings.ReplaceAll(s, " ", "_") }
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl"><SOAP-ENV:Header><wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</wsa:Action></SOAP-ENV:Header><SOAP-ENV:Body><d:ProbeMatches><d:ProbeMatch><wsa:EndpointReference><wsa:Address>urn:uuid:fake</wsa:Address></wsa:EndpointReference><d:Types>dn:NetworkVideoTransmitter</d:Types><d:Scopes>onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/name/` +
		esc(name) + ` onvif://www.onvif.org/hardware/` + esc(hardware) + ` onvif://www.onvif.org/mfr/` + esc(mfr) +
		`</d:Scopes><d:XAddrs>` + serviceURL + `</d:XAddrs><d:MetadataVersion>1</d:MetadataVersion></d:ProbeMatch></d:ProbeMatches></SOAP-ENV:Body></SOAP-ENV:Envelope>`)
}

// AnswerProbes joins the WS-Discovery group on ifaceName ("" = default) and
// replies to every Probe with ProbeMatch(serviceURL…) until conn closes.
func AnswerProbes(ifaceName, serviceURL, name, hardware, mfr string) (*net.UDPConn, error) {
	var ifc *net.Interface
	if ifaceName != "" {
		i, err := net.InterfaceByName(ifaceName)
		if err != nil {
			return nil, err
		}
		ifc = i
	}
	conn, err := net.ListenMulticastUDP("udp4", ifc, &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702})
	if err != nil {
		return nil, err
	}
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if bytes.Contains(buf[:n], []byte("Probe")) && !bytes.Contains(buf[:n], []byte("ProbeMatches")) {
				_, _ = conn.WriteToUDP(ProbeMatch(serviceURL, name, hardware, mfr), from)
			}
		}
	}()
	return conn, nil
}
