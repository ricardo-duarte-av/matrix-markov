package react

import (
	"strings"
	"testing"
)

func TestValidKey(t *testing.T) {
	good := []string{"👍", "😂", "❤️", "👨‍👩‍👧", "lol", "+1", "this is fine", "mxc://example.org/abcDEF123"}
	bad := []string{
		"", " 👍", "two\nlines", "tab\there", "\x00",
		strings.Repeat("a", MaxKeyRunes+1),
		"hi @alice:example.org", "see https://example.com", "#room:example.org",
		"mxc://example.org/x y",
	}
	for _, k := range good {
		if !ValidKey(k) {
			t.Errorf("ValidKey(%q) = false, want true", k)
		}
	}
	for _, k := range bad {
		if ValidKey(k) {
			t.Errorf("ValidKey(%q) = true, want false", k)
		}
	}
}

func TestSuggestLearnsAssociations(t *testing.T) {
	r := New()
	r.Add("$1", "the pizza tonight was great", Reaction{Key: "🍕"})
	r.Add("$1", "the pizza tonight was great", Reaction{Key: "🍕"})
	r.Add("$2", "more pizza for everyone", Reaction{Key: "🍕"})
	r.Add("$3", "the deploy failed again", Reaction{Key: "😭"})
	r.Add("$4", "deploy is broken, the build failed", Reaction{Key: "😭"})
	r.Add("$5", "the weather is nice", Reaction{Key: "lol"}) // used once: never suggested

	rc, score, ok := r.Suggest("who wants pizza?")
	if !ok || rc.Key != "🍕" || score < 0.9 {
		t.Fatalf("pizza: got %q %.2f %v", rc.Key, score, ok)
	}
	rc, _, ok = r.Suggest("the deploy failed")
	if !ok || rc.Key != "😭" {
		t.Fatalf("deploy: got %q %v", rc.Key, ok)
	}
	if _, _, ok = r.Suggest("completely unrelated words"); ok {
		t.Fatal("unrelated message got a suggestion")
	}
	if rc, _, ok = r.Suggest("nice weather"); ok && rc.Key == "lol" {
		t.Fatal("key used once was suggested")
	}
}

func TestRemoveUndoesAdd(t *testing.T) {
	r := New()
	r.Add("$1", "cats are great", Reaction{Key: "🐱", Shortcode: ""})
	r.Add("$2", "cats again", Reaction{Key: "🐱"})
	if _, _, ok := r.Suggest("cats"); !ok {
		t.Fatal("expected suggestion")
	}
	r.Remove("$1", "🐱")
	r.Remove("$2", "🐱")
	r.Remove("$2", "🐱") // extra remove is a no-op
	if _, _, ok := r.Suggest("cats"); ok {
		t.Fatal("suggestion survived removal")
	}
	if len(r.msgs) != 0 || len(r.wordKey) != 0 || len(r.keyCount) != 0 || len(r.wordDF) != 0 {
		t.Fatalf("state not empty after removal: %+v", r)
	}
}
