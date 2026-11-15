package bot

import (
	"regexp"
	"strings"
	"sync"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type triggerReason string

const (
	reasonNone    triggerReason = ""
	reasonMention triggerReason = "mention"
	reasonReply   triggerReason = "reply"
	reasonKeyword triggerReason = "keyword"
	reasonRandom  triggerReason = "random"
)

// identity is what the bot knows about itself for mention detection.
type identity struct {
	userID      id.UserID
	displayName string
}

// mentioned reports whether a message mentions the bot, either through
// m.mentions or by its MXID, localpart or display name appearing in the body.
func (me identity) mentioned(msg *event.MessageEventContent) bool {
	if msg.Mentions != nil && msg.Mentions.Has(me.userID) {
		return true
	}
	body := strings.ToLower(msg.Body)
	if strings.Contains(body, strings.ToLower(me.userID.String())) {
		return true
	}
	for _, name := range []string{me.displayName, me.userID.Localpart()} {
		if name != "" && containsWord(body, strings.ToLower(name)) {
			return true
		}
	}
	return false
}

// stripMention removes a leading "Name:" / "Name," addressing the bot, so the
// bot's own name isn't learned as part of the room's text.
func (me identity) stripMention(body string) string {
	trimmed := strings.TrimSpace(body)
	lower := strings.ToLower(trimmed)
	for _, name := range []string{me.displayName, me.userID.Localpart()} {
		n := strings.ToLower(name)
		if n == "" || !strings.HasPrefix(lower, n) {
			continue
		}
		rest := trimmed[len(n):]
		if strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, ",") {
			return strings.TrimSpace(rest[1:])
		}
	}
	return body
}

// keywordMatch reports whether any keyword appears as a whole word or phrase
// (case-insensitive).
func keywordMatch(body string, keywords []string) bool {
	body = strings.ToLower(body)
	for _, k := range keywords {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" && containsWord(body, k) {
			return true
		}
	}
	return false
}

var wordRe sync.Map // string -> *regexp.Regexp

func containsWord(haystack, word string) bool {
	re, ok := wordRe.Load(word)
	if !ok {
		re, _ = wordRe.LoadOrStore(word, regexp.MustCompile(`(^|[^\pL\pN_])`+regexp.QuoteMeta(word)+`($|[^\pL\pN_])`))
	}
	return re.(*regexp.Regexp).MatchString(haystack)
}

// sentLog remembers the most recent event IDs the bot sent, so replies to
// them can be recognised without fetching (and decrypting) the parent event.
type sentLog struct {
	mu   sync.Mutex
	ids  map[id.EventID]struct{}
	ring []id.EventID
	pos  int
}

func newSentLog(size int) *sentLog {
	return &sentLog{ids: make(map[id.EventID]struct{}, size), ring: make([]id.EventID, size)}
}

func (s *sentLog) add(evtID id.EventID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.ring[s.pos]; old != "" {
		delete(s.ids, old)
	}
	s.ring[s.pos] = evtID
	s.ids[evtID] = struct{}{}
	s.pos = (s.pos + 1) % len(s.ring)
}

func (s *sentLog) has(evtID id.EventID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[evtID]
	return ok
}

// decideTrigger picks why (if at all) the bot should answer a message.
// roll is a uniform random number in [0,1).
func decideTrigger(me identity, msg *event.MessageEventContent, sent *sentLog, keywords []string, chance, roll float64) triggerReason {
	switch {
	case me.mentioned(msg):
		return reasonMention
	case msg.RelatesTo.GetReplyTo() != "" && sent.has(msg.RelatesTo.GetReplyTo()):
		return reasonReply
	case keywordMatch(msg.Body, keywords):
		return reasonKeyword
	case chance > 0 && roll < chance:
		return reasonRandom
	}
	return reasonNone
}
