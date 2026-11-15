package bot

import (
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestDecideTrigger(t *testing.T) {
	me := identity{userID: "@markov:example.org", displayName: "Markov Bot"}
	sent := newSentLog(4)
	sent.add("$mine")
	kw := []string{"chain", "hello there"}

	cases := []struct {
		name   string
		msg    *event.MessageEventContent
		chance float64
		roll   float64
		want   triggerReason
	}{
		{"plain", &event.MessageEventContent{Body: "nothing here"}, 0, 0, reasonNone},
		{"m.mentions", &event.MessageEventContent{Body: "hi", Mentions: &event.Mentions{UserIDs: []id.UserID{me.userID}}}, 0, 0, reasonMention},
		{"display name", &event.MessageEventContent{Body: "Markov Bot: say something"}, 0, 0, reasonMention},
		{"localpart word", &event.MessageEventContent{Body: "hey markov"}, 0, 0, reasonMention},
		{"localpart substring", &event.MessageEventContent{Body: "markovian ideas"}, 0, 0, reasonNone},
		{"reply to bot", &event.MessageEventContent{Body: "ok", RelatesTo: (&event.RelatesTo{}).SetReplyTo("$mine")}, 0, 0, reasonReply},
		{"reply to other", &event.MessageEventContent{Body: "ok", RelatesTo: (&event.RelatesTo{}).SetReplyTo("$theirs")}, 0, 0, reasonNone},
		{"keyword", &event.MessageEventContent{Body: "a Chain!"}, 0, 0, reasonKeyword},
		{"keyword phrase", &event.MessageEventContent{Body: "well, hello there."}, 0, 0, reasonKeyword},
		{"keyword substring", &event.MessageEventContent{Body: "blockchains"}, 0, 0, reasonNone},
		{"random hit", &event.MessageEventContent{Body: "x"}, 0.5, 0.1, reasonRandom},
		{"random miss", &event.MessageEventContent{Body: "x"}, 0.5, 0.9, reasonNone},
	}
	for _, c := range cases {
		if got := decideTrigger(me, c.msg, sent, kw, c.chance, c.roll); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAddressed(t *testing.T) {
	for r, want := range map[triggerReason]bool{
		reasonMention: true, reasonReply: true, reasonKeyword: false, reasonRandom: false, reasonNone: false,
	} {
		if got := r.addressed(); got != want {
			t.Errorf("%q.addressed() = %v, want %v", r, got, want)
		}
	}
}

func TestStripMention(t *testing.T) {
	me := identity{userID: "@markov:example.org", displayName: "Markov Bot"}
	for in, want := range map[string]string{
		"Markov Bot: hello world": "hello world",
		"markov, hi":              "hi",
		"markov is cool":          "markov is cool",
	} {
		if got := me.stripMention(in); got != want {
			t.Errorf("stripMention(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSentLogEvicts(t *testing.T) {
	s := newSentLog(2)
	s.add("$1")
	s.add("$2")
	s.add("$3")
	if s.has("$1") || !s.has("$2") || !s.has("$3") {
		t.Fatal("sentLog did not evict oldest entry")
	}
}
