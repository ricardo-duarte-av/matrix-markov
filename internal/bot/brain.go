package bot

import (
	"bufio"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"

	"maunium.net/go/mautrix/id"

	"matrix-markov/internal/config"
	"matrix-markov/internal/markov"
	"matrix-markov/internal/store"
)

// Brain holds one Markov chain per room. It is the only place where text is
// learned or generated. Every method is keyed by room ID, and a room's chain
// is only ever fed that room's own messages plus its operator-configured seed
// file, so text can't move between rooms.
type Brain struct {
	store *store.Store
	rooms map[id.RoomID]*roomBrain
}

type roomBrain struct {
	settings config.RoomSettings
	chain    *markov.Chain
}

// NewBrain builds a chain for every allowed room from its seed file and its
// stored messages.
func NewBrain(ctx context.Context, st *store.Store, rooms map[id.RoomID]config.RoomSettings) (*Brain, error) {
	b := &Brain{store: st, rooms: make(map[id.RoomID]*roomBrain, len(rooms))}
	for roomID, s := range rooms {
		rb := &roomBrain{settings: s, chain: markov.NewChain(s.Order)}
		if s.SeedFile != "" {
			if err := loadSeed(rb.chain, s.SeedFile); err != nil {
				return nil, fmt.Errorf("room %s: %w", roomID, err)
			}
		}
		if _, err := st.ForEach(ctx, roomID.String(), rb.chain.Add); err != nil {
			return nil, fmt.Errorf("room %s: load messages: %w", roomID, err)
		}
		b.rooms[roomID] = rb
	}
	return b, nil
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
	return n, rb.chain.Size(), err
}

// Learn stores and learns one message in one room. Rooms that aren't allowed
// or have learning off are ignored.
func (b *Brain) Learn(ctx context.Context, roomID id.RoomID, eventID id.EventID, sender id.UserID, body string, ts int64) error {
	rb, ok := b.rooms[roomID]
	if !ok || !rb.settings.Learn {
		return nil
	}
	clean := markov.Clean(body)
	if clean == "" {
		return nil
	}
	inserted, err := b.store.Add(ctx, roomID.String(), eventID.String(), sender.String(), clean, ts)
	if err != nil {
		return err
	}
	if !inserted {
		return nil // already learned (e.g. redelivered after restart)
	}
	rb.chain.Add(clean)
	// Pruning only bounds the database; the in-memory chain shrinks on restart.
	return b.store.Prune(ctx, roomID.String(), rb.settings.MaxMessages)
}

// Reply generates a message for one room, using prompt (the triggering
// message from that same room) to pick a topic. It returns "" if the room
// isn't allowed or there is nothing to say yet.
func (b *Brain) Reply(roomID id.RoomID, prompt string) string {
	rb, ok := b.rooms[roomID]
	if !ok {
		return ""
	}
	r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	seeds := markov.Tokenize(markov.Clean(prompt))
	return rb.chain.Generate(r, seeds, rb.settings.MinWords, rb.settings.MaxWords)
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
