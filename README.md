# matrix-markov

A Matrix bot that learns how each room talks and replies with Markov-chain text.
It's written in Go with [mautrix-go](https://github.com/mautrix/go) and supports end-to-end encrypted rooms.

## Features

- **Rooms are kept apart.** Each allowed room has its own chain, built only from that room's messages and its own optional seed file. No chain is shared. Messages from rooms that aren't configured are ignored, and invites to them are rejected.
- **E2EE.** Uses mautrix's crypto helper. The device and its keys are kept in `data/crypto.db`, so restarts reuse the same device.
- **Triggers.** The bot answers **as a reply** when it is addressed:
  - it is mentioned (via `m.mentions`, its MXID, display name or localpart)
  - someone replies to one of its messages

  It **just talks** in the room (a plain message that mentions nobody) when:
  - a configured keyword appears
  - a random draw against `reply_chance` hits
- **Mentions become placeholders.** When someone is mentioned (a pill in `formatted_body`, or a raw MXID), the mention is stored as a placeholder. When the bot speaks, each placeholder is filled with the display name of someone currently in the same room: a recent sender, or the person who triggered the reply. Names are plain text and never go into `m.mentions`, so nobody gets pinged. Mentions of the bot itself are removed.
- **Reactions.** The bot learns which reactions people put on which words, per room, and reacts to new messages with the best match. See [Reactions](#reactions).
- **Redactions are honoured.** When a learned message is redacted, it is deleted and the room's chain is rebuilt without it. When a reaction is redacted, it is unlearned.
- **Configurable N-order.** Set it globally or per room. Changing it only needs a restart, because the chain is rebuilt from the stored raw messages.

## Build and run

Needs Go 1.26 or newer (`GOTOOLCHAIN=auto` fetches it automatically) and a C compiler (for SQLite).

```sh
cp config.example.yaml config.yaml   # then edit it
go build -tags goolm -o markov-bot .
./markov-bot -config config.yaml
```

`-tags goolm` uses the pure-Go olm implementation. Without the tag, the bot links against the system `libolm`, which is deprecated.

Invite the bot to each configured room, or make the room public, and it joins.

### Docker

CI builds and pushes `ghcr.io/ricardo-duarte-av/matrix-markov` on every push, for linux/amd64 and linux/arm64. Each architecture builds on a native runner, so no emulation is involved. The tags are:

- branch name
- `sha-<commit>`
- `latest` (default branch)
- semver (for `v*` tags)

```sh
cp config.example.yaml config.yaml   # then edit it
mkdir -p data
echo "PUID=$(id -u)" > .env && echo "PGID=$(id -g)" >> .env
docker compose up -d                 # or: docker compose up -d --build
```

The compose file mounts:

- `./config.yaml` at `/config/config.yaml` (read-only)
- `./data` at `/data`. The container ignores `data_dir` from the config and uses `/data` (it passes `-data-dir /data`).

Seed file paths are relative to `/config`. To use seed files, uncomment the `./seeds` volume in the compose file.

## Corpus and storage

- Each room learns from messages sent **after the bot joined**. It can't decrypt older history in encrypted rooms.
- `seed_file` is plain text, for example a book from Project Gutenberg. It is split into sentences and loaded into the chain at startup, so the bot can talk from day one. Seed text is never written to the database. Pointing several rooms at the same seed file is fine, since it isn't room text.
- Learned messages are stored in `data/markov.db`, table `messages(event_id, room_id, sender, body, ts)`. Before storing, the bot strips reply quotes and URLs, and turns mentions into placeholders, so it never repeats an MXID or pings anyone.
- **Short messages:** a message is added to the chain only if it has at least `min_learn_words` words (default 2) left after cleaning. For example, `https://bsky.app/… lmao?` becomes just `lmao?` and stays out of the chain. It is still stored, though, so reactions on it are learned, and changing the threshold only needs a restart. Link-preview metadata (`com.beeper.linkpreviews`) is never learned. A message that is only a URL is not stored at all.
- To forget a room:

  ```sh
  sqlite3 data/markov.db "DELETE FROM messages WHERE room_id='!id:server'; DELETE FROM reactions WHERE room_id='!id:server'"
  ```

  Then restart the bot.

**Privacy:** `markov.db` holds **plaintext** copies of messages from encrypted rooms. `crypto.db` holds the bot's private keys. Both files are created with mode `0600`. Protect `data/` accordingly.

## Reactions

- **Learning:** an `m.reaction` is learned only if it targets a message stored from the **same room**. It is saved in the `reactions` table.
- **Keys:** reaction keys are arbitrary text, so a key is only accepted if:
  - it is at most 32 characters and 256 bytes
  - it is a single line with no control characters
  - it contains no Matrix IDs, aliases or URLs

  `mxc://` custom emoji are accepted, along with their `com.beeper.reaction.shortcode`.
- **Scoring:** for a new message, each key the room has used at least twice gets a score. The score is how often that key followed each of the message's words, weighted so that distinctive words count more than common ones like "the".
- **When it reacts:** the bot reacts when the best score is at least `react_threshold` and a random draw passes `react_chance`. If the bot was addressed (mentioned or replied to) but has nothing to say yet, it reacts instead, if it has any suggestion at all. It reacts at most once per message.
- **Encryption:** in encrypted rooms the bot encrypts its reactions itself (mautrix normally sends reactions unencrypted).

## Logging

At `log_level: info`, the bot logs one line per action, with the room and event IDs:

- `Added message to room corpus` / `Stored short message for reactions (not added to chain)`
- `Learned reaction`, `Forgot redacted message|reaction`
- `Sent reply` (with the trigger reason and word count), `Sent reaction` (with key and score)
- `Triggered, but the room corpus has nothing to say yet`

Message text is never logged above `debug`, because it may come from an encrypted room. At `debug`, the text of the bot's own replies is logged too.

## Choosing the order

| order | behaviour |
|------:|-----------|
| 1 | Mostly word salad. |
| 2 | **Default.** Good mix of coherence and novelty for chat-sized corpora. |
| 3+ | Very coherent, but it mostly repeats whole messages unless the room has tens of thousands of them. |

The generator avoids sending an exact copy of a learned message when it can.

## Tests

```sh
go test -tags goolm ./...
```

`internal/bot/isolation_test.go` feeds two rooms disjoint vocabularies and seed files. It then generates 4,000 replies, including replies prompted with the other room's text, and asserts that no foreign token ever appears.
