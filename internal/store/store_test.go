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
