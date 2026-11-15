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
	"matrix-markov/internal/react"
	"matrix-markov/internal/store"
)

// TestRoomsDoNotLeak feeds two rooms disjoint vocabularies, seed files,
// senders (whose names fill mention placeholders) and reaction keys. It then
// generates many replies and reaction suggestions in each room, including
// with prompts made of the *other* room's text, and checks that nothing from
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
	sendersA := []id.UserID{"@ua1:example.org", "@ua2:example.org", "@ua3:example.org"}
	sendersB := []id.UserID{"@ub1:example.org", "@ub2:example.org", "@ub3:example.org"}
	// The resolver knows everybody (as a real state store could, if the
	// same person is in both rooms); isolation must come from the Brain only
	// asking about the room's own senders.
	names := func(_ context.Context, _ id.RoomID, u id.UserID) string { return "Name_" + u.Localpart() }

	writeSeed := func(name string, words []string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(sentences(words, 30), ".\n")+".\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	settings := func(rid id.RoomID, seed string, order int) config.RoomSettings {
		return config.RoomSettings{ID: rid.String(), SeedFile: seed, Order: order, MinWords: 3, MaxWords: 25, Learn: true, MinLearnWords: 2}
	}
	rooms := map[id.RoomID]config.RoomSettings{
		roomA: settings(roomA, writeSeed("a.txt", seedA), 2),
		roomB: settings(roomB, writeSeed("b.txt", seedB), 1),
	}

	// Every 3rd message mentions someone, so placeholders get learned.
	msgsA, msgsB := sentences(wordsA, 100), sentences(wordsB, 100)
	for i := 0; i < 100; i += 3 {
		msgsA[i] = markov.UserPlaceholder + " " + msgsA[i]
		msgsB[i] = msgsB[i] + " @someone:example.org"
	}

	// Learn half, "restart" (rebuild from the DB), learn the rest live, so
	// both load paths are covered.
	brain, err := NewBrain(ctx, st, names, rooms)
	if err != nil {
		t.Fatal(err)
	}
	learn := func(br *Brain, from, to int) {
		for i := from; i < to; i++ {
			mustLearn(t, br, roomA, fmt.Sprintf("$a%d", i), sendersA[i%3], msgsA[i])
			mustLearn(t, br, roomB, fmt.Sprintf("$b%d", i), sendersB[i%3], msgsB[i])
			// Each room's messages get that room's own reactions.
			mustReact(t, br, roomA, fmt.Sprintf("$ra%d", i), fmt.Sprintf("$a%d", i), "🅰️")
			mustReact(t, br, roomB, fmt.Sprintf("$rb%d", i), fmt.Sprintf("$b%d", i), "bravo reaction")
			// Cross-room attempt: a reaction in room B pointing at a room A message.
			mustReact(t, br, roomB, fmt.Sprintf("$x%d", i), fmt.Sprintf("$a%d", i), "LEAK")
		}
	}
	learn(brain, 0, 50)
	if brain, err = NewBrain(ctx, st, names, rooms); err != nil {
		t.Fatal(err)
	}
	learn(brain, 50, 100)

	// A room that isn't allowed must neither learn nor reply.
	mustLearn(t, brain, "!other:example.org", "$x", "@ux:example.org", "secret words here")
	if out := brain.Reply(ctx, "!other:example.org", "secret", "@ux:example.org"); out != "" {
		t.Fatalf("unallowed room got a reply: %q", out)
	}

	allowed := func(words, seed []string, senders []id.UserID) map[string]bool {
		m := map[string]bool{}
		for _, w := range append(words, seed...) {
			m[w], m[w+"."] = true, true // seed sentences end with a period
		}
		for _, u := range senders {
			m[names(ctx, "", u)] = true
		}
		return m
	}
	allowedA := allowed(wordsA, seedA, sendersA)
	allowedB := allowed(wordsB, seedB, sendersB)
	namesSeen := 0
	check := func(room id.RoomID, allowed map[string]bool, trigger id.UserID, prompt string) {
		t.Helper()
		out := brain.Reply(ctx, room, prompt, trigger)
		if out == "" {
			t.Fatalf("room %s produced no reply", room)
		}
		if strings.Contains(out, markov.UserPlaceholder) {
			t.Fatalf("room %s left a placeholder in %q", room, out)
		}
		for _, w := range markov.Tokenize(out) {
			if !allowed[w] {
				t.Fatalf("room %s leaked foreign token %q in %q", room, w, out)
			}
			if strings.HasPrefix(w, "Name_") {
				namesSeen++
			}
		}
		rc, _, ok := brain.SuggestReaction(room, prompt)
		if ok {
			want := map[id.RoomID]string{roomA: "🅰️", roomB: "bravo reaction"}[room]
			if rc.Key != want {
				t.Fatalf("room %s suggested foreign reaction %q", room, rc.Key)
			}
		}
	}
	for i := 0; i < 1000; i++ {
		check(roomA, allowedA, sendersA[0], msgsB[i%len(msgsB)]) // prompt with the other room's text
		check(roomA, allowedA, sendersA[1], msgsA[i%len(msgsA)])
		check(roomB, allowedB, sendersB[0], msgsA[i%len(msgsA)])
		check(roomB, allowedB, sendersB[1], msgsB[i%len(msgsB)])
	}
	if namesSeen == 0 {
		t.Fatal("no mention placeholder was ever filled in; test isn't exercising names")
	}
	if rc, _, ok := brain.SuggestReaction(roomA, msgsA[1]); !ok || rc.Key != "🅰️" {
		t.Fatalf("room A reaction not learned: %q %v", rc.Key, ok)
	}
}

