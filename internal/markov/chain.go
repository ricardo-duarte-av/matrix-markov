// Package markov implements a word-level N-order Markov chain.
//
// A Chain holds the text of exactly one room. The bot creates one Chain per
// room and never shares them, which is what keeps rooms isolated.
//
// Two things keep the output from simply parroting the room:
//   - Replies on a topic are grown in both directions from the topic word
//     (a backward chain is learned alongside the forward one), so they start
//     at a natural sentence beginning instead of mid-message.
//   - Every candidate is rejected if it shares too long a run of consecutive
//     words with any single learned message (the approach used by the
//     markovify library): at most 70% of its length, and never more than 15
//     words.
package markov

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
)

const (
	tokBegin = "\x02"
	tokEnd   = "\x03"
	sep      = "\x00"

	maxAttempts = 50
	// minSeedLen is the shortest normalized word used as a reply topic, to
	// skip filler like "a", "is", "the".
	minSeedLen = 4

	maxOverlapRatio = 0.7
	maxOverlapTotal = 15
)

type occurrence struct {
	msg, off int32
}

type Chain struct {
	mu    sync.RWMutex
	order int
	// fwd maps a prefix key (order tokens joined by sep) to follower counts;
	// bwd is the same for messages read backwards.
	fwd, bwd map[string]map[string]int
	// byWord maps a normalized word to forward prefix keys ending with it.
	byWord map[string][]string
	// msgs and occ index every learned message for the overlap check:
	// occ maps the hash of each (order+1)-word run to where it occurs.
	msgs [][]string
	occ  map[uint64][]occurrence
}

func NewChain(order int) *Chain {
	if order < 1 {
		order = 1
	}
	return &Chain{
		order:  order,
		fwd:    make(map[string]map[string]int),
		bwd:    make(map[string]map[string]int),
		byWord: make(map[string][]string),
		occ:    make(map[uint64][]occurrence),
	}
}

func (c *Chain) Order() int { return c.order }

// Size returns the number of distinct (forward) prefixes in the chain.
func (c *Chain) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.fwd)
}

// Add learns one message (already cleaned).
func (c *Chain) Add(text string) {
	words := Tokenize(text)
	if len(words) == 0 {
		return
	}
	reversed := slices.Clone(words)
	slices.Reverse(reversed)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.learn(c.fwd, pad(words, tokBegin, tokEnd, c.order), true)
	c.learn(c.bwd, pad(reversed, tokEnd, tokBegin, c.order), false)

	idx := int32(len(c.msgs))
	c.msgs = append(c.msgs, words)
	g := c.order + 1
	for i := 0; i+g <= len(words); i++ {
		h := hashWords(words[i : i+g])
		c.occ[h] = append(c.occ[h], occurrence{msg: idx, off: int32(i)})
	}
}

// pad surrounds words with order start sentinels and one end sentinel.
func pad(words []string, start, end string, order int) []string {
	toks := make([]string, 0, len(words)+order+1)
	for i := 0; i < order; i++ {
		toks = append(toks, start)
	}
	toks = append(toks, words...)
	return append(toks, end)
}

func (c *Chain) learn(trans map[string]map[string]int, toks []string, index bool) {
	for i := 0; i+c.order < len(toks); i++ {
		prefix := toks[i : i+c.order]
		key := strings.Join(prefix, sep)
		followers, ok := trans[key]
		if !ok {
			followers = make(map[string]int)
			trans[key] = followers
			if w := Normalize(prefix[len(prefix)-1]); index && len(w) >= minSeedLen {
				c.byWord[w] = append(c.byWord[w], key)
			}
		}
		followers[toks[i+c.order]]++
	}
}

