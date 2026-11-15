package bot

import (
	"html"
	"net/url"
	"regexp"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"matrix-markov/internal/markov"
)

var (
	anchorRe = regexp.MustCompile(`(?is)<a\s[^>]*?href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
)

type pill struct {
	userID id.UserID
	text   string // the visible link text, as it appears in the plain body
}

// userPills extracts user pills (links to matrix.to/#/@user or matrix:u/user)
// from an HTML body, in order.
func userPills(formatted string) []pill {
	var out []pill
	for _, m := range anchorRe.FindAllStringSubmatch(formatted, -1) {
		uid := pillUserID(html.UnescapeString(m[1]))
		if uid == "" {
			continue
		}
		text := strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(m[2], "")))
		out = append(out, pill{userID: uid, text: text})
	}
	return out
}

func pillUserID(href string) id.UserID {
	var raw string
	switch {
	case strings.HasPrefix(href, "https://matrix.to/#/"):
		raw = strings.TrimPrefix(href, "https://matrix.to/#/")
	case strings.HasPrefix(href, "matrix:u/"):
		raw = "@" + strings.TrimPrefix(href, "matrix:u/")
	default:
		return ""
	}
	if i := strings.IndexAny(raw, "?/"); i >= 0 {
		raw = raw[:i]
	}
	if dec, err := url.PathUnescape(raw); err == nil {
		raw = dec
	}
	if !strings.HasPrefix(raw, "@") || !strings.Contains(raw, ":") {
		return ""
	}
	return id.UserID(raw)
}

// learnableBody returns the text of a message to learn from:
//   - pills of other users become markov.UserPlaceholder (filled in with a
//     same-room display name when the bot speaks),
//   - pills, MXIDs and a leading "Name:" addressing the bot itself are
//     removed, so the bot doesn't learn its own name,
//   - raw MXIDs of others are turned into placeholders later by markov.Clean.
func (me identity) learnableBody(msg *event.MessageEventContent) string {
	body := msg.Body
	if msg.Format == event.FormatHTML && msg.FormattedBody != "" {
		for _, p := range userPills(msg.FormattedBody) {
			if p.text == "" {
				continue
			}
			repl := markov.UserPlaceholder
			if p.userID == me.userID {
				repl = ""
			}
			body = strings.Replace(body, p.text, repl, 1)
		}
	}
	body = strings.ReplaceAll(body, me.userID.String(), "")
	body = me.stripMention(body)
	body = strings.TrimSpace(body)
	// A removed leading bot pill leaves "": hello" behind.
	if strings.HasPrefix(body, ":") || strings.HasPrefix(body, ",") {
		body = strings.TrimSpace(body[1:])
	}
	return body
}
