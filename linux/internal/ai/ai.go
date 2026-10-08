// Package ai is Sentinel's bring-your-own-key Claude integration: scene
// descriptions for detections, natural-language event search and a daily
// digest. Calls go straight from this server to the Anthropic API with the
// operator's key — no Sentinel backend. Prompts, models and output shapes are
// ported from the Mac app's SentinelAIClient so both behave the same.
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

const (
	// VisionModel: fast, inexpensive and plenty for clothing colors, carried
	// objects, actions and a coarse routine-vs-suspicious read (same as Mac).
	VisionModel = "claude-haiku-4-5"
	// TextModel: stronger reasoning for search and digests, at low effort.
	TextModel = "claude-sonnet-5-5"

	MaxFrames       = 5
	MaxSearchEvents = 600
	MaxDigestEvents = 400
	requestTimeout  = 90 * time.Second
)

var (
	ErrNoKey   = errors.New("no Anthropic API key set — add your key in Settings → AI")
	ErrRefused = errors.New("Claude declined this request — try rephrasing it")
)

type Client struct{ c anthropic.Client }

func New(apiKey string, extra ...option.RequestOption) *Client {
	opts := append([]option.RequestOption{option.WithAPIKey(strings.TrimSpace(apiKey)), option.WithRequestTimeout(requestTimeout), option.WithMaxRetries(2)}, extra...)
	return &Client{c: anthropic.NewClient(opts...)}
}

// Validate checks a key with a token-free models listing.
func Validate(ctx context.Context, apiKey string, extra ...option.RequestOption) error {
	if strings.TrimSpace(apiKey) == "" {
		return ErrNoKey
	}
	c := New(apiKey, extra...)
	_, err := c.c.Models.List(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1)})
	return friendly(err)
}

// friendly turns API errors into operator-readable sentences.
func friendly(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return errors.New("Anthropic rejected the API key — check it in Settings → AI")
		case http.StatusTooManyRequests:
			return errors.New("Anthropic rate limit reached — try again shortly")
		case 529:
			return errors.New("Anthropic is temporarily overloaded — try again shortly")
		}
		return fmt.Errorf("Claude request failed (%d)", apiErr.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Claude took too long to answer — try again")
	}
	return fmt.Errorf("couldn't reach Anthropic: %w", err)
}

// MARK: - Scene analysis (vision)

const describePrompt = "You are a security camera assistant. The image(s) are consecutive frames from ONE event, " +
	"oldest first. Describe the person(s) and what happens across the frames for a security alert, " +
	"and judge how suspicious it looks. Reply ONLY with a JSON object and nothing else:\n" +
	"{\"description\": \"<one short sentence, max ~20 words; clothing colors, carried items, action>\", " +
	"\"threat\": \"none|low|elevated|high\", " +
	"\"anomaly\": <true if this looks unusual or worth attention, else false>, " +
	"\"reason\": \"<short reason for the threat level, max ~12 words>\", " +
	"\"tags\": [\"<a few short tags: e.g. person, vehicle, package, night, loitering>\"]}\n" +
	"Routine activity (a resident, a normal delivery) is threat \"none\" or \"low\". Reserve " +
	"\"elevated\"/\"high\" for things like loitering, testing doors/handles, masked faces, or " +
	"forced entry. If no person is clearly visible, set description to 'No person clearly visible.' " +
	"and threat to 'none'."

// vehiclePrompt adapts the Mac prompt for vehicle events (the Mac only
// describes people; Linux also describes vehicles).
const vehiclePrompt = "You are a security camera assistant. The image(s) are consecutive frames from ONE event, " +
	"oldest first. Describe the vehicle(s) and what they do for a security alert, and judge how suspicious it looks. " +
	"Reply ONLY with a JSON object and nothing else:\n" +
	"{\"description\": \"<one short sentence, max ~20 words; vehicle type, color, action, any people>\", " +
	"\"threat\": \"none|low|elevated|high\", \"anomaly\": <true|false>, " +
	"\"reason\": \"<short reason, max ~12 words>\", \"tags\": [\"<a few short tags>\"]}\n" +
	"Routine traffic, parking and deliveries are threat \"none\" or \"low\". If no vehicle is clearly visible, " +
	"set description to 'No vehicle clearly visible.' and threat to 'none'."

type Analysis struct {
	Description string   `json:"description"`
	Threat      string   `json:"threat"`
	Anomaly     bool     `json:"anomaly"`
	Reason      string   `json:"reason"`
	Tags        []string `json:"tags"`
}

// AnalyzeScene sends up to MaxFrames consecutive JPEGs of one event.
func (c *Client) AnalyzeScene(ctx context.Context, jpegs [][]byte, kind string) (*Analysis, error) {
	if len(jpegs) == 0 {
		return nil, errors.New("no frames")
	}
	if len(jpegs) > MaxFrames {
		jpegs = jpegs[:MaxFrames]
	}
	prompt := describePrompt
	if kind == "Vehicle" {
		prompt = vehiclePrompt
	}
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(jpegs)+1)
	for _, j := range jpegs {
		blocks = append(blocks, anthropic.NewImageBlockBase64("image/jpeg", base64.StdEncoding.EncodeToString(j)))
	}
	blocks = append(blocks, anthropic.NewTextBlock(prompt))
	msg, err := c.c.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     VisionModel,
		MaxTokens: 400,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
	})
	if err != nil {
		return nil, friendly(err)
	}
	if msg.StopReason == anthropic.StopReasonRefusal {
		return nil, ErrRefused
	}
	text := ""
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			text = t.Text
			break
		}
	}
	a := &Analysis{Description: strings.TrimSpace(text), Threat: "none"}
	if obj := firstJSONObject(text); obj != nil {
		var parsed Analysis
		if json.Unmarshal(obj, &parsed) == nil && parsed.Description != "" {
			a = &parsed
		}
	}
	switch a.Threat {
	case "none", "low", "elevated", "high":
	default:
		a.Threat = "none"
	}
	if len(a.Tags) > 6 {
		a.Tags = a.Tags[:6]
	}
	return a, nil
}

