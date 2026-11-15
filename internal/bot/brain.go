package bot

import (
	"bufio"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"

	"maunium.net/go/mautrix/id"

	"matrix-markov/internal/config"
	"matrix-markov/internal/markov"
	"matrix-markov/internal/react"
	"matrix-markov/internal/store"
)

// maxRecentSenders bounds the per-room list of people whose names may be
// used to fill in mention placeholders.
const maxRecentSenders = 50

// NameFunc returns the display name of a user who is currently joined to a
// room, or "" if they aren't (or can't be resolved).
type NameFunc func(ctx context.Context, roomID id.RoomID, userID id.UserID) string

// Brain holds one Markov chain and one reaction model per room. It is the
// only place where text is learned or generated. Every method is keyed by
// room ID, and a room's models are only ever fed that room's own messages,
// reactions and senders plus its operator-configured seed file, so nothing
// moves between rooms.
type Brain struct {
	store *store.Store
	names NameFunc
	rooms map[id.RoomID]*roomBrain
}

type roomBrain struct {
	settings config.RoomSettings

	mu      sync.RWMutex // guards the fields below (swapped on rebuild)
	chain   *markov.Chain
	reactor *react.Reactor
	senders []id.UserID // most recent first
}

// NewBrain builds the models of every allowed room from its seed file and
// stored data.
func NewBrain(ctx context.Context, st *store.Store, names NameFunc, rooms map[id.RoomID]config.RoomSettings) (*Brain, error) {
	b := &Brain{store: st, names: names, rooms: make(map[id.RoomID]*roomBrain, len(rooms))}
	for roomID, s := range rooms {
		rb := &roomBrain{settings: s}
		if err := b.rebuild(ctx, roomID, rb); err != nil {
			return nil, err
		}
		b.rooms[roomID] = rb
	}
	return b, nil
}

// rebuild (re)loads one room's models from its seed file and the database.
func (b *Brain) rebuild(ctx context.Context, roomID id.RoomID, rb *roomBrain) error {
	s := rb.settings
	chain := markov.NewChain(s.Order)
	if s.SeedFile != "" {
		if err := loadSeed(chain, s.SeedFile); err != nil {
			return fmt.Errorf("room %s: %w", roomID, err)
		}
	}
	_, err := b.store.ForEach(ctx, roomID.String(), func(body string) {
		if len(markov.Tokenize(body)) >= s.MinLearnWords {
			chain.Add(body)
		}
	})
	if err != nil {
		return fmt.Errorf("room %s: load messages: %w", roomID, err)
	}
	reactor := react.New()
	err = b.store.ForEachReaction(ctx, roomID.String(), func(r store.Reaction) {
		reactor.Add(r.Target, r.TargetBody, react.Reaction{Key: r.Key, Shortcode: r.Shortcode})
	})
	if err != nil {
		return fmt.Errorf("room %s: load reactions: %w", roomID, err)
	}
	recent, err := b.store.RecentSenders(ctx, roomID.String(), maxRecentSenders)
	if err != nil {
		return fmt.Errorf("room %s: load senders: %w", roomID, err)
	}
	senders := make([]id.UserID, len(recent))
	for i, u := range recent {
		senders[i] = id.UserID(u)
	}

	rb.mu.Lock()
	rb.chain, rb.reactor, rb.senders = chain, reactor, senders
	rb.mu.Unlock()
	return nil
}

func (rb *roomBrain) models() (*markov.Chain, *react.Reactor) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.chain, rb.reactor
}

func (rb *roomBrain) noteSender(u id.UserID) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.senders = slices.DeleteFunc(rb.senders, func(x id.UserID) bool { return x == u })
	rb.senders = slices.Insert(rb.senders, 0, u)
	if len(rb.senders) > maxRecentSenders {
		rb.senders = rb.senders[:maxRecentSenders]
	}
}

