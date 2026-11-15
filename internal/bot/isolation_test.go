package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maunium.net/go/mautrix/id"

	"matrix-markov/internal/config"
	"matrix-markov/internal/markov"
	"matrix-markov/internal/store"
)

// TestRoomsDoNotLeak feeds two rooms disjoint vocabularies (plus a different
// seed file each), then generates many replies in each room, including with
// prompts made of the *other* room's words, and checks that no token from
// the other room ever appears.
func TestRoomsDoNotLeak(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "markov.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	roomA, roomB := id.RoomID("!alpha:example.org"), id.RoomID("!bravo:example.org")
	vocab := func(p string, n int) []string {
		w := make([]string, n)
		for i := range w {
			w[i] = fmt.Sprintf("%s%02d", p, i)
		}
		return w
	}
	sentences := func(words []string, n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			var s []string
			for j := 0; j < 6+i%5; j++ {
				s = append(s, words[(i*7+j*3)%len(words)])
			}
			out = append(out, strings.Join(s, " "))
		}
		return out
	}
	wordsA, wordsB := vocab("alpha", 40), vocab("bravo", 40)
	seedA, seedB := vocab("seeda", 20), vocab("seedb", 20)

	writeSeed := func(name string, words []string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(sentences(words, 30), ".\n")+".\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	settings := func(rid id.RoomID, seed string, order int) config.RoomSettings {
		return config.RoomSettings{ID: rid.String(), SeedFile: seed, Order: order, MinWords: 3, MaxWords: 25, Learn: true}
	}
	rooms := map[id.RoomID]config.RoomSettings{
		roomA: settings(roomA, writeSeed("a.txt", seedA), 2),
		roomB: settings(roomB, writeSeed("b.txt", seedB), 1),
	}

	// Learn half the messages, then "restart" (rebuild from the DB), then
	// learn the rest live, so both load paths are covered.
	msgsA, msgsB := sentences(wordsA, 100), sentences(wordsB, 100)
	brain, err := NewBrain(ctx, st, rooms)
	if err != nil {
		t.Fatal(err)
	}
	learn := func(br *Brain, from, to int) {
		for i := from; i < to; i++ {
			mustLearn(t, br, roomA, fmt.Sprintf("$a%d", i), msgsA[i])
			mustLearn(t, br, roomB, fmt.Sprintf("$b%d", i), msgsB[i])
		}
	}
	learn(brain, 0, 50)
	if brain, err = NewBrain(ctx, st, rooms); err != nil {
		t.Fatal(err)
	}
	learn(brain, 50, 100)

	// A room that isn't allowed must neither learn nor reply.
	mustLearn(t, brain, "!other:example.org", "$x", "secret words here")
	if out := brain.Reply("!other:example.org", "secret"); out != "" {
		t.Fatalf("unallowed room got a reply: %q", out)
	}

	allowedA := set(wordsA, seedA)
	allowedB := set(wordsB, seedB)
	check := func(room id.RoomID, allowed map[string]bool, prompt string) {
		t.Helper()
		out := brain.Reply(room, prompt)
		if out == "" {
			t.Fatalf("room %s produced no reply", room)
		}
		for _, w := range markov.Tokenize(out) {
			if !allowed[w] {
				t.Fatalf("room %s leaked foreign token %q in %q", room, w, out)
			}
		}
	}
	for i := 0; i < 1000; i++ {
		check(roomA, allowedA, msgsB[i%len(msgsB)]) // prompt with the other room's text
		check(roomA, allowedA, msgsA[i%len(msgsA)])
		check(roomB, allowedB, msgsA[i%len(msgsA)])
		check(roomB, allowedB, msgsB[i%len(msgsB)])
	}
}

func mustLearn(t *testing.T, b *Brain, room id.RoomID, evt, body string) {
	t.Helper()
	if err := b.Learn(context.Background(), room, id.EventID(evt), "@u:example.org", body, 1); err != nil {
		t.Fatal(err)
	}
}

func set(lists ...[]string) map[string]bool {
	m := map[string]bool{}
	for _, l := range lists {
		for _, w := range l {
			m[w] = true
			m[w+"."] = true // seed sentences end with a period
		}
	}
	return m
}
