package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStoreRoomScoping(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	add := func(room, ev, body string, ts int64) {
		t.Helper()
		if _, err := s.Add(ctx, room, ev, "@u:x", body, ts); err != nil {
			t.Fatal(err)
		}
	}
	add("!a:x", "$1", "alpha one", 1)
	add("!a:x", "$2", "alpha two", 2)
	add("!b:x", "$3", "bravo one", 3)

	if ins, _ := s.Add(ctx, "!a:x", "$1", "@u:x", "dup", 9); ins {
		t.Fatal("duplicate event was inserted")
	}

	var got []string
	if _, err := s.ForEach(ctx, "!a:x", func(b string) { got = append(got, b) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "alpha one" || got[1] != "alpha two" {
		t.Fatalf("room a rows = %v", got)
	}

	add("!a:x", "$4", "alpha three", 4)
	if err := s.Prune(ctx, "!a:x", 2); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count(ctx, "!a:x"); n != 2 {
		t.Fatalf("after prune room a has %d rows", n)
	}
	if n, _ := s.Count(ctx, "!b:x"); n != 1 {
		t.Fatalf("prune of room a touched room b: %d rows", n)
	}

	if err := s.DeleteRoom(ctx, "!a:x"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count(ctx, "!b:x"); n != 1 {
		t.Fatalf("delete of room a touched room b: %d rows", n)
	}
}

func TestReactionsAreRoomScoped(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _ = s.Add(ctx, "!a:x", "$m1", "@u:x", "alpha", 1)
	_, _ = s.Add(ctx, "!b:x", "$m2", "@v:x", "bravo", 2)

	// A reaction claiming to be in room b but targeting room a's message
	// must not be joined to it.
	for _, r := range []struct{ room, ev, target, key string }{
		{"!a:x", "$r1", "$m1", "👍"},
		{"!b:x", "$r2", "$m1", "😈"},
		{"!b:x", "$r3", "$m2", "🎉"},
	} {
		if _, err := s.AddReaction(ctx, r.room, Reaction{EventID: r.ev, Target: r.target, Sender: "@w:x", Key: r.key}, 3); err != nil {
			t.Fatal(err)
		}
	}
	collect := func(room string) []string {
		var keys []string
		if err := s.ForEachReaction(ctx, room, func(r Reaction) { keys = append(keys, r.Key+":"+r.TargetBody) }); err != nil {
			t.Fatal(err)
		}
		return keys
	}
	if got := collect("!a:x"); len(got) != 1 || got[0] != "👍:alpha" {
		t.Fatalf("room a reactions = %v", got)
	}
	if got := collect("!b:x"); len(got) != 1 || got[0] != "🎉:bravo" {
		t.Fatalf("room b reactions = %v", got)
	}

	if r, _ := s.DeleteReaction(ctx, "!b:x", "$r1"); r != nil {
		t.Fatal("deleted a reaction through the wrong room")
	}
	if r, _ := s.DeleteReaction(ctx, "!a:x", "$r1"); r == nil || r.Key != "👍" || r.Target != "$m1" {
		t.Fatalf("DeleteReaction = %+v", r)
	}
	if ok, _ := s.DeleteMessage(ctx, "!b:x", "$m1"); ok {
		t.Fatal("deleted a message through the wrong room")
	}
	if ok, _ := s.DeleteMessage(ctx, "!b:x", "$m2"); !ok {
		t.Fatal("DeleteMessage did not find message")
	}
	if got := collect("!b:x"); len(got) != 0 {
		t.Fatalf("reactions on deleted message remain: %v", got)
	}
	senders, _ := s.RecentSenders(ctx, "!a:x", 10)
	if len(senders) != 1 || senders[0] != "@u:x" {
		t.Fatalf("RecentSenders = %v", senders)
	}
}
