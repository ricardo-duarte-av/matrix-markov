// Package react learns which reactions a room puts on which words, and
// suggests a reaction for a new message.
//
// Reaction keys are arbitrary text (usually an emoji, but also words, long
// strings or mxc:// custom emoji), so keys are treated as opaque strings and
// filtered by ValidKey before they are learned. Like markov.Chain, a Reactor
// holds one room's data only.
package react

import (
	"math"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"matrix-markov/internal/markov"
)

const (
	MaxKeyRunes = 32
	MaxKeyBytes = 256

	// minWordSupport is how many reactions a word needs before it counts.
	minWordSupport = 2
	// minKeyUses is how often a key must have been used before it's suggested.
	minKeyUses = 2
	minWordLen = 3
)

type Reaction struct {
	Key       string
	Shortcode string // for mxc:// custom emoji (com.beeper.reaction.shortcode)
}

// ValidKey reports whether a reaction key is safe to learn and re-send:
// short, single-line, no control characters, and no Matrix IDs or URLs
// (except an mxc:// custom-emoji key on its own).
func ValidKey(key string) bool {
	if key == "" || key != strings.TrimSpace(key) || len(key) > MaxKeyBytes || !utf8.ValidString(key) {
		return false
	}
	if strings.HasPrefix(key, "mxc://") {
		return !strings.ContainsFunc(key, unicode.IsSpace) && len(key) <= MaxKeyBytes
	}
	if utf8.RuneCountInString(key) > MaxKeyRunes {
		return false
	}
	for _, r := range key {
		// Allow zero-width joiners / variation selectors used in emoji.
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return !markov.HasIdentifierOrURL(key)
}

// ValidShortcode checks the optional custom-emoji shortcode.
func ValidShortcode(s string) bool {
	return s == "" || (utf8.RuneCountInString(s) <= MaxKeyRunes && ValidKey(s) && !strings.HasPrefix(s, "mxc://"))
}

type message struct {
	words []string
	keys  map[string]int
}

type Reactor struct {
	mu         sync.RWMutex
	msgs       map[string]*message       // target event ID -> its words and reactions
	wordKey    map[string]map[string]int // word -> key -> count
	wordTotal  map[string]int            // word -> total reactions
	wordDF     map[string]int            // word -> number of reacted messages containing it
	keyCount   map[string]int
	shortcodes map[string]string
}

func New() *Reactor {
	return &Reactor{
		msgs:       map[string]*message{},
		wordKey:    map[string]map[string]int{},
		wordTotal:  map[string]int{},
		wordDF:     map[string]int{},
		keyCount:   map[string]int{},
		shortcodes: map[string]string{},
	}
}

// Add learns that the message target (whose cleaned text is body) got a
// reaction with key.
func (r *Reactor) Add(target, body string, rc Reaction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.msgs[target]
	if !ok {
		m = &message{words: words(body), keys: map[string]int{}}
		r.msgs[target] = m
		for _, w := range m.words {
			r.wordDF[w]++
		}
	}
	m.keys[rc.Key]++
	r.keyCount[rc.Key]++
	if rc.Shortcode != "" {
		r.shortcodes[rc.Key] = rc.Shortcode
	}
	for _, w := range m.words {
		if r.wordKey[w] == nil {
			r.wordKey[w] = map[string]int{}
		}
		r.wordKey[w][rc.Key]++
		r.wordTotal[w]++
	}
}

// Remove undoes one Add (used when a reaction is redacted).
func (r *Reactor) Remove(target, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.msgs[target]
	if !ok || m.keys[key] == 0 {
		return
	}
	decr := func(mp map[string]int, k string) {
		if mp[k]--; mp[k] <= 0 {
			delete(mp, k)
		}
	}
	decr(m.keys, key)
	decr(r.keyCount, key)
	for _, w := range m.words {
		decr(r.wordKey[w], key)
		if len(r.wordKey[w]) == 0 {
			delete(r.wordKey, w)
		}
		decr(r.wordTotal, w)
	}
	if len(m.keys) == 0 {
		for _, w := range m.words {
			decr(r.wordDF, w)
		}
		delete(r.msgs, target)
	}
	if r.keyCount[key] == 0 {
		delete(r.shortcodes, key)
	}
}

// Suggest scores every known key for body and returns the best one with a
// score in [0,1]. The score is the IDF-weighted average, over the message's
// words, of P(key | word): words common to many reacted messages ("the")
// count less than distinctive ones. ok is false if nothing applies.
func (r *Reactor) Suggest(body string) (rc Reaction, score float64, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := float64(len(r.msgs))
	scores := map[string]float64{}
	var sumIDF float64
	for _, w := range words(body) {
		total := r.wordTotal[w]
		if total < minWordSupport {
			continue
		}
		idf := math.Log(1 + n/float64(r.wordDF[w]))
		sumIDF += idf
		for k, c := range r.wordKey[w] {
			scores[k] += idf * float64(c) / float64(total)
		}
	}
	if sumIDF == 0 {
		return Reaction{}, 0, false
	}
	keys := make([]string, 0, len(scores))
	for k := range scores {
		keys = append(keys, k)
	}
	slices.Sort(keys) // deterministic tie-break
	best := ""
	for _, k := range keys {
		if r.keyCount[k] < minKeyUses {
			continue
		}
		if best == "" || scores[k] > scores[best] {
			best = k
		}
	}
	if best == "" {
		return Reaction{}, 0, false
	}
	return Reaction{Key: best, Shortcode: r.shortcodes[best]}, scores[best] / sumIDF, true
}

func words(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range markov.Tokenize(markov.Clean(body)) {
		w := markov.Normalize(t)
		if utf8.RuneCountInString(w) < minWordLen || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}
