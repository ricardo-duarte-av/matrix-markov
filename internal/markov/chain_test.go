package markov

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func rng(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) }

func TestCleanStripsFallbackURLsAndMXIDs(t *testing.T) {
	in := "> <@alice:example.org> old quoted text\n> more\n\nhey @bob:example.org see https://example.com/x and #room:example.org ok"
	got := Clean(in)
	want := "hey " + UserPlaceholder + " see and ok"
	if got != want {
		t.Fatalf("Clean = %q, want %q", got, want)
	}
}

func TestEmptyChain(t *testing.T) {
	c := NewChain(2)
	if out := c.Generate(rng(1), nil, 1, 10); out != "" {
		t.Fatalf("expected empty output, got %q", out)
	}
}

// combos builds a branchy corpus: every subject/verb/object combination
// shares words with many others, so the chain has real choices to make.
func combos() []string {
	var out []string
	subjects := []string{"the cat", "the dog", "my bird", "a fox"}
	verbs := []string{"sat on", "ran to", "looked at", "jumped over"}
	objects := []string{"the mat today", "the rug again", "the old fence", "a red ball"}
	for i, s := range subjects {
		for j, v := range verbs {
			// Skip some combinations so novel ones exist to be generated.
			if (i+j)%2 == 0 {
				out = append(out, fmt.Sprintf("%s %s %s", s, v, objects[(i+2*j)%len(objects)]))
			}
		}
	}
	return out
}

// chatCorpus builds many messages from a small vocabulary, so word pairs
// recur with different followers, as in a busy room.
func chatCorpus(seed uint64, n int) []string {
	words := strings.Fields("i think the bot is really weird today but we like it when it talks about trains and cats")
	r := rng(seed)
	out := make([]string, n)
	for i := range out {
		m := make([]string, 5+r.IntN(8))
		for j := range m {
			m[j] = words[r.IntN(len(words))]
		}
		out[i] = strings.Join(m, " ")
	}
	return out
}

// longestSharedRun is a brute-force reference for the overlap check.
func longestSharedRun(out []string, corpus []string) int {
	best := 0
	for _, msg := range corpus {
		m := Tokenize(msg)
		for i := range out {
			for j := range m {
				k := 0
				for i+k < len(out) && j+k < len(m) && out[i+k] == m[j+k] {
					k++
				}
				best = max(best, k)
			}
		}
	}
	return best
}

func TestGenerateNeverParrots(t *testing.T) {
	corpus := chatCorpus(5, 300)
	vocab := map[string]bool{}
	for _, s := range corpus {
		for _, w := range Tokenize(s) {
			vocab[w] = true
		}
	}
	for order := 1; order <= 3; order++ {
		c := NewChain(order)
		for _, s := range corpus {
			c.Add(s)
		}
		r := rng(uint64(order))
		produced := 0
		for i := 0; i < 300; i++ {
			out := c.Generate(r, []string{"trains"}, 3, 20)
			if out == "" {
				continue
			}
			produced++
			words := Tokenize(out)
			for _, w := range words {
				if !vocab[w] {
					t.Fatalf("order %d: unknown token %q in %q", order, w, out)
				}
			}
			limit := max(min(int(float64(len(words))*maxOverlapRatio+0.5), maxOverlapTotal), order+2)
			if limit < len(words) {
				if run := longestSharedRun(words, corpus); run >= limit {
					t.Fatalf("order %d: %q shares %d consecutive words with a message (limit %d)", order, out, run, limit)
				}
			} else if longestSharedRun(words, corpus) == len(words) {
				t.Fatalf("order %d: %q is a copy of part of a message", order, out)
			}
		}
		if produced == 0 {
			t.Fatalf("order %d: nothing was generated from a large branchy corpus", order)
		}
	}
}

// TestTopicRepliesStartAtSentenceStart: replies grown from a topic word
// begin with a word that starts some learned message, instead of starting
// mid-sentence at the topic.
func TestTopicRepliesStartAtSentenceStart(t *testing.T) {
	corpus := combos()
	starts := map[string]bool{}
	for _, s := range corpus {
		starts[Tokenize(s)[0]] = true
	}
	c := NewChain(1)
	for _, s := range corpus {
		c.Add(s)
	}
	r := rng(3)
	topical := 0
	for i := 0; i < 200; i++ {
		out := c.Generate(r, []string{"fence"}, 3, 20)
		if out == "" {
			continue
		}
		words := Tokenize(out)
		if !starts[words[0]] {
			t.Fatalf("%q does not start at a sentence start", out)
		}
		if strings.Contains(out, "fence") {
			topical++
		}
	}
	if topical < 50 {
		t.Fatalf("topic used in only %d/200 replies", topical)
	}
}

// TestNoEchoOfPrompt reproduces the problem seen in a real room: prompted
// with a message it had just learned, the bot answered with the tail of
// that same message.
func TestNoEchoOfPrompt(t *testing.T) {
	c := NewChain(2)
	prompt := "Seems it was via random selection"
	c.Add(prompt)
	c.Add("the open world seems kinda pointless")
	c.Add("they should have made the races 3 laps with connections")
	r := rng(9)
	for i := 0; i < 200; i++ {
		if out := c.Generate(r, Tokenize(prompt), 3, 30); out != "" {
			t.Fatalf("unique messages can only be parroted, but got %q", out)
		}
	}
}

func TestGenerateIsDeterministicWithSeed(t *testing.T) {
	c := NewChain(1)
	for _, s := range combos() {
		c.Add(s)
	}
	a := c.Generate(rng(42), nil, 3, 20)
	b := c.Generate(rng(42), nil, 3, 20)
	if a != b {
		t.Fatalf("same seed gave %q and %q", a, b)
	}
}
