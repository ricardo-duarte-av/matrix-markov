// Package markov implements a word-level N-order Markov chain.
//
// A Chain holds the text of exactly one room. The bot creates one Chain per
// room and never shares them, which is what keeps rooms isolated.
package markov

import (
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
)

const (
	tokBegin = "\x02"
	tokEnd   = "\x03"
	sep      = "\x00"

	maxAttempts = 25
	// minSeedLen is the shortest normalized word used as a reply topic, to
	// skip filler like "a", "is", "the".
	minSeedLen = 4
)

type Chain struct {
	mu    sync.RWMutex
	order int
	// next maps a prefix key (order tokens joined by sep) to follower counts.
	next map[string]map[string]int
	// byWord maps a normalized word to prefix keys whose last token is that word.
	byWord map[string][]string
	// learned holds hashes of every learned message so we can avoid parroting.
	learned map[uint64]struct{}
}

func NewChain(order int) *Chain {
	if order < 1 {
		order = 1
	}
	return &Chain{
		order:   order,
		next:    make(map[string]map[string]int),
		byWord:  make(map[string][]string),
		learned: make(map[uint64]struct{}),
	}
}

func (c *Chain) Order() int { return c.order }

// Size returns the number of distinct prefixes in the chain.
func (c *Chain) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.next)
}

// Add learns one message (already cleaned).
func (c *Chain) Add(text string) {
	words := Tokenize(text)
	if len(words) == 0 {
		return
	}
	toks := make([]string, 0, len(words)+c.order+1)
	for i := 0; i < c.order; i++ {
		toks = append(toks, tokBegin)
	}
	toks = append(toks, words...)
	toks = append(toks, tokEnd)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.learned[hashWords(words)] = struct{}{}
	for i := 0; i+c.order < len(toks); i++ {
		prefix := toks[i : i+c.order]
		key := strings.Join(prefix, sep)
		followers, ok := c.next[key]
		if !ok {
			followers = make(map[string]int)
			c.next[key] = followers
			if w := Normalize(prefix[len(prefix)-1]); len(w) >= minSeedLen {
				c.byWord[w] = append(c.byWord[w], key)
			}
		}
		followers[toks[i+c.order]]++
	}
}

// Generate produces a message between minWords and maxWords words long.
// seedWords (normally the words of the triggering message) are used to pick
// a topic when possible. It prefers output that isn't an exact copy of a
// learned message, and returns "" if the chain is empty.
func (c *Chain) Generate(r *rand.Rand, seedWords []string, minWords, maxWords int) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.next) == 0 {
		return ""
	}

	var topics []string
	for _, w := range seedWords {
		if n := Normalize(w); len(c.byWord[n]) > 0 {
			topics = append(topics, n)
		}
	}

	var fallbackLong, fallbackAny []string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var start []string
		// Use a topic for most attempts, but leave some random ones in case
		// every topical walk is a dead end.
		if len(topics) > 0 && attempt < maxAttempts*3/4 {
			keys := c.byWord[topics[r.IntN(len(topics))]]
			start = strings.Split(keys[r.IntN(len(keys))], sep)
		} else {
			start = make([]string, c.order)
			for i := range start {
				start[i] = tokBegin
			}
		}
		out := c.walk(r, start, maxWords)
		if len(out) == 0 {
			continue
		}
		if len(out) >= minWords {
			if _, copied := c.learned[hashWords(out)]; !copied {
				return strings.Join(out, " ")
			}
			if fallbackLong == nil {
				fallbackLong = out
			}
		} else if fallbackAny == nil {
			fallbackAny = out
		}
	}
	if fallbackLong != nil {
		return strings.Join(fallbackLong, " ")
	}
	return strings.Join(fallbackAny, " ")
}

func (c *Chain) walk(r *rand.Rand, start []string, maxWords int) []string {
	state := append([]string(nil), start...)
	var out []string
	for _, t := range start {
		if t != tokBegin {
			out = append(out, t)
		}
	}
	for len(out) < maxWords {
		followers := c.next[strings.Join(state[len(state)-c.order:], sep)]
		if len(followers) == 0 {
			break
		}
		tok := pick(r, followers)
		if tok == tokEnd {
			break
		}
		out = append(out, tok)
		state = append(state, tok)
	}
	return out
}

// pick chooses a follower weighted by count. Map order doesn't matter for
// correctness; iterating in a stable order makes seeded tests reproducible.
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
