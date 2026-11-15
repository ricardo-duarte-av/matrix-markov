package markov

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	urlRe   = regexp.MustCompile(`(?i)\b(?:https?|ftp|mxc)://\S+`)
	mxidRe  = regexp.MustCompile(`[@#!+][^\s:]+:[A-Za-z0-9.\-]+(?::\d+)?`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// Clean strips parts of a message body that shouldn't be learned:
// reply-fallback quote lines, URLs and Matrix identifiers (so the bot never
// pings anyone by repeating an MXID). It returns a single-line string.
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
	s = mxidRe.ReplaceAllString(s, " ")
	s = spaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// Tokenize splits cleaned text into words, keeping punctuation attached.
func Tokenize(text string) []string {
	return strings.Fields(text)
}

// normalize maps a token to the key used for seed-word lookup.
func normalize(tok string) string {
	return strings.ToLower(strings.TrimFunc(tok, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}))
}
