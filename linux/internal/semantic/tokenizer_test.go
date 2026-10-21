package semantic

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Reference ids from the Hugging Face tokenizer (tokenizers 0.22.2, the
// tokenizer.json of Xenova/clip-vit-base-patch32), run once in Python:
//
//	Tokenizer.from_file("tokenizer.json").encode(text).ids
var referenceIDs = []struct {
	text string
	ids  []int
}{
	{"a photo of a cat", []int{49406, 320, 1125, 539, 320, 2368, 49407}},
	{"A Photo of a CAT!", []int{49406, 320, 1125, 539, 320, 2368, 256, 49407}},
	{"red pickup truck backing up", []int{49406, 736, 15382, 4629, 14935, 705, 49407}},
	{"person's dog at 3:45pm, near the gate", []int{49406, 2533, 568, 1929, 536, 274, 281, 275, 276, 990, 267, 2252, 518, 3302, 49407}},
	{"  hello   world  ", []int{49406, 3306, 1002, 49407}},
	{"café naïve 日本 🙂", []int{49406, 15304, 1097, 35689, 563, 39121, 19277, 361, 14860, 49407}},
	{"don't you'll they're", []int{49406, 847, 713, 592, 1342, 889, 982, 49407}},
	{"abc123def 4567", []int{49406, 5334, 272, 273, 274, 11649, 275, 276, 277, 278, 49407}},
	{"person-in-a-red-shirt", []int{49406, 2533, 268, 530, 268, 320, 268, 736, 268, 2523, 49407}},
}

func referenceTexts() []string {
	var out []string
	for _, r := range referenceIDs {
		out = append(out, r.text)
	}
	return out
}

func checkReference(t *testing.T, tok *Tokenizer) {
	t.Helper()
	for _, r := range referenceIDs {
		if got := tok.Encode(r.text); !reflect.DeepEqual(got, r.ids) {
			t.Errorf("Encode(%q) = %v, want %v", r.text, got, r.ids)
		}
	}
}

// The committed testdata is the subset of the real vocab.json and
// merges.txt these texts touch (same ids, same merge order), so the known
// ids are checked on every run without shipping the tokenizer files.
func TestTokenizerKnownIDs(t *testing.T) {
	tok, err := LoadTokenizer("testdata/clip_mini_vocab.json", "testdata/clip_mini_merges.txt")
	if err != nil {
		t.Fatal(err)
	}
	checkReference(t, tok)
}

// The same against the full files when they are installed.
func TestTokenizerKnownIDsFullVocab(t *testing.T) {
	f := realFiles(t)
	tok, err := LoadTokenizer(f.Vocab, f.Merges)
	if err != nil {
		t.Fatal(err)
	}
	checkReference(t, tok)
	// Typed special tokens are text, not control tokens.
	ids := tok.Encode("<|endoftext|> x")
	for _, id := range ids[1 : len(ids)-1] {
		if id == tok.eot || id == tok.sot {
			t.Fatalf("typed special token became a control token: %v", ids)
		}
	}
}

func TestTokensPadAndTruncate(t *testing.T) {
	tok, err := LoadTokenizer("testdata/clip_mini_vocab.json", "testdata/clip_mini_merges.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := tok.Tokens("a photo of a cat")
	if len(got) != ContextLen {
		t.Fatalf("len %d", len(got))
	}
	want := []int64{49406, 320, 1125, 539, 320, 2368, 49407}
	if !reflect.DeepEqual(got[:7], want) {
		t.Fatalf("head %v", got[:7])
	}
	for _, v := range got[7:] {
		if v != 49407 {
			t.Fatalf("padding %v", got)
		}
	}
	long := tok.Tokens(strings.Repeat("a photo of a cat ", 40))
	if len(long) != ContextLen || long[0] != 49406 || long[ContextLen-1] != 49407 || long[ContextLen-2] == 49407 {
		t.Fatalf("truncated %v", long)
	}
	if e := tok.Encode(""); !reflect.DeepEqual(e, []int{49406, 49407}) {
		t.Fatalf("empty %v", e)
	}
}

func TestTokenizerRejectsBadFiles(t *testing.T) {
	if _, err := NewTokenizer(map[string]int{"a": 1}, []string{"a b"}); err == nil {
		t.Fatal("vocab without special tokens accepted")
	}
	v := map[string]int{"<|startoftext|>": 1, "<|endoftext|>": 2}
	if _, err := NewTokenizer(v, []string{"#version: 0.2"}); err == nil {
		t.Fatal("no merges accepted")
	}
	if _, err := NewTokenizer(v, []string{"ab"}); err == nil {
		t.Fatal("bad merge line accepted")
	}
	if _, err := LoadTokenizer(filepath.Join(t.TempDir(), "nope.json"), "x"); err == nil {
		t.Fatal("missing file accepted")
	}
}

// realFiles finds the CLIP files: the SENTINEL_CLIP_* overrides, or the
// git-ignored linux/.models-cache. It skips the test when they are absent.
func realFiles(t *testing.T) Files {
	t.Helper()
	f := Paths("")
	cache, _ := filepath.Abs("../../.models-cache")
	def := Files{Image: filepath.Join(cache, ImageModelFile), Text: filepath.Join(cache, TextModelFile),
		Vocab: filepath.Join(cache, VocabFile), Merges: filepath.Join(cache, MergesFile)}
	if os.Getenv(ImageModelEnv) == "" {
		f.Image = def.Image
	}
	if os.Getenv(TextModelEnv) == "" {
		f.Text = def.Text
	}
	if os.Getenv(VocabEnv) == "" {
		f.Vocab = def.Vocab
	}
	if os.Getenv(MergesEnv) == "" {
		f.Merges = def.Merges
	}
	if what, path := f.Missing(); what != "" {
		t.Skipf("CLIP %s not found at %s: download Xenova/clip-vit-base-patch32 into linux/.models-cache (clip_image.onnx, clip_text.onnx, clip_vocab.json, clip_merges.txt) or set %s/%s/%s/%s",
			what, path, ImageModelEnv, TextModelEnv, VocabEnv, MergesEnv)
	}
	return f
}
