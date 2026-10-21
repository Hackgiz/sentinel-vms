package semantic

// The CLIP byte-pair tokenizer, in pure Go, matching the Hugging Face
// "fast" CLIP tokenizer the Xenova/clip-vit-base-patch32 export ships with:
//
//   - normalize: NFC, runs of whitespace to one space, lower-case;
//   - split with CLIP's pattern ('s 't 're 've 'm 'll 'd, letter runs,
//     single digits, runs of other non-space characters);
//   - map each piece's UTF-8 bytes to GPT-2's printable byte alphabet;
//   - byte-pair merge (merges.txt, lowest rank first) with "</w>" marking
//     the end of a word, then look the symbols up in vocab.json;
//   - wrap in <|startoftext|> … <|endoftext|> and pad to 77 tokens.
//
// Text is always text: "<|endoftext|>" typed by a user is tokenized as
// characters, never as the special token.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"
)

// ContextLen is the CLIP text tower's sequence length.
const ContextLen = 77

// Tokenizer is safe for concurrent use.
type Tokenizer struct {
	vocab    map[string]int
	ranks    map[[2]string]int
	byteRune [256]rune
	sot, eot int

	mu    sync.Mutex
	cache map[string][]int
}

const tokenizerCacheMax = 10_000

var (
	clipPattern = regexp.MustCompile(`'s|'t|'re|'ve|'m|'ll|'d|\p{L}+|\p{N}|[^\s\p{L}\p{N}]+`)
	spaceRuns   = regexp.MustCompile(`[\s\p{Z}\x{85}]+`) // Unicode whitespace, as the HF normalizer
)

// LoadTokenizer reads CLIP's vocab.json and merges.txt.
func LoadTokenizer(vocabPath, mergesPath string) (*Tokenizer, error) {
	raw, err := os.ReadFile(vocabPath)
	if err != nil {
		return nil, err
	}
	var vocab map[string]int
	if err := json.Unmarshal(raw, &vocab); err != nil {
		return nil, fmt.Errorf("vocab: %w", err)
	}
	f, err := os.Open(mergesPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var merges []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		merges = append(merges, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("merges: %w", err)
	}
	return NewTokenizer(vocab, merges)
}

// NewTokenizer builds a tokenizer from a vocabulary and merge lines ("a b";
// a "#version" header and blank lines are skipped).
func NewTokenizer(vocab map[string]int, merges []string) (*Tokenizer, error) {
	t := &Tokenizer{vocab: vocab, ranks: make(map[[2]string]int, len(merges)), cache: map[string][]int{}}
	var ok1, ok2 bool
	if t.sot, ok1 = vocab["<|startoftext|>"]; !ok1 {
		return nil, errors.New("vocab has no <|startoftext|> token")
	}
	if t.eot, ok2 = vocab["<|endoftext|>"]; !ok2 {
		return nil, errors.New("vocab has no <|endoftext|> token")
	}
	for _, m := range merges {
		if m == "" || strings.HasPrefix(m, "#version") {
			continue
		}
		a, b, ok := strings.Cut(m, " ")
		if !ok || a == "" || b == "" {
			return nil, fmt.Errorf("bad merge line %q", m)
		}
		k := [2]string{a, b}
		if _, dup := t.ranks[k]; !dup {
			t.ranks[k] = len(t.ranks)
		}
	}
	if len(t.ranks) == 0 {
		return nil, errors.New("no merges")
	}
	t.byteRune = bytesToUnicode()
	return t, nil
}

// bytesToUnicode is GPT-2's reversible byte → printable rune table.
func bytesToUnicode() [256]rune {
	var out [256]rune
	var used [256]bool
	for _, r := range [][2]int{{'!', '~'}, {0xA1, 0xAC}, {0xAE, 0xFF}} {
		for b := r[0]; b <= r[1]; b++ {
			out[b], used[b] = rune(b), true
		}
	}
	n := 0
	for b := 0; b < 256; b++ {
		if !used[b] {
			out[b] = rune(256 + n)
			n++
		}
	}
	return out
}

// Normalize is the text clean-up applied before splitting.
func Normalize(text string) string {
	text = norm.NFC.String(text)
	text = spaceRuns.ReplaceAllString(text, " ")
	return strings.ToLower(text)
}

// Encode returns the token ids of text, wrapped in the start and end
// tokens, without padding. Text longer than the context is cut so the end
// token still fits.
func (t *Tokenizer) Encode(text string) []int {
	ids := []int{t.sot}
	for _, piece := range clipPattern.FindAllString(Normalize(text), -1) {
		ids = append(ids, t.bpe(piece)...)
		if len(ids) >= ContextLen-1 {
			ids = ids[:ContextLen-1]
			break
		}
	}
	return append(ids, t.eot)
}

// Tokens is Encode padded to ContextLen with the end token (as the
// Hugging Face tokenizer pads CLIP), ready for the text tower's input_ids.
func (t *Tokenizer) Tokens(text string) []int64 {
	ids := t.Encode(text)
	out := make([]int64, ContextLen)
	for i := range out {
		if i < len(ids) {
			out[i] = int64(ids[i])
		} else {
			out[i] = int64(t.eot)
		}
	}
	return out
}

// bpe turns one split piece into token ids.
func (t *Tokenizer) bpe(piece string) []int {
	t.mu.Lock()
	if ids, ok := t.cache[piece]; ok {
		t.mu.Unlock()
		return ids
	}
	t.mu.Unlock()

	// Symbols: one per byte, in the printable alphabet; the last carries </w>.
	b := []byte(piece)
	word := make([]string, len(b))
	for i, c := range b {
		word[i] = string(t.byteRune[c])
	}
	if len(word) == 0 {
		return nil
	}
	word[len(word)-1] += "</w>"
	for len(word) > 1 {
		best, at := -1, -1
		for i := 0; i+1 < len(word); i++ {
			if r, ok := t.ranks[[2]string{word[i], word[i+1]}]; ok && (best < 0 || r < best) {
				best, at = r, i
			}
		}
		if at < 0 {
			break
		}
		first, second := word[at], word[at+1]
		merged := make([]string, 0, len(word)-1)
		for i := 0; i < len(word); {
			if i+1 < len(word) && word[i] == first && word[i+1] == second {
				merged = append(merged, first+second)
				i += 2
			} else {
				merged = append(merged, word[i])
				i++
			}
		}
		word = merged
	}
	ids := make([]int, 0, len(word))
	for _, sym := range word {
		if id, ok := t.vocab[sym]; ok {
			ids = append(ids, id)
		} else {
			ids = append(ids, t.eot) // unk_token is <|endoftext|>
		}
	}
	t.mu.Lock()
	if len(t.cache) >= tokenizerCacheMax {
		clear(t.cache)
	}
	t.cache[piece] = ids
	t.mu.Unlock()
	return ids
}
