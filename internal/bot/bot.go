// Package bot wires the Markov brain to a Matrix account, with E2EE.
package bot

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"matrix-markov/internal/config"
	"matrix-markov/internal/react"
	"matrix-markov/internal/store"
)

// replyMaxAge is how old a message may be and still get a reply. Older
// messages (e.g. those that arrive after a restart) are learned but not
// answered.
const replyMaxAge = 2 * time.Minute

type Bot struct {
	cfg    *config.Config
	log    zerolog.Logger
	cli    *mautrix.Client
	crypto *cryptohelper.CryptoHelper
	store  *store.Store
	brain  *Brain
	me     identity
	sent   *sentLog
	// once holds keys of notices that are logged only once per run.
	once sync.Map
}

// logOnce logs at info level the first time key is seen.
func (b *Bot) logOnce(key string, fn func()) {
	if _, seen := b.once.LoadOrStore(key, struct{}{}); !seen {
		fn()
	}
}

func New(cfg *config.Config, log zerolog.Logger) *Bot {
	return &Bot{cfg: cfg, log: log, sent: newSentLog(512)}
}

// Run logs in, sets up encryption, and syncs until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	if err := os.MkdirAll(b.cfg.DataDir, 0o700); err != nil {
		return err
	}
	var err error
	if b.store, err = store.Open(filepath.Join(b.cfg.DataDir, "markov.db")); err != nil {
		return fmt.Errorf("open markov db: %w", err)
	}
	defer b.store.Close()

	if err = b.login(ctx); err != nil {
		return err
	}
	defer b.crypto.Close()

	rooms, err := b.resolveRooms(ctx)
	if err != nil {
		return err
	}
	if b.brain, err = NewBrain(ctx, b.store, b.displayName, rooms); err != nil {
		return err
	}
	for roomID := range rooms {
		msgs, prefixes, _ := b.brain.Stats(ctx, roomID)
		b.log.Info().Stringer("room_id", roomID).Int("messages", msgs).Int("prefixes", prefixes).Msg("Loaded room chain")
	}
	b.joinRooms(ctx, rooms)

	syncer := b.cli.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnSync(b.cli.DontProcessOldEvents)
	syncer.OnEventType(event.StateMember, b.handleMember)
	syncer.OnEventType(event.EventMessage, b.handleMessage)
	syncer.OnEventType(event.EventReaction, b.handleReaction)
	syncer.OnEventType(event.EventRedaction, b.handleRedaction)

	b.log.Info().Stringer("user_id", b.cli.UserID).Stringer("device_id", b.cli.DeviceID).Msg("Bot is running")
	err = b.cli.SyncWithContext(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (b *Bot) login(ctx context.Context) error {
	cli, err := mautrix.NewClient(b.cfg.Homeserver, "", "")
	if err != nil {
		return err
	}
	cli.Log = b.log
	b.cli = cli

	cryptoPath := filepath.Join(b.cfg.DataDir, "crypto.db")
	// Pre-create with tight permissions; it holds the device's private keys.
	if f, err := os.OpenFile(cryptoPath, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		_ = f.Close()
	}
	helper, err := cryptohelper.NewCryptoHelper(cli, []byte(b.cfg.PickleKey), cryptoPath)
	if err != nil {
		return fmt.Errorf("crypto helper: %w", err)
	}
	// The helper logs in, and on later runs reuses the device ID stored in
	// crypto.db so the same device and keys are kept.
	helper.LoginAs = &mautrix.ReqLogin{
		Type:                     mautrix.AuthTypePassword,
		Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: b.cfg.Username},
		Password:                 b.cfg.Password,
		InitialDeviceDisplayName: b.cfg.DeviceName,
	}
	helper.DecryptErrorCallback = func(evt *event.Event, err error) {
		b.log.Warn().Err(err).Stringer("room_id", evt.RoomID).Stringer("event_id", evt.ID).Stringer("sender", evt.Sender).
			Msg("Could not decrypt message (the sender's client may not be sharing room keys with this device)")
	}
	if err = helper.Init(ctx); err != nil {
		return fmt.Errorf("login / crypto init: %w", err)
	}
	cli.Crypto = helper
	b.crypto = helper

	b.me = identity{userID: cli.UserID}
	if resp, err := cli.GetOwnDisplayName(ctx); err == nil {
		b.me.displayName = resp.DisplayName
	}
	return nil
}

// resolveRooms turns configured IDs/aliases into the allowed-room set.
func (b *Bot) resolveRooms(ctx context.Context) (map[id.RoomID]config.RoomSettings, error) {
	out := make(map[id.RoomID]config.RoomSettings, len(b.cfg.Rooms))
	for _, r := range b.cfg.Rooms {
		roomID := id.RoomID(r.ID)
		if strings.HasPrefix(r.ID, "#") {
			resp, err := b.cli.ResolveAlias(ctx, id.RoomAlias(r.ID))
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", r.ID, err)
			}
			roomID = resp.RoomID
		}
		if _, dup := out[roomID]; dup {
			return nil, fmt.Errorf("room %s is configured twice", roomID)
		}
		s := b.cfg.RoomSettings(r)
		s.ID = roomID.String()
		out[roomID] = s
	}
	return out, nil
}

