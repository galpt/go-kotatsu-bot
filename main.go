package main

import (
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bwmarrin/discordgo"
)

func main() {
	cfg, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	token := cfg.DiscordToken
	if token == "" {
		log.Fatal("Discord token required via config.yaml or DISCORD_TOKEN env var")
	}
	watchedMap := map[string]bool{}
	for _, id := range cfg.ForumParentIDs {
		watchedMap[strings.TrimSpace(id)] = true
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("error creating Discord session: %v", err)
	}

	// ensure gateway intents include message content so the bot can read command messages
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsGuildMessageReactions | discordgo.IntentsMessageContent

	h := &handler{dg: dg, watchedParents: watchedMap, token: token, cfg: cfg}

	dg.AddHandler(h.onMessageCreate)

	if err := dg.Open(); err != nil {
		log.Fatalf("error opening connection: %v", err)
	}
	defer dg.Close()

	log.Printf("Bot is now running. Watching %d forum parents. Press CTRL-C to exit.", len(watchedMap))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("Shutting down")
}

// handler holds runtime state
type handler struct {
	dg             *discordgo.Session
	watchedParents map[string]bool
	token          string
	cfg            *Config
}