// Generate produces a message between minWords and maxWords words long.
// seedWords (normally the words of the triggering message) pick a topic when
// possible. It returns "" if the chain is empty or every attempt was too
// short or too close to a learned message.
func (c *Chain) Generate(r *rand.Rand, seedWords []string, minWords, maxWords int) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.fwd) == 0 {
		return ""
	}

	var topics []string
	for _, w := range seedWords {
		if n := Normalize(w); len(c.byWord[n]) > 0 {
			topics = append(topics, n)
		}
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		var out []string
		// Use a topic for most attempts, but leave some random ones in case
		// every topical walk is rejected.
		if len(topics) > 0 && attempt < maxAttempts*3/4 {
			keys := c.byWord[topics[r.IntN(len(topics))]]
			out = c.around(r, strings.Split(keys[r.IntN(len(keys))], sep), maxWords)
		} else {
			start := make([]string, c.order)
			for i := range start {
				start[i] = tokBegin
			}
			out = c.walk(r, c.fwd, start, tokEnd, maxWords)
		}
		if len(out) >= minWords && !c.overlapsSource(out) {
			return strings.Join(out, " ")
		}
	}
	return ""
}

// around grows a sentence in both directions from a forward prefix: back to
// a sentence start with the backward chain, then forward to an end.
func (c *Chain) around(r *rand.Rand, start []string, maxWords int) []string {
	words := withoutSentinels(start)
	var before []string // preceding words, nearest first
	if start[0] != tokBegin {
		// The backward chain's state is the reversed sentence start.
		state := slices.Clone(start)
		slices.Reverse(state)
		before = c.walk(r, c.bwd, state, tokBegin, maxWords-1)
		before = before[len(words):] // walk echoes its start words first
	}
	slices.Reverse(before)
	after := c.walk(r, c.fwd, start, tokEnd, maxWords-len(before))
	return append(before, after...)
}

// walk follows trans from start until stop or maxWords words, returning the
// start's own words followed by the generated ones.
func (c *Chain) walk(r *rand.Rand, trans map[string]map[string]int, start []string, stop string, maxWords int) []string {
	state := slices.Clone(start)
	out := withoutSentinels(start)
	for len(out) < maxWords {
		followers := trans[strings.Join(state[len(state)-c.order:], sep)]
		if len(followers) == 0 {
			break
		}
		tok := pick(r, followers)
		if tok == stop {
			break
		}
		out = append(out, tok)
		state = append(state, tok)
	}
	return out
}

func withoutSentinels(toks []string) []string {
	var out []string
	for _, t := range toks {
		if t != tokBegin && t != tokEnd {
			out = append(out, t)
		}
	}
	return out
}

// overlapsSource reports whether out shares a run of consecutive words with
// one learned message that is too long: round(70% of its length), capped at
// 15, and never below order+2 (every order+1 run exists by construction).
func (c *Chain) overlapsSource(out []string) bool {
	w := int(math.Round(maxOverlapRatio * float64(len(out))))
	w = min(w, maxOverlapTotal)
	w = max(w, c.order+2)
	w = min(w, len(out))
	for i := 0; i+w <= len(out); i++ {
		if c.containsRun(out[i : i+w]) {
			return true
		}
	}
	return false
}

// containsRun reports whether run appears contiguously in a learned message.
func (c *Chain) containsRun(run []string) bool {
	g := c.order + 1
	if len(run) < g {
		for _, m := range c.msgs { // rare: output shorter than order+1
			if hasRun(m, run) {
				return true
			}
		}
		return false
	}
	for _, o := range c.occ[hashWords(run[:g])] {
		m := c.msgs[o.msg]
		if end := int(o.off) + len(run); end <= len(m) && slices.Equal(m[o.off:end], run) {
			return true
		}
	}
	return false
}

func hasRun(m, run []string) bool {
	for i := 0; i+len(run) <= len(m); i++ {
		if slices.Equal(m[i:i+len(run)], run) {
			return true
		}
	}
	return false
}

// pick chooses a follower weighted by count. Iterating in sorted order makes
// seeded tests reproducible.
func pick(r *rand.Rand, followers map[string]int) string {
	keys := make([]string, 0, len(followers))
	total := 0
	for k, n := range followers {
		keys = append(keys, k)
		total += n
	}
	slices.Sort(keys)
	n := r.IntN(total)
	for _, k := range keys {
		n -= followers[k]
		if n < 0 {
			return k
		}
	}
	return keys[len(keys)-1]
}

func hashWords(words []string) uint64 {
	h := fnv.New64a()
	for _, w := range words {
		h.Write([]byte(w))
		h.Write([]byte{0})
	}
	return h.Sum64()
}