// joinRooms joins every allowed room it can (public rooms or pending
// invites) and warns about joined rooms that aren't configured.
func (b *Bot) joinRooms(ctx context.Context, rooms map[id.RoomID]config.RoomSettings) {
	joined := map[id.RoomID]bool{}
	if resp, err := b.cli.JoinedRooms(ctx); err == nil {
		for _, r := range resp.JoinedRooms {
			joined[r] = true
			if _, ok := rooms[r]; !ok {
				b.log.Warn().Stringer("room_id", r).Msg("Joined a room that is not in config; ignoring it")
			}
		}
	}
	for roomID := range rooms {
		if joined[roomID] {
			b.log.Info().Stringer("room_id", roomID).Msg("Watching configured room")
			continue
		}
		if _, err := b.cli.JoinRoomByID(ctx, roomID); err != nil {
			b.log.Warn().Err(err).Stringer("room_id", roomID).Msg("Could not join configured room yet (invite the bot)")
		} else {
			b.log.Info().Stringer("room_id", roomID).Msg("Joined configured room")
		}
	}
}

func (b *Bot) handleMember(ctx context.Context, evt *event.Event) {
	if evt.GetStateKey() != b.cli.UserID.String() || evt.Content.AsMember().Membership != event.MembershipInvite {
		return
	}
	log := b.log.With().Stringer("room_id", evt.RoomID).Stringer("inviter", evt.Sender).Logger()
	if _, ok := b.brain.Settings(evt.RoomID); !ok {
		log.Info().Msg("Rejecting invite to a room that is not in config")
		_, _ = b.cli.LeaveRoom(ctx, evt.RoomID)
		return
	}
	if _, err := b.cli.JoinRoomByID(ctx, evt.RoomID); err != nil {
		log.Error().Err(err).Msg("Failed to join after invite")
		return
	}
	log.Info().Msg("Joined room after invite")
}

func (b *Bot) handleMessage(ctx context.Context, evt *event.Event) {
	log := b.log.With().Stringer("room_id", evt.RoomID).Stringer("event_id", evt.ID).Logger()
	log.Debug().Stringer("sender", evt.Sender).Bool("decrypted", evt.Mautrix.EventSource&event.SourceDecrypted != 0).Msg("Received message event")

	// Allowed-room check comes first: nothing from other rooms is learned or answered.
	settings, ok := b.brain.Settings(evt.RoomID)
	if !ok {
		b.logOnce("room:"+evt.RoomID.String(), func() {
			log.Info().Msg("Ignoring messages from a room that is not in config (logged once per room)")
		})
		return
	}
	if evt.Sender == b.cli.UserID {
		if evt.Unsigned.TransactionID == "" { // not an echo of something this process sent
			b.logOnce("self:"+evt.RoomID.String(), func() {
				log.Info().Msg("Ignoring messages sent by the bot's own account from another client; use a separate account for the bot (logged once per room)")
			})
		}
		return
	}
	msg := evt.Content.AsMessage()
	if msg == nil || (msg.MsgType != event.MsgText && msg.MsgType != event.MsgEmote) {
		log.Debug().Msg("Ignoring non-text message")
		return
	}
	if msg.RelatesTo.GetReplaceID() != "" || msg.NewContent != nil {
		log.Debug().Msg("Ignoring edit")
		return
	}
	msg.RemoveReplyFallback()

	reason := decideTrigger(b.me, msg, b.sent, settings.Keywords, settings.ReplyChance, rand.Float64())
	body := b.me.learnableBody(msg)

	// Message text is never logged above debug level: it may come from an
	// encrypted room.
	switch outcome, words, err := b.brain.Learn(ctx, evt.RoomID, evt.ID, evt.Sender, body, evt.Timestamp); {
	case err != nil:
		log.Error().Err(err).Msg("Failed to learn message")
	case outcome == LearnChained:
		log.Info().Stringer("sender", evt.Sender).Int("words", words).Msg("Added message to room corpus")
	case outcome == LearnStored:
		log.Info().Stringer("sender", evt.Sender).Int("words", words).Int("min_learn_words", settings.MinLearnWords).
			Msg("Stored short message for reactions (not added to chain)")
	default:
		log.Debug().Str("outcome", string(outcome)).Msg("Message not learned")
	}

	if age := time.Since(time.UnixMilli(evt.Timestamp)); age > replyMaxAge {
		log.Debug().Dur("age", age).Msg("Not responding to old message")
		return
	}

	replied := false
	if reason != reasonNone {
		replied = b.reply(ctx, evt, body, reason)
	}

	// React when a learned reaction fits well enough and the dice agree.
	// If the bot was addressed but had nothing to say, react instead
	// whenever it has any suggestion at all.
	rc, score, ok := b.brain.SuggestReaction(evt.RoomID, body)
	forced := reason.addressed() && !replied
	if ok && (forced || (score >= settings.ReactThreshold && rand.Float64() < settings.ReactChance)) {
		if err := b.sendReaction(ctx, evt.RoomID, evt.ID, rc); err != nil {
			log.Error().Err(err).Msg("Failed to send reaction")
		} else {
			log.Info().Str("key", rc.Key).Float64("score", score).Bool("forced", forced).Msg("Sent reaction")
		}
	}
}

