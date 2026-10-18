// Package reid is appearance search's embedding side (WAVE9-REACH.md,
// stream A): an optional re-identification model turns the crop of a
// detected person or vehicle into a unit-length float vector, and two
// crops of the same thing score close to 1 under cosine similarity.
//
// The model sits behind the Embedder interface exactly like the wave-8
// audio classifier: the server runs without it (appearance search simply
// reports why it is unavailable) and tests use a fake.
package reid

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Encode packs a vector as little-endian float32 bytes (the
// event_embeddings.vec format).
func Encode(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

// Decode unpacks Encode's bytes.
func Decode(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector of %d bytes is not a whole number of float32s", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

// ErrBadVector is a vector that can't be normalized: empty, all zeros, or
// holding NaN/Inf (a broken model output).
var ErrBadVector = errors.New("embedding is empty, zero or not finite")

// Normalize scales v to unit L2 length in place and returns it.
func Normalize(v []float32) ([]float32, error) {
	var sum float64
	for _, f := range v {
		x := float64(f)
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, ErrBadVector
		}
		sum += x * x
	}
	if len(v) == 0 || sum == 0 {
		return nil, ErrBadVector
	}
	inv := 1 / math.Sqrt(sum)
	for i := range v {
		v[i] = float32(float64(v[i]) * inv)
	}
	return v, nil
}

// Cosine is the cosine similarity of two unit vectors (their dot product),
// clamped to [-1, 1]. Vectors of different length score -1.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return -1
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return math.Max(-1, math.Min(1, dot))
}

// Hit is one ranked candidate.
type Hit struct {
	ID    string
	Score float64
}

// Ranker keeps the best limit candidates by cosine similarity to a query
// while candidates stream past (brute force, O(n log limit) memory-light).
type Ranker struct {
	query    []float32
	limit    int
	minScore float64
	hits     []Hit
}

// NewRanker ranks against query (unit length), keeping at most limit hits
// scoring at least minScore.
func NewRanker(query []float32, limit int, minScore float64) *Ranker {
	if limit < 1 {
		limit = 1
	}
	return &Ranker{query: query, limit: limit, minScore: minScore}
}

// Add scores one candidate. Vectors of another dimension are skipped.
func (r *Ranker) Add(id string, v []float32) {
	if len(v) != len(r.query) {
		return
	}
	s := Cosine(r.query, v)
	if s < r.minScore {
		return
	}
	if len(r.hits) == r.limit && s <= r.hits[len(r.hits)-1].Score {
		return
	}
	i := sort.Search(len(r.hits), func(i int) bool { return r.hits[i].Score < s })
	r.hits = append(r.hits, Hit{})
	copy(r.hits[i+1:], r.hits[i:])
	r.hits[i] = Hit{ID: id, Score: s}
	if len(r.hits) > r.limit {
		r.hits = r.hits[:r.limit]
	}
}

// Hits are the kept candidates, best first (ties keep arrival order).
func (r *Ranker) Hits() []Hit { return r.hits }
