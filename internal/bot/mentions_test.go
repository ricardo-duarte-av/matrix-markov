package bot

import (
	"testing"

	"maunium.net/go/mautrix/event"

	"matrix-markov/internal/markov"
)

func TestLearnableBody(t *testing.T) {
	me := identity{userID: "@markov:example.org", displayName: "Markov Bot"}
	P := markov.UserPlaceholder
	cases := []struct {
		name string
		msg  event.MessageEventContent
		want string
	}{
		{
			name: "pill of another user (the gomuks example)",
			msg: event.MessageEventContent{
				Body:          "Penny act 1 is sunshine and rainbows act 2 is trauma",
				Format:        event.FormatHTML,
				FormattedBody: `<a href="https://matrix.to/#/%40plate%3Asquarebowl.club">Penny</a> act 1 is sunshine and rainbows act 2 is trauma`,
			},
			want: P + " act 1 is sunshine and rainbows act 2 is trauma",
		},
		{
			name: "matrix: URI pill, name with markup and entities",
			msg: event.MessageEventContent{
				Body:          "thanks Tom & Jerry: nice",
				Format:        event.FormatHTML,
				FormattedBody: `thanks <a href="matrix:u/tom:example.org?action=chat"><b>Tom &amp; Jerry</b></a>: nice`,
			},
			want: "thanks " + P + ": nice",
		},
		{
			name: "pill of the bot is removed",
			msg: event.MessageEventContent{
				Body:          "Markov Bot: what do you think",
				Format:        event.FormatHTML,
				FormattedBody: `<a href="https://matrix.to/#/@markov:example.org">Markov Bot</a>: what do you think`,
			},
			want: "what do you think",
		},
		{
			name: "plain-text name of the bot",
			msg:  event.MessageEventContent{Body: "markov, hi"},
			want: "hi",
		},
		{
			name: "non-user links are left alone",
			msg: event.MessageEventContent{
				Body:          "see the room docs",
				Format:        event.FormatHTML,
				FormattedBody: `see the <a href="https://matrix.to/#/#room:example.org">room</a> docs`,
			},
			want: "see the room docs",
		},
	}
	for _, c := range cases {
		if got := me.learnableBody(&c.msg); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
