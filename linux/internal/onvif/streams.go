package onvif

// Finding a saved camera's second stream (WAVE22 addendum): ONVIF first,
// then guesses from the main address with the usual vendor patterns, each
// confirmed with an RTSP DESCRIBE that offers a video track. Runs on the
// server that owns the camera (internal/server for the local recorder,
// internal/recorder for a remote one over link.CmdStreamsDiscover).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Discovery limits.
const (
	MaxStreamGuesses      = 6
	StreamDescribeTimeout = 2 * time.Second
	streamsONVIFTimeout   = 25 * time.Second
	// StreamsDiscoverTimeout bounds a whole discovery (ONVIF, then the
	// guesses, which are checked in parallel).
	StreamsDiscoverTimeout = streamsONVIFTimeout + StreamDescribeTimeout + 3*time.Second
)

// Stream sources.
const (
	StreamSourceONVIF = "onvif"
	StreamSourceGuess = "guess"
)

// FoundStream is one stream a camera offers. URL never carries credentials.
type FoundStream struct {
	URL    string  `json:"url"`
	Label  string  `json:"label"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	FPS    float64 `json:"fps"`
	Codec  string  `json:"codec"`  // H264, H265, MJPEG, or "" when unknown
	Source string  `json:"source"` // onvif | guess
	Role   string  `json:"role"`   // main | sub | "": what the camera uses it for now
	// EncoderToken is set when the stream can be resized over ONVIF.
	EncoderToken string `json:"encoderToken,omitempty"`
}

// TooSmallSub is a second stream below 640×360 that ONVIF can resize.
type TooSmallSub struct {
	URL          string `json:"url"`
	EncoderToken string `json:"encoderToken"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

// StreamDiscovery is the answer of POST /api/v2/cameras/{id}/streams/discover.
type StreamDiscovery struct {
	Streams        []FoundStream `json:"streams"`
	RecommendedSub *string       `json:"recommendedSub"`
	TooSmallSub    *TooSmallSub  `json:"tooSmallSub"`
}

// ErrNoVideo: the address answered DESCRIBE without a video track.
var ErrNoVideo = errors.New("the stream has no video track")

// ErrStreamRefused: the camera answered DESCRIBE with an error status (the
// address doesn't exist there); the camera itself was reached.
var ErrStreamRefused = errors.New("the camera has no stream at that address")

// DescribeFunc asks one RTSP address for its description; nil error means
// it offers video, with codec its name (H264, H265, MJPEG or "").
type DescribeFunc func(ctx context.Context, rtspURL, user, pass string) (codec string, err error)

// StreamTarget is the saved camera discovery works on.
type StreamTarget struct {
	ONVIFURL string
	MainURL  string
	SubURL   string
	Username string
	Password string
}

// guessRule rewrites the part of an address after the host. The trailing
// group keeps "stream1" from matching "stream10".
type guessRule struct {
	re   *regexp.Regexp
	repl string
}

var guessRules = []guessRule{
	{regexp.MustCompile(`(?i)(stream)1([^0-9]|$)`), "${1}2${2}"},   // Tapo, many OEMs
	{regexp.MustCompile(`/([1-9])01([^0-9]|$)`), "/${1}02${2}"},    // Hikvision /101, Streaming/Channels/101
	{regexp.MustCompile(`(?i)(subtype=)0([^0-9]|$)`), "${1}1${2}"}, // Dahua, Amcrest
	{regexp.MustCompile(`_main([^a-z]|$)`), "_sub${1}"},            // …_main.sdp
	{regexp.MustCompile(`_Main([^a-z]|$)`), "_Sub${1}"},
	{regexp.MustCompile(`(^|[^a-z])main([^a-z]|$)`), "${1}sub${2}"}, // /h264/ch1/main/av_stream, /live/main
	{regexp.MustCompile(`(^|[^A-Za-z])Main([^a-z]|$)`), "${1}Sub${2}"},
	{regexp.MustCompile(`(?i)(profile)1([^0-9]|$)`), "${1}2${2}"}, // Axis-likes, ONVIF-ish paths
	{regexp.MustCompile(`/ch0_0([^0-9]|$)`), "/ch0_1${1}"},        // /ch0_0.h264
	{regexp.MustCompile(`(?i)(av0_)0([^0-9]|$)`), "${1}1${2}"},    // Foscam-likes /videoMain av0_0
}

// GuessSubURLs proposes second-stream addresses from the main one, in rule
// order, without duplicates and never the main address itself. Only the
// path and query are rewritten; credentials are dropped.
func GuessSubURLs(mainURL string) []string {
	clean := StripUserInfo(strings.TrimSpace(mainURL))
	i := strings.Index(clean, "://")
	if i < 0 {
		return nil
	}
	slash := strings.IndexAny(clean[i+3:], "/?")
	if slash < 0 {
		return nil
	}
	prefix, rest := clean[:i+3+slash], clean[i+3+slash:]
	seen := map[string]bool{streamKey(clean): true}
	var out []string
	for _, r := range guessRules {
		g := r.re.ReplaceAllString(rest, r.repl)
		if g == rest {
			continue
		}
		u := prefix + g
		if k := streamKey(u); !seen[k] {
			seen[k] = true
			out = append(out, u)
		}
	}
	return out
}

// streamKey compares stream addresses by path and query, the way
// streamRole does: cameras report their own host or an alias.
func streamKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(raw) == "" {
		return strings.TrimSpace(raw)
	}
	return u.EscapedPath() + "?" + u.RawQuery
}