func (rb *roomBrain) recentSenders() []id.UserID {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return slices.Clone(rb.senders)
}

// Settings returns a room's settings, and false if the room isn't allowed.
func (b *Brain) Settings(roomID id.RoomID) (config.RoomSettings, bool) {
	rb, ok := b.rooms[roomID]
	if !ok {
		return config.RoomSettings{}, false
	}
	return rb.settings, true
}

// Stats returns (stored messages, chain prefixes) for one room.
func (b *Brain) Stats(ctx context.Context, roomID id.RoomID) (int, int, error) {
	rb, ok := b.rooms[roomID]
	if !ok {
		return 0, 0, fmt.Errorf("room %s is not allowed", roomID)
	}
	n, err := b.store.Count(ctx, roomID.String())
	chain, _ := rb.models()
	return n, chain.Size(), err
}

// LearnOutcome says what Learn did with a message.
type LearnOutcome string

const (
	LearnIgnored   LearnOutcome = "ignored"   // room not allowed, learning off, or nothing left after cleaning
	LearnDuplicate LearnOutcome = "duplicate" // already stored (e.g. redelivered after restart)
	LearnStored    LearnOutcome = "stored"    // stored (for reactions) but too short for the chain
	LearnChained   LearnOutcome = "chained"   // stored and added to the chain
)

// Learn stores and learns one message in one room. body should already have
// mentions replaced by markov.UserPlaceholder. Every message with some text
// left after cleaning is stored, so reactions on it can be learned; only
// those with at least min_learn_words words are added to the chain.
func (b *Brain) Learn(ctx context.Context, roomID id.RoomID, eventID id.EventID, sender id.UserID, body string, ts int64) (LearnOutcome, int, error) {
	rb, ok := b.rooms[roomID]
	if !ok || !rb.settings.Learn {
		return LearnIgnored, 0, nil
	}
	clean := markov.Clean(body)
	words := len(markov.Tokenize(clean))
	if words == 0 {
		return LearnIgnored, 0, nil
	}
	inserted, err := b.store.Add(ctx, roomID.String(), eventID.String(), sender.String(), clean, ts)
	if err != nil {
		return LearnIgnored, words, err
	} else if !inserted {
		return LearnDuplicate, words, nil
	}
	rb.noteSender(sender)
	outcome := LearnStored
	if words >= rb.settings.MinLearnWords {
		chain, _ := rb.models()
		chain.Add(clean)
		outcome = LearnChained
	}
	// Pruning only bounds the database; the in-memory chain shrinks on restart.
	return outcome, words, b.store.Prune(ctx, roomID.String(), rb.settings.MaxMessages)
}

// LearnReaction records a reaction on a message and reports whether it was
// learned. Only reactions on messages stored in the same room are learned,
// and keys must pass react.ValidKey.
func (b *Brain) LearnReaction(ctx context.Context, roomID id.RoomID, eventID, target id.EventID, sender id.UserID, rc react.Reaction, ts int64) (bool, error) {
	rb, ok := b.rooms[roomID]
	if !ok || !rb.settings.Learn || !react.ValidKey(rc.Key) {
		return false, nil
	}
	if !react.ValidShortcode(rc.Shortcode) || !strings.HasPrefix(rc.Key, "mxc://") {
		rc.Shortcode = ""
	}
	body, found, err := b.store.Message(ctx, roomID.String(), target.String())
	if err != nil || !found {
		return false, err
	}
	inserted, err := b.store.AddReaction(ctx, roomID.String(), store.Reaction{
		EventID: eventID.String(), Target: target.String(), Sender: sender.String(), Key: rc.Key, Shortcode: rc.Shortcode,
	}, ts)
	if err != nil || !inserted {
		return false, err
	}
	_, reactor := rb.models()
	reactor.Add(target.String(), body, rc)
	return true, nil
}