func (b *Bot) reply(ctx context.Context, evt *event.Event, body string, reason triggerReason) bool {
	log := b.log.With().Stringer("room_id", evt.RoomID).Stringer("event_id", evt.ID).Logger()
	text := b.brain.Reply(ctx, evt.RoomID, body, evt.Sender)
	if text == "" {
		log.Info().Str("reason", string(reason)).Msg("Triggered, but the room corpus has nothing to say yet")
		return false
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: text}
	if reason.addressed() {
		content.SetReply(evt) // mentions only the person replied to
	} else {
		content.Mentions = &event.Mentions{} // keyword/random: just talk, mention nobody
	}
	resp, err := b.cli.SendMessageEvent(ctx, evt.RoomID, event.EventMessage, content)
	if err != nil {
		log.Error().Err(err).Msg("Failed to send reply")
		return false
	}
	b.sent.add(resp.EventID)
	log.Info().Str("reason", string(reason)).Int("words", len(strings.Fields(text))).Stringer("reply_id", resp.EventID).Msg("Sent reply")
	log.Debug().Str("text", text).Msg("Reply text")
	return true
}

func (b *Bot) handleReaction(ctx context.Context, evt *event.Event) {
	if _, ok := b.brain.Settings(evt.RoomID); !ok || evt.Sender == b.cli.UserID {
		return
	}
	content := evt.Content.AsReaction()
	if content == nil || content.RelatesTo.Type != event.RelAnnotation || content.RelatesTo.EventID == "" {
		return
	}
	rc := react.Reaction{Key: content.RelatesTo.Key}
	if sc, ok := evt.Content.Raw["com.beeper.reaction.shortcode"].(string); ok {
		rc.Shortcode = sc
	}
	log := b.log.With().Stringer("room_id", evt.RoomID).Stringer("event_id", evt.ID).Stringer("target", content.RelatesTo.EventID).Logger()
	learned, err := b.brain.LearnReaction(ctx, evt.RoomID, evt.ID, content.RelatesTo.EventID, evt.Sender, rc, evt.Timestamp)
	switch {
	case err != nil:
		log.Error().Err(err).Msg("Failed to learn reaction")
	case learned:
		log.Info().Stringer("sender", evt.Sender).Str("key", rc.Key).Msg("Learned reaction")
	default:
		log.Debug().Msg("Reaction not learned (unknown target, duplicate or invalid key)")
	}
}

func (b *Bot) handleRedaction(ctx context.Context, evt *event.Event) {
	if _, ok := b.brain.Settings(evt.RoomID); !ok {
		return
	}
	target := evt.Redacts
	if c := evt.Content.AsRedaction(); c != nil && c.Redacts != "" {
		target = c.Redacts
	}
	if target == "" {
		return
	}
	kind, err := b.brain.Forget(ctx, evt.RoomID, target)
	if err != nil {
		b.log.Error().Err(err).Stringer("room_id", evt.RoomID).Stringer("redacts", target).Msg("Failed to forget redacted event")
	} else if kind != "" {
		b.log.Info().Stringer("room_id", evt.RoomID).Stringer("redacts", target).Str("kind", kind).Msg("Forgot redacted " + kind)
	}
}

// sendReaction sends an m.reaction. mautrix never encrypts reactions, but
// keys are arbitrary text from the room, so in encrypted rooms the bot
// encrypts them itself (m.relates_to stays in cleartext, as the spec requires).
func (b *Bot) sendReaction(ctx context.Context, roomID id.RoomID, target id.EventID, rc react.Reaction) error {
	content := map[string]any{
		"m.relates_to": map[string]any{
			"rel_type": event.RelAnnotation,
			"event_id": target,
			"key":      rc.Key,
		},
	}
	if rc.Shortcode != "" {
		content["com.beeper.reaction.shortcode"] = rc.Shortcode
	}
	encrypted, err := b.cli.StateStore.IsEncrypted(ctx, roomID)
	if err != nil {
		return fmt.Errorf("check room encryption: %w", err)
	}
	if !encrypted {
		_, err = b.cli.SendMessageEvent(ctx, roomID, event.EventReaction, content)
		return err
	}
	enc, err := b.crypto.Encrypt(ctx, roomID, event.EventReaction, content)
	if err != nil {
		return fmt.Errorf("encrypt reaction: %w", err)
	}
	_, err = b.cli.SendMessageEvent(ctx, roomID, event.EventEncrypted, enc)
	return err
}

// displayName returns a user's display name in a room, or "" if they aren't
// currently joined to it.
func (b *Bot) displayName(ctx context.Context, roomID id.RoomID, userID id.UserID) string {
	m, err := b.cli.StateStore.GetMember(ctx, roomID, userID)
	if err != nil || m == nil || m.Membership != event.MembershipJoin {
		return ""
	}
	if m.Displayname != "" {
		return m.Displayname
	}
	return userID.Localpart()
}
