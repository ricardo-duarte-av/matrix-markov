// Package config loads and validates the bot's config.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Homeserver string `yaml:"homeserver"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	PickleKey  string `yaml:"pickle_key"`
	DeviceName string `yaml:"device_name"`
	DataDir    string `yaml:"data_dir"`
	LogLevel   string `yaml:"log_level"`
	Markov     Markov `yaml:"markov"`
	Rooms      []Room `yaml:"rooms"`
}

// Markov holds the global chain/reply settings. Rooms may override some of them.
type Markov struct {
	Order       int      `yaml:"order"`
	MinWords    int      `yaml:"min_words"`
	MaxWords    int      `yaml:"max_words"`
	ReplyChance float64  `yaml:"reply_chance"`
	Keywords    []string `yaml:"keywords"`
	SeedFile    string   `yaml:"seed_file"`
	MaxMessages int      `yaml:"max_messages"`
	// ReactChance is the probability of reacting when a reaction scores at
	// least ReactThreshold (0..1).
	ReactChance    float64 `yaml:"react_chance"`
	ReactThreshold float64 `yaml:"react_threshold"`
}

// Room is one allowed room. Pointer fields are optional overrides of Markov.
type Room struct {
	ID          string    `yaml:"id"`
	SeedFile    *string   `yaml:"seed_file"`
	Order       *int      `yaml:"order"`
	ReplyChance *float64  `yaml:"reply_chance"`
	Keywords    *[]string `yaml:"keywords"`
	Learn       *bool     `yaml:"learn"`

	ReactChance    *float64 `yaml:"react_chance"`
	ReactThreshold *float64 `yaml:"react_threshold"`
}

// RoomSettings is the fully-resolved configuration for one room.
type RoomSettings struct {
	ID          string
	SeedFile    string
	Order       int
	MinWords    int
	MaxWords    int
	ReplyChance float64
	Keywords    []string
	Learn       bool
	MaxMessages int

	ReactChance    float64
	ReactThreshold float64
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{
		DataDir:    "./data",
		DeviceName: "Markov Bot",
		LogLevel:   "info",
		Markov: Markov{
			Order:       2,
			MinWords:    3,
			MaxWords:    30,
			ReplyChance: 0.02,
			MaxMessages: 50000,

			ReactChance:    0.1,
			ReactThreshold: 0.35,
		},
	}
	if err = yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// Relative seed paths are resolved against the config file's directory.
	cfg.resolvePaths(filepath.Dir(path))
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) resolvePaths(base string) {
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	c.DataDir = abs(c.DataDir)
	c.Markov.SeedFile = abs(c.Markov.SeedFile)
	for i := range c.Rooms {
		if c.Rooms[i].SeedFile != nil {
			p := abs(*c.Rooms[i].SeedFile)
			c.Rooms[i].SeedFile = &p
		}
	}
}

func (c *Config) Validate() error {
	var errs []error
	if c.Homeserver == "" {
		errs = append(errs, errors.New("homeserver is required"))
	}
	if c.Username == "" {
		errs = append(errs, errors.New("username is required"))
	}
	if c.Password == "" {
		errs = append(errs, errors.New("password is required"))
	}
	if c.PickleKey == "" {
		errs = append(errs, errors.New("pickle_key is required (any long random string; keep it stable)"))
	}
	if len(c.Rooms) == 0 {
		errs = append(errs, errors.New("at least one room is required"))
	}
	if c.Markov.MinWords < 1 || c.Markov.MaxWords < c.Markov.MinWords {
		errs = append(errs, errors.New("markov.min_words must be >= 1 and <= markov.max_words"))
	}
	if c.Markov.MaxMessages < 0 {
		errs = append(errs, errors.New("markov.max_messages must be >= 0"))
	}
	seen := map[string]bool{}
	for i, r := range c.Rooms {
		if r.ID == "" || (!strings.HasPrefix(r.ID, "!") && !strings.HasPrefix(r.ID, "#")) {
			errs = append(errs, fmt.Errorf("rooms[%d].id must be a room ID (!...) or alias (#...)", i))
		}
		if seen[r.ID] {
			errs = append(errs, fmt.Errorf("rooms[%d].id %q is duplicated", i, r.ID))
		}
		seen[r.ID] = true
		s := c.RoomSettings(r)
		if s.Order < 1 {
			errs = append(errs, fmt.Errorf("room %s: order must be >= 1", r.ID))
		}
		if s.ReplyChance < 0 || s.ReplyChance > 1 {
			errs = append(errs, fmt.Errorf("room %s: reply_chance must be between 0 and 1", r.ID))
		}
		if s.ReactChance < 0 || s.ReactChance > 1 {
			errs = append(errs, fmt.Errorf("room %s: react_chance must be between 0 and 1", r.ID))
		}
		if s.ReactThreshold < 0 || s.ReactThreshold > 1 {
			errs = append(errs, fmt.Errorf("room %s: react_threshold must be between 0 and 1", r.ID))
		}
		if s.SeedFile != "" {
			if _, err := os.Stat(s.SeedFile); err != nil {
				errs = append(errs, fmt.Errorf("room %s: seed_file: %w", r.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// RoomSettings merges the global markov settings with a room's overrides.
func (c *Config) RoomSettings(r Room) RoomSettings {
	s := RoomSettings{
		ID:          r.ID,
		SeedFile:    c.Markov.SeedFile,
		Order:       c.Markov.Order,
		MinWords:    c.Markov.MinWords,
		MaxWords:    c.Markov.MaxWords,
		ReplyChance: c.Markov.ReplyChance,
		Keywords:    c.Markov.Keywords,
		Learn:       true,
		MaxMessages: c.Markov.MaxMessages,

		ReactChance:    c.Markov.ReactChance,
		ReactThreshold: c.Markov.ReactThreshold,
	}
	if r.SeedFile != nil {
		s.SeedFile = *r.SeedFile
	}
	if r.Order != nil {
		s.Order = *r.Order
	}
	if r.ReplyChance != nil {
		s.ReplyChance = *r.ReplyChance
	}
	if r.Keywords != nil {
		s.Keywords = *r.Keywords
	}
	if r.Learn != nil {
		s.Learn = *r.Learn
	}
	if r.ReactChance != nil {
		s.ReactChance = *r.ReactChance
	}
	if r.ReactThreshold != nil {
		s.ReactThreshold = *r.ReactThreshold
	}
	return s
}