// DescribeVideo is the real DescribeFunc: one DESCRIBE over RTSP.
func DescribeVideo(ctx context.Context, rtspURL, user, pass string) (string, error) {
	c, err := DialRTSP(ctx, rtspURL, user, pass)
	if err != nil {
		return "", err
	}
	defer c.Close()
	resp, err := c.Do(ctx, "DESCRIBE", StripUserInfo(rtspURL), map[string]string{"Accept": "application/sdp"})
	if err != nil {
		return "", err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return "", fmt.Errorf("%w (RTSP %d)", ErrStreamRefused, resp.Status)
	}
	codec, ok := sdpVideoCodec(string(resp.Body))
	if !ok {
		return "", ErrNoVideo
	}
	return codec, nil
}

// sdpVideoCodec finds the first m=video section and names its codec.
func sdpVideoCodec(sdp string) (codec string, ok bool) {
	inVideo := false
	var pts []string
	for _, line := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			if inVideo {
				break
			}
			if strings.HasPrefix(line, "m=video ") {
				inVideo, ok = true, true
				if f := strings.Fields(line); len(f) > 3 {
					pts = f[3:]
				}
			}
			continue
		}
		if inVideo && strings.HasPrefix(line, "a=rtpmap:") {
			if _, enc, found := strings.Cut(strings.TrimPrefix(line, "a=rtpmap:"), " "); found {
				name, _, _ := strings.Cut(enc, "/")
				if codec = NormalizeCodec(name); codec != "" {
					return codec, true
				}
			}
		}
	}
	if ok && codec == "" {
		for _, pt := range pts {
			if pt == "26" {
				return "MJPEG", true
			}
		}
	}
	return codec, ok
}

// NormalizeCodec spells a codec the way the API does: H264, H265, MJPEG,
// else the camera's own name upper-cased.
func NormalizeCodec(name string) string {
	switch n := strings.ToUpper(strings.TrimSpace(name)); {
	case n == "":
		return ""
	case n == "H264" || n == "AVC":
		return "H264"
	case n == "H265" || n == "HEVC":
		return "H265"
	case strings.Contains(n, "JPEG"):
		return "MJPEG"
	default:
		return n
	}
}

// StreamLabel is a stream's one-line description, e.g.
// "1280×720 · H.264 · 15 fps".
func StreamLabel(width, height int, codec string, fps float64) string {
	var parts []string
	if width > 0 && height > 0 {
		parts = append(parts, fmt.Sprintf("%d×%d", width, height))
	}
	switch codec {
	case "H264":
		parts = append(parts, "H.264")
	case "H265":
		parts = append(parts, "H.265")
	case "":
	default:
		parts = append(parts, codec)
	}
	if fps > 0 {
		parts = append(parts, fmt.Sprintf("%g fps", fps))
	}
	if len(parts) == 0 {
		return "Video stream"
	}
	return strings.Join(parts, " · ")
}

func isVideoCodec(c string) bool { return c == "H264" || c == "H265" }

// rtspHost is the camera's host from its main address, for ONVIF by host.
func rtspHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// fetchForStreams signs in over ONVIF: the saved address first, then the
// usual ports on the camera's host.
func fetchForStreams(ctx context.Context, t StreamTarget) (*Details, error) {
	host := rtspHost(t.MainURL)
	if t.ONVIFURL != "" {
		d, err := FetchDetails(ctx, t.ONVIFURL, t.Username, t.Password)
		if err == nil || errors.Is(err, ErrAuth) || host == "" {
			return d, err
		}
		if d2, err2 := FetchByHost(ctx, host, t.Username, t.Password); err2 == nil || errors.Is(err2, ErrAuth) {
			return d2, err2
		}
		return nil, err
	}
	if host == "" {
		return nil, errors.New("the camera's main stream address has no host")
	}
	return FetchByHost(ctx, host, t.Username, t.Password)
}

