package markov

import (
	"regexp"
	"strings"
	"unicode"
)

// UserPlaceholder stands in for a mentioned user in learned text. It has no
// letters or digits, so it's never used as a reply topic, and it's filled in
// with a display name only when a reply is generated.
const UserPlaceholder = "\x1a@\x1a"

var (
	urlRe    = regexp.MustCompile(`(?i)\b(?:https?|ftp|mxc|matrix):\S+`)
	userRe   = regexp.MustCompile(`@[^\s:@]+:[A-Za-z0-9.\-]+(?::\d+)?`)
	otherIDs = regexp.MustCompile(`[#!+][^\s:]+:[A-Za-z0-9.\-]+(?::\d+)?`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// Clean prepares a message body for learning: it drops reply-fallback quote
// lines and URLs, removes room IDs/aliases, and replaces user IDs with
// UserPlaceholder (so the bot never repeats an MXID). Returns one line.
func Clean(body string) string {
	lines := strings.Split(body, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), ">") {
			continue
		}
		kept = append(kept, l)
	}
	s := strings.Join(kept, " ")
	s = urlRe.ReplaceAllString(s, " ")
	s = userRe.ReplaceAllString(s, UserPlaceholder)
	s = otherIDs.ReplaceAllString(s, " ")
	s = spaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// HasIdentifierOrURL reports whether s contains a Matrix ID, alias or URL.
func HasIdentifierOrURL(s string) bool {
	return urlRe.MatchString(s) || userRe.MatchString(s) || otherIDs.MatchString(s)
}

// Tokenize splits cleaned text into words, keeping punctuation attached.
func Tokenize(text string) []string {
	return strings.Fields(text)
}

// Normalize maps a token to a lowercase key without surrounding punctuation,
// used for topic and reaction lookups.
func Normalize(tok string) string {
	return strings.ToLower(strings.TrimFunc(tok, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}))
}
