package fakecam

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Video encoder service: GetProfiles carries each stream's full
// VideoEncoderConfiguration (config token "v"+stream token),
// GetVideoEncoderConfiguration[Options] answer from it, and
// SetVideoEncoderConfiguration validates against the options, updates the
// stream and records the request body for tests (EncoderSets).

// EncoderOptions are the ranges the camera advertises; zero fields default to
// 1920x1080/1280x720/640x360, 1-30 fps, 64-16384 kbit/s, GOP 1-120, quality 1-10.
type EncoderOptions struct {
	Resolutions [][2]int
	FPSMin, FPSMax,
	BitrateMin, BitrateMax,
	GovMin, GovMax,
	QualityMin, QualityMax int
}

func (o EncoderOptions) withDefaults() EncoderOptions {
	if len(o.Resolutions) == 0 {
		o.Resolutions = [][2]int{{1920, 1080}, {1280, 720}, {640, 360}}
	}
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	def(&o.FPSMin, 1)
	def(&o.FPSMax, 30)
	def(&o.BitrateMin, 64)
	def(&o.BitrateMax, 16384)
	def(&o.GovMin, 1)
	def(&o.GovMax, 120)
	def(&o.QualityMin, 1)
	def(&o.QualityMax, 10)
	return o
}

var (
	reCfgToken = regexp.MustCompile(`Configuration[^>]*\btoken="([^"]*)"`)
	reTokenArg = regexp.MustCompile(`<trt:ConfigurationToken>([^<]*)</trt:ConfigurationToken>`)
)

// EncoderSets returns the raw SetVideoEncoderConfiguration bodies received.
func (c *Camera) EncoderSets() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.encSets...)
}

// Stream returns a copy of the stream with the given token.
func (c *Camera) Stream(token string) (Stream, bool) {
	for _, st := range c.streamsSnapshot() {
		if st.Token == token {
			return st, true
		}
	}
	return Stream{}, false
}

func (c *Camera) streamsSnapshot() []Stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Stream(nil), c.Streams...)
}

func encoderConfigXML(st Stream, tag string) string {
	bitrate, gov, q := st.Bitrate, st.GovLength, st.Quality
	if bitrate == 0 {
		bitrate = 4096
	}
	if gov == 0 {
		gov = 30
	}
	if q == 0 {
		q = 5
	}
	codec := ""
	if strings.EqualFold(st.Encoding, "H264") {
		codec = fmt.Sprintf(`<tt:H264><tt:GovLength>%d</tt:GovLength><tt:H264Profile>Main</tt:H264Profile></tt:H264>`, gov)
	}
	return fmt.Sprintf(`<%[1]s token="v%[2]s" GuaranteedFrameRate="false"><tt:Name>v</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%[3]s</tt:Encoding><tt:Resolution><tt:Width>%[4]d</tt:Width><tt:Height>%[5]d</tt:Height></tt:Resolution><tt:Quality>%[6]d</tt:Quality><tt:RateControl><tt:FrameRateLimit>%[7]d</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval><tt:BitrateLimit>%[8]d</tt:BitrateLimit></tt:RateControl>%[9]s<tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address><tt:Port>0</tt:Port><tt:TTL>5</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast><tt:SessionTimeout>PT60S</tt:SessionTimeout></%[1]s>`,
		tag, st.Token, st.Encoding, st.Width, st.Height, q, st.FPS, bitrate, codec)
}

func (c *Camera) optionsXML(st Stream) string {
	o := c.EncoderOptions.withDefaults()
	var res strings.Builder
	for _, r := range o.Resolutions {
		fmt.Fprintf(&res, `<tt:ResolutionsAvailable><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:ResolutionsAvailable>`, r[0], r[1])
	}
	group := st.Encoding
	if group == "" {
		group = "H264"
	}
	ext := ""
	if !c.NoBitrateRange {
		ext = fmt.Sprintf(`<tt:Extension><tt:%[1]s><tt:BitrateRange><tt:Min>%[2]d</tt:Min><tt:Max>%[3]d</tt:Max></tt:BitrateRange></tt:%[1]s></tt:Extension>`, group, o.BitrateMin, o.BitrateMax)
	}
	gov := ""
	if strings.EqualFold(st.Encoding, "H264") {
		gov = fmt.Sprintf(`<tt:GovLengthRange><tt:Min>%d</tt:Min><tt:Max>%d</tt:Max></tt:GovLengthRange>`, o.GovMin, o.GovMax)
	}
	return fmt.Sprintf(`<trt:GetVideoEncoderConfigurationOptionsResponse><trt:Options><tt:QualityRange><tt:Min>%[1]d</tt:Min><tt:Max>%[2]d</tt:Max></tt:QualityRange><tt:%[3]s>%[4]s%[5]s<tt:FrameRateRange><tt:Min>%[6]d</tt:Min><tt:Max>%[7]d</tt:Max></tt:FrameRateRange><tt:EncodingIntervalRange><tt:Min>1</tt:Min><tt:Max>1</tt:Max></tt:EncodingIntervalRange></tt:%[3]s>%[8]s</trt:Options></trt:GetVideoEncoderConfigurationOptionsResponse>`,
		o.QualityMin, o.QualityMax, group, res.String(), gov, o.FPSMin, o.FPSMax, ext)
}