// DiscoverStreams lists the camera's streams and recommends a second one.
// describe checks guesses (nil = DescribeVideo). The error wraps ErrAuth
// when the camera rejected the login and nothing was found.
func DiscoverStreams(ctx context.Context, t StreamTarget, describe DescribeFunc) (*StreamDiscovery, error) {
	if describe == nil {
		describe = DescribeVideo
	}
	mainKey := streamKey(StripUserInfo(t.MainURL))
	subKey := ""
	if strings.TrimSpace(t.SubURL) != "" {
		subKey = streamKey(StripUserInfo(t.SubURL))
	}
	role := func(u string) string {
		switch streamKey(u) {
		case mainKey:
			return "main"
		case subKey:
			return "sub"
		}
		return ""
	}

	out := &StreamDiscovery{Streams: []FoundStream{}}
	seen := map[string]bool{}

	octx, cancel := context.WithTimeout(ctx, streamsONVIFTimeout)
	d, onvifErr := fetchForStreams(octx, t)
	cancel()
	extra := false
	if onvifErr == nil && d != nil {
		for _, p := range d.Profiles {
			u := StripUserInfo(p.RTSPURL)
			k := streamKey(u)
			if u == "" || seen[k] {
				continue
			}
			seen[k] = true
			codec := NormalizeCodec(p.Encoding)
			s := FoundStream{URL: u, Label: StreamLabel(p.Width, p.Height, codec, float64(p.FPS)), Width: p.Width, Height: p.Height,
				FPS: float64(p.FPS), Codec: codec, Source: StreamSourceONVIF, Role: role(u), EncoderToken: p.EncoderToken}
			out.Streams = append(out.Streams, s)
			if s.Role != "main" && isVideoCodec(codec) {
				extra = true
			}
		}
	}

	authFailed := errors.Is(onvifErr, ErrAuth)
	tried, reached := 0, false
	if !extra {
		var guesses []string
		for _, g := range GuessSubURLs(t.MainURL) {
			if !seen[streamKey(g)] {
				guesses = append(guesses, g)
			}
		}
		if len(guesses) > MaxStreamGuesses {
			guesses = guesses[:MaxStreamGuesses]
		}
		codecs := make([]string, len(guesses))
		errs := make([]error, len(guesses))
		var wg sync.WaitGroup
		for i, g := range guesses {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dctx, cancel := context.WithTimeout(ctx, StreamDescribeTimeout)
				defer cancel()
				codecs[i], errs[i] = describe(dctx, g, t.Username, t.Password)
			}()
		}
		wg.Wait()
		tried = len(guesses)
		for i, g := range guesses {
			if errs[i] != nil {
				authFailed = authFailed || errors.Is(errs[i], ErrAuth)
				reached = reached || errors.Is(errs[i], ErrNoVideo) || errors.Is(errs[i], ErrStreamRefused)
				continue
			}
			reached = true
			seen[streamKey(g)] = true
			out.Streams = append(out.Streams, FoundStream{URL: g, Label: StreamLabel(0, 0, codecs[i], 0), Codec: codecs[i], Source: StreamSourceGuess, Role: role(g)})
		}
	}

	// Nothing found: an error only when the camera refused the login or
	// couldn't be reached at all; a camera that simply has one stream (or
	// an address no pattern fits) is an empty, successful answer.
	if len(out.Streams) == 0 && !reached {
		switch {
		case authFailed:
			return nil, ErrAuth
		case onvifErr != nil && (t.ONVIFURL != "" || tried > 0):
			return nil, fmt.Errorf("couldn't reach the camera: %w", onvifErr)
		}
	}
	recommend(out, mainKey)
	return out, nil
}

// recommend fills RecommendedSub and TooSmallSub: the smallest H.264/H.265
// stream of at least 640×360 that isn't the main one, else the smallest
// sized one (offered for resizing when ONVIF can), else the first guess
// whose codec isn't known to be unusable.
func recommend(d *StreamDiscovery, mainKey string) {
	var best, smallest, guess *FoundStream
	for i := range d.Streams {
		s := &d.Streams[i]
		if streamKey(s.URL) == mainKey {
			continue
		}
		px := s.Width * s.Height
		switch {
		case px > 0 && isVideoCodec(s.Codec):
			if smallest == nil || px < smallest.Width*smallest.Height {
				smallest = s
			}
			if s.Width >= MinSubWidth && s.Height >= MinSubHeight && (best == nil || px < best.Width*best.Height) {
				best = s
			}
		case px == 0 && guess == nil && (s.Codec == "" || isVideoCodec(s.Codec)):
			guess = s
		}
	}
	pick := best
	if pick == nil {
		pick = smallest
		if pick != nil && pick.EncoderToken != "" {
			d.TooSmallSub = &TooSmallSub{URL: pick.URL, EncoderToken: pick.EncoderToken, Width: pick.Width, Height: pick.Height}
		}
	}
	if pick == nil {
		pick = guess
	}
	if pick != nil {
		u := pick.URL
		d.RecommendedSub = &u
	}
}