// MARK: - Search & digest (text)

// EventLine is one event as the text models see it.
type EventLine struct {
	ID          string
	Time        time.Time
	Camera      string
	Kind        string
	Description string
	Tags        string
	Threat      string
}

func (e EventLine) format(index int) string {
	var b strings.Builder
	if index >= 0 {
		b.WriteString("[" + strconv.Itoa(index) + "] ")
	}
	b.WriteString(e.Time.Format("Mon Jan 2 2006 3:04 PM") + " · " + e.Camera + " · " + e.Kind)
	if e.Description != "" {
		b.WriteString(" — " + e.Description)
	}
	if e.Tags != "" {
		b.WriteString(" [tags: " + e.Tags + "]")
	}
	if e.Threat != "" && e.Threat != "none" {
		b.WriteString(" [threat: " + e.Threat + "]")
	}
	return b.String()
}

const digestSystem = "You are the security analyst for a self-hosted home/business camera system. " +
	"Given a time-ordered list of detection events (each with a time, camera, type, " +
	"and a short description), write a brief situational digest the owner can read in " +
	"ten seconds. Group similar activity, call out anything unusual or worth attention " +
	"(loitering, unfamiliar vehicles, night activity, repeated visits), and stay factual " +
	"— never invent details that aren't in the events. If nothing notable happened, say so plainly."

// Digest summarizes a window of events ("What happened today?").
func (c *Client) Digest(ctx context.Context, label string, events []EventLine) (string, error) {
	if len(events) == 0 {
		return "No detection events " + label + ".", nil
	}
	shown := events
	if len(shown) > MaxDigestEvents {
		shown = shown[:MaxDigestEvents]
	}
	lines := make([]string, len(shown))
	for i, e := range shown {
		lines[i] = e.format(-1)
	}
	head := fmt.Sprintf("Detection events for %s (%d total", label, len(events))
	if len(events) > len(shown) {
		head += fmt.Sprintf(", showing the first %d", len(shown))
	}
	user := head + "):\n\n" + strings.Join(lines, "\n") + "\n\nWrite the digest now. Start with a one-line headline, then 2–5 short bullet points."
	return c.text(ctx, digestSystem, user, 4000)
}

const searchSystem = "You search a security camera's detection-event log. You are given a user query and a " +
	"numbered list of events (each: index, time, camera, type, description). Return ONLY the " +
	"events that genuinely match the query's intent — match on described attributes (clothing " +
	"colors, carried objects, vehicles, actions), camera, type, and time. Be precise: do not " +
	"return weak matches. Reply with a single JSON object and nothing else: " +
	"{\"answer\": \"<one short sentence answering the query>\", \"matches\": [<event indexes>]}. " +
	"If nothing matches, return an empty matches array and say so in the answer."

// Search answers a natural-language query over events and returns the ids of
// the matching events.
func (c *Client) Search(ctx context.Context, query string, events []EventLine, now time.Time) (string, []string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", nil, errors.New("type what you're looking for")
	}
	if len(events) == 0 {
		return "No events to search.", nil, nil
	}
	if len(events) > MaxSearchEvents {
		events = events[len(events)-MaxSearchEvents:] // most recent
	}
	lines := make([]string, len(events))
	for i, e := range events {
		lines[i] = e.format(i)
	}
	user := "Current local time: " + now.Format("Mon Jan 2 2006 3:04 PM") + "\n\nQuery: " + query + "\n\nEvents:\n" + strings.Join(lines, "\n")
	raw, err := c.text(ctx, searchSystem, user, 6000)
	if err != nil {
		return "", nil, err
	}
	answer := raw
	var ids []string
	if obj := firstJSONObject(raw); obj != nil {
		var parsed struct {
			Answer  string `json:"answer"`
			Matches []any  `json:"matches"`
		}
		if json.Unmarshal(obj, &parsed) == nil {
			if parsed.Answer != "" {
				answer = parsed.Answer
			}
			for _, m := range parsed.Matches {
				var i int
				switch v := m.(type) {
				case float64:
					i = int(v)
				case string:
					i, _ = strconv.Atoi(v)
				default:
					continue
				}
				if i >= 0 && i < len(events) {
					ids = append(ids, events[i].ID)
				}
			}
		}
	}
	return strings.TrimSpace(answer), ids, nil
}

// text runs a text request on TextModel at low effort, with the server-side
// refusal fallback (a declined request is re-served by Anthropic's
// recommended model instead of failing) — same setup as the Mac.
func (c *Client) text(ctx context.Context, system, user string, maxTokens int64) (string, error) {
	msg, err := c.c.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:        TextModel,
		MaxTokens:    maxTokens,
		System:       []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user))},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow},
		Fallbacks:    anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		return "", friendly(err)
	}
	// A refusal (after any fallback also declined) must never be read as an answer.
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		return "", ErrRefused
	}
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			return strings.TrimSpace(t.Text), nil
		}
	}
	return "", errors.New("unexpected response from Claude")
}

// firstJSONObject extracts the outermost {...} from model text (tolerates
// code fences and stray prose), as the Mac client does.
func firstJSONObject(text string) []byte {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil
	}
	return []byte(text[start : end+1])
}
