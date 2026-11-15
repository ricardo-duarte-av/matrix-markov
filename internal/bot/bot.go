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
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"matrix-markov/internal/config"
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
	if b.brain, err = NewBrain(ctx, b.store, rooms); err != nil {
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
		b.log.Warn().Err(err).Stringer("room_id", evt.RoomID).Stringer("event_id", evt.ID).Msg("Could not decrypt message")
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
			continue
		}
		if _, err := b.cli.JoinRoomByID(ctx, roomID); err != nil {
			b.log.Warn().Err(err).Stringer("room_id", roomID).Msg("Could not join configured room yet (invite the bot)")
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
	// Allowed-room check comes first: nothing from other rooms is learned or answered.
	settings, ok := b.brain.Settings(evt.RoomID)
	if !ok || evt.Sender == b.cli.UserID {
		return
	}
	msg := evt.Content.AsMessage()
	if msg == nil || (msg.MsgType != event.MsgText && msg.MsgType != event.MsgEmote) {
		return
	}
	if msg.RelatesTo.GetReplaceID() != "" || msg.NewContent != nil {
		return // edits
	}
	msg.RemoveReplyFallback()
	log := b.log.With().Stringer("room_id", evt.RoomID).Stringer("event_id", evt.ID).Logger()

	reason := decideTrigger(b.me, msg, b.sent, settings.Keywords, settings.ReplyChance, rand.Float64())
	body := b.me.stripMention(msg.Body)

	if err := b.brain.Learn(ctx, evt.RoomID, evt.ID, evt.Sender, body, evt.Timestamp); err != nil {
		log.Error().Err(err).Msg("Failed to learn message")
	}

	if reason == reasonNone {
		return
	}
	if age := time.Since(time.UnixMilli(evt.Timestamp)); age > replyMaxAge {
		log.Debug().Dur("age", age).Msg("Not replying to old message")
		return
	}
	text := b.brain.Reply(evt.RoomID, body)
	if text == "" {
		log.Debug().Str("reason", string(reason)).Msg("Nothing to say yet")
		return
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: text}
	if reason != reasonRandom {
		content.SetReply(evt)
	} else {
		content.Mentions = &event.Mentions{}
	}
	resp, err := b.cli.SendMessageEvent(ctx, evt.RoomID, event.EventMessage, content)
	if err != nil {
		log.Error().Err(err).Msg("Failed to send reply")
		return
	}
	b.sent.add(resp.EventID)
	log.Debug().Str("reason", string(reason)).Msg("Replied")
}