func TestForgetRedactedMessage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "markov.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	room := id.RoomID("!r:example.org")
	brain, err := NewBrain(ctx, st, nil, map[id.RoomID]config.RoomSettings{
		room: {Order: 1, MinWords: 1, MaxWords: 10, Learn: true, MinLearnWords: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustLearn(t, brain, room, "$secret", "@u:example.org", "topsecret topsecret topsecret")
	mustReact(t, brain, room, "$r1", "$secret", "🤫")
	mustReact(t, brain, room, "$r2", "$secret", "🤫")
	if out := brain.Reply(ctx, room, "", ""); !strings.Contains(out, "topsecret") {
		t.Fatalf("expected the message to be learned, got %q", out)
	}
	if kind, err := brain.Forget(ctx, room, "$secret"); err != nil || kind != "message" {
		t.Fatalf("Forget = %q, %v", kind, err)
	}
	if out := brain.Reply(ctx, room, "", ""); out != "" {
		t.Fatalf("redacted message still generated: %q", out)
	}
	if _, _, ok := brain.SuggestReaction(room, "topsecret"); ok {
		t.Fatal("reactions on redacted message still suggested")
	}
}

func mustLearn(t *testing.T, b *Brain, room id.RoomID, evt string, sender id.UserID, body string) {
	t.Helper()
	if _, _, err := b.Learn(context.Background(), room, id.EventID(evt), sender, body, 1); err != nil {
		t.Fatal(err)
	}
}

func mustReact(t *testing.T, b *Brain, room id.RoomID, evt, target, key string) {
	t.Helper()
	if _, err := b.LearnReaction(context.Background(), room, id.EventID(evt), id.EventID(target), "@r:example.org", react.Reaction{Key: key}, 2); err != nil {
		t.Fatal(err)
	}
}

// TestShortMessages: the gomuks example "<url> lmao?" cleans to one word. It
// must be stored (so reactions on it are learned) but not added to the
// chain, both live and after a rebuild from the database.
func TestShortMessages(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "markov.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	room := id.RoomID("!r:example.org")
	rooms := map[id.RoomID]config.RoomSettings{room: {Order: 1, MinWords: 1, MaxWords: 10, Learn: true, MinLearnWords: 2}}
	brain, err := NewBrain(ctx, st, nil, rooms)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		evt, body string
		want      LearnOutcome
	}{
		{"$1", "https://bsky.app/profile/mlyp.bsky.social/post/3n22sr3zsw22k lmao?", LearnStored},
		{"$2", "https://example.com/only-a-link", LearnIgnored},
		{"$3", "two words", LearnChained},
		{"$3", "two words", LearnDuplicate},
	}
	for _, c := range cases {
		got, _, err := brain.Learn(ctx, room, id.EventID(c.evt), "@u:example.org", c.body, 1)
		if err != nil || got != c.want {
			t.Fatalf("Learn(%q) = %q, %v; want %q", c.body, got, err, c.want)
		}
	}
	for _, br := range []*Brain{brain, must(NewBrain(ctx, st, nil, rooms))} { // live, then rebuilt
		for i := 0; i < 200; i++ {
			if out := br.Reply(ctx, room, "", ""); strings.Contains(out, "lmao") {
				t.Fatalf("short message reached the chain: %q", out)
			}
		}
	}
	// Reactions on the short message are still learned.
	for i, k := range []string{"$r1", "$r2"} {
		if ok, err := brain.LearnReaction(ctx, room, id.EventID(k), "$1", id.UserID(fmt.Sprintf("@r%d:x", i)), react.Reaction{Key: "😂"}, 2); err != nil || !ok {
			t.Fatalf("reaction on short message not learned: %v %v", ok, err)
		}
	}
	if rc, _, ok := brain.SuggestReaction(room, "lmao"); !ok || rc.Key != "😂" {
		t.Fatalf("SuggestReaction = %q %v", rc.Key, ok)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