// Forget handles a redaction in one room: a redacted reaction is unlearned,
// and a redacted message is deleted and the room's models are rebuilt
// without it. It returns what was forgotten: "reaction", "message" or "".
func (b *Brain) Forget(ctx context.Context, roomID id.RoomID, eventID id.EventID) (string, error) {
	rb, ok := b.rooms[roomID]
	if !ok {
		return "", nil
	}
	r, err := b.store.DeleteReaction(ctx, roomID.String(), eventID.String())
	if err != nil {
		return "", err
	}
	if r != nil {
		_, reactor := rb.models()
		reactor.Remove(r.Target, r.Key)
		return "reaction", nil
	}
	deleted, err := b.store.DeleteMessage(ctx, roomID.String(), eventID.String())
	if err != nil || !deleted {
		return "", err
	}
	return "message", b.rebuild(ctx, roomID, rb)
}

// Reply generates a message for one room, using prompt (the triggering
// message from that same room) to pick a topic. Mention placeholders are
// filled with display names of people in that room. It returns "" if the
// room isn't allowed or there is nothing to say yet.
func (b *Brain) Reply(ctx context.Context, roomID id.RoomID, prompt string, trigger id.UserID) string {
	rb, ok := b.rooms[roomID]
	if !ok {
		return ""
	}
	r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	chain, _ := rb.models()
	seeds := markov.Tokenize(markov.Clean(prompt))
	out := chain.Generate(r, seeds, rb.settings.MinWords, rb.settings.MaxWords)
	if strings.Contains(out, markov.UserPlaceholder) {
		out = b.fillNames(ctx, r, roomID, rb, out, trigger)
	}
	return out
}

// fillNames replaces each placeholder with the display name of a recent
// sender in the same room (or the trigger's sender). Names are plain text
// and never added to m.mentions, so nobody is pinged.
func (b *Brain) fillNames(ctx context.Context, r *rand.Rand, roomID id.RoomID, rb *roomBrain, text string, trigger id.UserID) string {
	var names []string
	seen := map[id.UserID]bool{}
	for _, u := range append(rb.recentSenders(), trigger) {
		if u == "" || seen[u] || b.names == nil {
			continue
		}
		seen[u] = true
		if n := strings.TrimSpace(b.names(ctx, roomID, u)); n != "" && !strings.Contains(n, markov.UserPlaceholder) {
			names = append(names, n)
		}
	}
	parts := strings.Split(text, markov.UserPlaceholder)
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 && len(names) > 0 {
			sb.WriteString(names[r.IntN(len(names))])
		}
		sb.WriteString(p)
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

// SuggestReaction returns the best reaction for a message in one room,
// using only that room's learned reactions.
func (b *Brain) SuggestReaction(roomID id.RoomID, body string) (react.Reaction, float64, bool) {
	rb, ok := b.rooms[roomID]
	if !ok {
		return react.Reaction{}, 0, false
	}
	_, reactor := rb.models()
	return reactor.Suggest(body)
}

// loadSeed feeds a plain-text file into a chain, one sentence at a time.
// Blank lines separate paragraphs; hard-wrapped lines inside a paragraph
// are joined, so books (e.g. from Project Gutenberg) work as-is.
func loadSeed(c *markov.Chain, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("seed file: %w", err)
	}
	defer f.Close()

	var para []string
	flush := func() {
		for _, s := range splitSentences(strings.Join(para, " ")) {
			c.Add(markov.Clean(s))
		}
		para = para[:0]
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			flush()
			continue
		}
		para = append(para, line)
	}
	flush()
	return sc.Err()
}

func splitSentences(text string) []string {
	var out []string
	var cur []string
	for _, w := range strings.Fields(text) {
		cur = append(cur, w)
		trimmed := strings.TrimRight(w, `"')]»”’`)
		if strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, "!") || strings.HasSuffix(trimmed, "?") {
			out = append(out, strings.Join(cur, " "))
			cur = cur[:0]
		}
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	return out
}
