// Command matrix-markov is a Matrix bot that replies with Markov-chain text
// learned separately in each room.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"matrix-markov/internal/bot"
	"matrix-markov/internal/config"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	dataDir := flag.String("data-dir", "", "override data_dir from the config (used by the Docker image)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.DateTime}).
		Level(level).With().Timestamp().Logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := bot.New(cfg, log).Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("Bot stopped")
	}
	log.Info().Msg("Shut down")
}
