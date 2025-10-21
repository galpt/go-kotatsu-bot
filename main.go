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

	// Enable automatic rate limit retry handling
	dg.ShouldRetryOnRateLimit = true

	// ensure gateway intents include message content so the bot can read command messages
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsGuildMessageReactions | discordgo.IntentsMessageContent

	h := &handler{dg: dg, watchedParents: watchedMap, token: token, cfg: cfg}

	dg.AddHandler(h.onMessageCreate)

	if err := dg.Open(); err != nil {
		log.Fatalf("error opening connection: %v", err)
	}
	defer dg.Close()

	// Startup validation: verify configured forum parent IDs are accessible and look like forums
	if len(cfg.ForumParentIDs) > 0 {
		for _, pid := range cfg.ForumParentIDs {
			ch, err := dg.Channel(pid)
			if err != nil {
				log.Printf("startup: cannot access parent channel %s: %v - check that the bot is a member of the server and the ID is correct", pid, err)
				continue
			}
			// If the channel type isn't a forum, warn the admin
			// Discord's forum channel type currently is 15. Some discordgo versions may not expose a named constant.
			if ch.Type != discordgo.ChannelType(15) {
				log.Printf("startup: channel %s exists but is not a Forum channel (type=%d). It may be a thread or text channel.", pid, ch.Type)
			} else {
				log.Printf("startup: forum parent %s OK (name=%q)", pid, ch.Name)
			}
		}
	}

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
