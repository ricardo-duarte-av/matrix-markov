package markov

import (
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

func TestGenerateOnlyUsesLearnedTokens(t *testing.T) {
	corpus := []string{
		"the quick brown fox jumps over the lazy dog",
		"the lazy cat sleeps all day long",
		"a quick brown cat jumps over the moon",
		"the dog barks at the moon every night",
	}
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
		for i := 0; i < 200; i++ {
			out := c.Generate(r, []string{"moon"}, 2, 20)
			if out == "" {
				t.Fatalf("order %d: empty output", order)
			}
			words := Tokenize(out)
			if len(words) > 20 {
				t.Fatalf("order %d: too long: %q", order, out)
			}
			for _, w := range words {
				if !vocab[w] {
					t.Fatalf("order %d: unknown token %q in %q", order, w, out)
				}
			}
		}
	}
}

func TestGenerateIsDeterministicWithSeed(t *testing.T) {
	c := NewChain(2)
	for _, s := range []string{"one two three four", "one two five six", "two three seven eight"} {
		c.Add(s)
	}
	a := c.Generate(rng(42), nil, 1, 10)
	b := c.Generate(rng(42), nil, 1, 10)
	if a != b {
		t.Fatalf("same seed gave %q and %q", a, b)
	}
}

func TestTopicSeedIsUsed(t *testing.T) {
	c := NewChain(2)
	c.Add("i like green apples very much")
	c.Add("bananas are yellow and sweet")
	r := rng(7)
	hits := 0
	for i := 0; i < 50; i++ {
		if strings.Contains(c.Generate(r, []string{"Bananas?"}, 1, 10), "bananas") {
			hits++
		}
	}
	if hits < 25 {
		t.Fatalf("topic word used only %d/50 times", hits)
	}
}
