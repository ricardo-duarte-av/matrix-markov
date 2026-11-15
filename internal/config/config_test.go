package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "seed.txt", "hello world.\n")
	p := write(t, dir, "config.yaml", `
homeserver: https://hs.example.org
username: bot
password: pw
pickle_key: pk
markov:
  keywords: [markov]
rooms:
  - id: "!a:example.org"
  - id: "#b:example.org"
    seed_file: seed.txt
    order: 3
    reply_chance: 0.5
    keywords: []
    learn: false
    react_chance: 1
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.RoomSettings(cfg.Rooms[0])
	if a.Order != 2 || a.MinWords != 3 || a.MaxWords != 30 || !a.Learn || len(a.Keywords) != 1 || a.SeedFile != "" {
		t.Fatalf("room a defaults wrong: %+v", a)
	}
	b := cfg.RoomSettings(cfg.Rooms[1])
	if a.ReactChance != 0.1 || a.ReactThreshold != 0.35 || b.ReactChance != 1 {
		t.Fatalf("react settings wrong: a=%+v b=%+v", a, b)
	}
	if b.Order != 3 || b.ReplyChance != 0.5 || b.Learn || len(b.Keywords) != 0 || b.SeedFile != filepath.Join(dir, "seed.txt") {
		t.Fatalf("room b overrides wrong: %+v", b)
	}
	if cfg.DataDir != filepath.Join(dir, "data") {
		t.Fatalf("data_dir not resolved relative to config: %s", cfg.DataDir)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "config.yaml", `
markov:
  order: 0
rooms:
  - id: "nope"
    reply_chance: 2
    react_threshold: -1
    seed_file: missing.txt
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"homeserver", "username", "password", "pickle_key", "rooms[0].id", "order", "reply_chance", "react_threshold", "seed_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