func fault(w http.ResponseWriter, text string) {
	w.WriteHeader(http.StatusBadRequest)
	reply(w, `<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:InvalidArgVal</s:Value></s:Subcode></s:Code><s:Reason><s:Text xml:lang="en">`+xmlEscape(text)+`</s:Text></s:Reason></s:Fault>`)
}

func tagInt(s, tag string) (int, bool) {
	m := regexp.MustCompile(`<tt:` + tag + `>\s*([0-9.]+)\s*</tt:` + tag + `>`).FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	return int(f), err == nil
}

// serveEncoder answers a video-encoder request s and reports whether it was one.
func (c *Camera) serveEncoder(w http.ResponseWriter, s string) bool {
	find := func(arg string) (Stream, bool) {
		m := reTokenArg.FindStringSubmatch(arg)
		if m == nil {
			return Stream{}, false
		}
		return c.Stream(strings.TrimPrefix(m[1], "v"))
	}
	switch {
	case strings.Contains(s, "GetVideoEncoderConfigurationOptions"):
		st, ok := find(s)
		if !ok {
			fault(w, "no such configuration")
			return true
		}
		reply(w, c.optionsXML(st))
	case strings.Contains(s, "SetVideoEncoderConfiguration"):
		m := reCfgToken.FindStringSubmatch(s)
		if m == nil || !strings.Contains(s, "<trt:ForcePersistence>true</trt:ForcePersistence>") {
			fault(w, "incomplete configuration")
			return true
		}
		// Real cameras reject a partial configuration; require the fields we echo.
		for _, need := range []string{"Name", "Encoding", "Resolution", "Quality", "RateControl", "Multicast", "SessionTimeout", "UseCount"} {
			if !strings.Contains(s, "<tt:"+need+">") {
				fault(w, "incomplete configuration: missing "+need)
				return true
			}
		}
		o := c.EncoderOptions.withDefaults()
		width, _ := tagInt(s, "Width")
		height, _ := tagInt(s, "Height")
		fps, _ := tagInt(s, "FrameRateLimit")
		bitrate, _ := tagInt(s, "BitrateLimit")
		quality, _ := tagInt(s, "Quality")
		gov, hasGov := tagInt(s, "GovLength")
		okRes := false
		for _, r := range o.Resolutions {
			okRes = okRes || (r[0] == width && r[1] == height)
		}
		switch {
		case !okRes:
			fault(w, fmt.Sprintf("resolution %dx%d is not supported by this camera", width, height))
			return true
		case fps < o.FPSMin || fps > o.FPSMax:
			fault(w, "frame rate out of range")
			return true
		case bitrate < o.BitrateMin || bitrate > o.BitrateMax:
			fault(w, "bitrate out of range")
			return true
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for i := range c.Streams {
			if "v"+c.Streams[i].Token != m[1] {
				continue
			}
			st := &c.Streams[i]
			st.Width, st.Height, st.FPS, st.Bitrate, st.Quality = width, height, fps, bitrate, quality
			if hasGov {
				st.GovLength = gov
			}
			c.encSets = append(c.encSets, s)
			reply(w, `<trt:SetVideoEncoderConfigurationResponse/>`)
			return true
		}
		fault(w, "no such configuration")
	case strings.Contains(s, "GetVideoEncoderConfiguration"):
		if strings.Contains(s, "GetVideoEncoderConfigurations") {
			return false
		}
		st, ok := find(s)
		if !ok {
			fault(w, "no such configuration")
			return true
		}
		reply(w, `<trt:GetVideoEncoderConfigurationResponse>`+encoderConfigXML(st, "trt:Configuration")+`</trt:GetVideoEncoderConfigurationResponse>`)
	default:
		return false
	}
	return true
}
