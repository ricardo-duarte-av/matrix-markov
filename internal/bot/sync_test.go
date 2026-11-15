package bot

import (
	"context"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func syncResp() *mautrix.RespSync {
	key := "@u:example.org"
	resp := &mautrix.RespSync{}
	resp.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{"!r:example.org": {}}
	room := resp.Rooms.Join["!r:example.org"]
	room.State.Events = []*event.Event{{Type: event.StateEncryption, StateKey: new(string)}}
	room.Timeline.Events = []*event.Event{
		{Type: event.EventEncrypted, ID: "$msg"},
		{Type: event.StateMember, StateKey: &key, ID: "$member"},
		{Type: event.EventMessage, ID: "$msg2"},
	}
	return resp
}

func TestDropInitialMessages(t *testing.T) {
	// First sync: messages dropped, state (including state in the timeline) kept.
	resp := syncResp()
	if !dropInitialMessages(context.Background(), resp, "") {
		t.Fatal("initial sync must still be processed (room state)")
	}
	room := resp.Rooms.Join["!r:example.org"]
	if len(room.Timeline.Events) != 1 || room.Timeline.Events[0].ID != "$member" {
		t.Fatalf("initial timeline = %v, want only the member event", room.Timeline.Events)
	}
	if len(room.State.Events) != 1 {
		t.Fatal("initial state events were dropped")
	}

	// Later syncs are untouched.
	resp = syncResp()
	if !dropInitialMessages(context.Background(), resp, "s123") {
		t.Fatal("later sync must be processed")
	}
	if n := len(resp.Rooms.Join["!r:example.org"].Timeline.Events); n != 3 {
		t.Fatalf("later sync timeline has %d events, want 3", n)
	}
}
