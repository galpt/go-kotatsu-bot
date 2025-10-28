package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// commandConfig maps a short command to the title prefix and expected forum tag name
var commandConfig = map[string]struct {
	Prefix  string
	TagName string
}{
	"solved":    {Prefix: "[Solved]", TagName: ".Solved"},
	"aware":     {Prefix: "[Devs aware]", TagName: ".Devs aware"},
	"duplicate": {Prefix: "[Duplicate]", TagName: ".Duplicate"},
	"false":     {Prefix: "[False report]", TagName: ".False report"},
	"known":     {Prefix: "[Known issue]", TagName: ".Known issue"},
	"wrong":     {Prefix: "[Wrong channel]", TagName: ".Wrong channel"},
}

// onMessageCreate handles MessageCreate events
func (h *handler) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// ignore bot messages
	if m.Author == nil || m.Author.Bot {
		return
	}

	content := strings.TrimSpace(m.Content)
	if content == "" {
		return
	}

	// If the message is not a command (doesn't start with '.'), consider running the search feature
	if !strings.HasPrefix(content, ".") {
		// run the search flow if enabled in config and allowed in this channel
		// Fetch channel info first so we can evaluate NSFW and config channel restrictions
		ch, err := s.Channel(m.ChannelID)
		if err == nil {
			// do not block other flows if search fails
			go func() {
				if err := h.trySearchInMessage(s, m, ch); err != nil {
					// log but do not disrupt
					log.Printf("search handler error: %v", err)
				}
			}()
		}
		return
	}

	// parse command token (first word)
	token := strings.Fields(content)[0]
	cmd := strings.TrimPrefix(strings.ToLower(token), ".")

	// Special admin-only helper: .list-tags (moved down after channel fetch)

	cfg, ok := commandConfig[cmd]
	if !ok {
		return
	}

	// find channel
	ch, err := s.Channel(m.ChannelID)
	if err != nil {
		log.Printf("failed to fetch channel: %v", err)
		return
	}

	// must be a thread
	if !isThreadChannel(ch) {
		// ignore: not a thread
		return
	}

	// must be in watched parents if configured
	if len(h.watchedParents) > 0 {
		if ch.ParentID == "" || !h.watchedParents[ch.ParentID] {
			return
		}
	}

	// check if user has moderator-level permission in the guild
	has, err := h.userCanManagePosts(s, m.Author.ID, ch)
	if err != nil {
		log.Printf("permission check failed: %v", err)
		return
	}
	// If the command is list-tags, reply with available tags and applied tags (admin-only)
	if cmd == "list-tags" {
		if !has {
			if _, e := s.ChannelMessageSend(m.ChannelID, "you don't have permission to list tags"); e != nil {
				log.Printf("failed to send permission message: %v", e)
			}
			return
		}

		// Fetch raw parent and thread JSON like the main flow
		parentEndpoint := discordgo.EndpointChannel(ch.ParentID)
		parentRaw, err := s.RequestWithBucketID("GET", parentEndpoint, nil, parentEndpoint)
		if err != nil {
			parentChan, err2 := s.Channel(ch.ParentID)
			if err2 != nil {
				parentRaw = []byte("{}")
			} else {
				parentRaw, _ = json.Marshal(parentChan)
			}
		}
		threadEndpoint := discordgo.EndpointChannel(ch.ID)
		threadRaw, err := s.RequestWithBucketID("GET", threadEndpoint, nil, threadEndpoint)
		if err != nil {
			thread, _ := s.Channel(ch.ID)
			threadRaw, _ = json.Marshal(thread)
		}

		// Parse parent available tags
		var p struct {
			AvailableTags []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"available_tags"`
			ForumMetadata *struct {
				AvailableTags []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"available_tags"`
			} `json:"forum_metadata"`
		}
		_ = json.Unmarshal(parentRaw, &p)
		available := p.AvailableTags
		if len(available) == 0 && p.ForumMetadata != nil {
			available = p.ForumMetadata.AvailableTags
		}

		// Parse thread applied tags
		var t struct {
			AppliedTags []string `json:"applied_tags"`
		}
		_ = json.Unmarshal(threadRaw, &t)

		// Build reply
		sb := &strings.Builder{}
		sb.WriteString("Available tags:\n")
		for _, at := range available {
			sb.WriteString("- ")
			sb.WriteString(at.Name)
			sb.WriteString(" (id=")
			sb.WriteString(at.ID)
			sb.WriteString(")\n")
		}
		sb.WriteString("Applied tags on this thread:\n")
		for _, id := range t.AppliedTags {
			sb.WriteString("- ")
			sb.WriteString(id)
			sb.WriteString("\n")
		}
		if _, e := s.ChannelMessageSend(m.ChannelID, sb.String()); e != nil {
			log.Printf("failed to send tag list: %v", e)
		}
		return
	}
	if !has {
		// optionally notify
		if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("<@%s> you don't have permission to run that command.", m.Author.ID)); err != nil {
			log.Printf("failed to send permission message: %v", err)
		}
		return
	}

	// Debug: log channel identifiers to help diagnose access problems
	log.Printf("debug: message in channel=%s parent=%s guild=%s", ch.ID, ch.ParentID, ch.GuildID)

	// Fetch parent (forum) channel using discordgo to read available tags
	parent, err := s.Channel(ch.ParentID)
	if err != nil {
		log.Printf("failed to fetch parent channel: %v", err)
		return
	}

	// Find the tag ID from available forum tags. Some discordgo versions expose tags
	// at top-level as `available_tags`, whereas the API may return them under
	// `forum_metadata.available_tags`. We'll check both and log raw JSON when
	// nothing is found so we can diagnose mismatches.
	tagID := ""
	dotTagIDs := map[string]bool{}

	// Retrieve raw parent channel JSON via discordgo's internal REST client. Some
	// discordgo Channel structs do not include forum_metadata when marshaled,
	// so a direct GET to the channels endpoint returns the full API payload
	// (including forum_metadata.available_tags).
	var parentJSON []byte
	endpoint := discordgo.EndpointChannel(ch.ParentID)
	if raw, err := s.RequestWithBucketID("GET", endpoint, nil, endpoint); err != nil {
		log.Printf("warning: failed to GET parent channel via discordgo raw REST: %v; falling back to marshaled struct", err)
		parentJSON, _ = json.Marshal(parent)
	} else {
		parentJSON = raw
	}

	var parentData struct {
		AvailableTags []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"available_tags"`
		ForumMetadata *struct {
			AvailableTags []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"available_tags"`
		} `json:"forum_metadata"`
	}
	if err := json.Unmarshal(parentJSON, &parentData); err != nil {
		log.Printf("failed to parse parent channel tags: %v", err)
		return
	}

	// Prefer top-level available_tags, fallback to forum_metadata.available_tags
	available := parentData.AvailableTags
	if len(available) == 0 && parentData.ForumMetadata != nil {
		available = parentData.ForumMetadata.AvailableTags
	}

	if len(available) == 0 {
		// Log raw JSON to help diagnose the structure returned by discordgo
		log.Printf("debug: parent channel raw JSON: %s", string(parentJSON))
	}

	log.Printf("debug: found %d available tags in forum %s", len(available), ch.ParentID)
	for _, t := range available {
		log.Printf("debug: available tag: %q (id=%s)", t.Name, t.ID)
		if strings.HasPrefix(t.Name, ".") {
			dotTagIDs[t.ID] = true
		}
		// Case-insensitive tag name matching
		if strings.EqualFold(t.Name, cfg.TagName) {
			tagID = t.ID
		}
	}
	if tagID == "" {
		if _, e := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Tag %s not found in the forum. Please create it first.", cfg.TagName)); e != nil {
			log.Printf("failed to send tag missing message: %v", e)
		}
		log.Printf("debug: looking for tag %q but not found among available tags", cfg.TagName)
		return
	}
	log.Printf("debug: matched tag %q to id=%s", cfg.TagName, tagID)

	// fetch this thread channel via REST to read applied_tags reliably
	var threadJSON []byte
	threadEndpoint := discordgo.EndpointChannel(ch.ID)
	if raw, err := s.RequestWithBucketID("GET", threadEndpoint, nil, threadEndpoint); err != nil {
		log.Printf("warning: failed to GET thread channel via raw REST: %v; falling back to marshaled struct", err)
		thread, err2 := s.Channel(ch.ID)
		if err2 != nil {
			log.Printf("failed to fetch thread channel: %v", err2)
			return
		}
		threadJSON, _ = json.Marshal(thread)
	} else {
		threadJSON = raw
	}

	var chData struct {
		AppliedTags []string `json:"applied_tags"`
	}
	if err := json.Unmarshal(threadJSON, &chData); err != nil {
		log.Printf("failed to parse thread applied tags: %v", err)
		return
	}

	// compute new applied tags: remove other dot-tags, keep non-dot tags
	newApplied := make([]string, 0, len(chData.AppliedTags))
	for _, at := range chData.AppliedTags {
		if !dotTagIDs[at] {
			newApplied = append(newApplied, at)
		}
	}
	// add desired tag id if not already present
	already := false
	for _, a := range newApplied {
		if a == tagID {
			already = true
			break
		}
	}
	if !already {
		newApplied = append(newApplied, tagID)
	}

	// edit thread title (prefix if missing)
	newName := addPrefixIfMissing(ch.Name, cfg.Prefix)

	// Log before editing
	log.Printf("debug: editing thread name: old=%q new=%q", ch.Name, newName)
	log.Printf("debug: newApplied tag IDs: %v", newApplied)

	// Use discordgo's ChannelEdit properly with the correct struct
	edit := &discordgo.ChannelEdit{
		Name:        newName,
		AppliedTags: &newApplied,
	}

	// Wrap ChannelEdit in a timeout to prevent indefinite blocking
	type editResult struct {
		updated *discordgo.Channel
		err     error
	}
	resultChan := make(chan editResult, 1)

	go func() {
		log.Printf("debug: calling ChannelEdit...")
		updated, err := s.ChannelEdit(ch.ID, edit)
		if err != nil {
			// Check if it's a rate limit error to provide better logging
			if restErr, ok := err.(*discordgo.RESTError); ok {
				if restErr.Response != nil && restErr.Response.StatusCode == 429 {
					log.Printf("WARN: Hit rate limit on ChannelEdit for thread %s - discordgo will automatically retry", ch.ID)
				}
			}
		}
		resultChan <- editResult{updated: updated, err: err}
	}()

	var updated *discordgo.Channel

	select {
	case result := <-resultChan:
		updated = result.updated
		err = result.err
		log.Printf("debug: ChannelEdit returned")
	case <-time.After(15 * time.Second):
		log.Printf("ERROR: ChannelEdit timed out after 15 seconds")
		if _, e := s.ChannelMessageSend(m.ChannelID, "command timed out (Discord API not responding)"); e != nil {
			log.Printf("failed to send timeout message: %v", e)
		}
		return
	}

	if err != nil {
		log.Printf("ERROR: ChannelEdit failed: %v", err)
		if restErr, ok := err.(*discordgo.RESTError); ok {
			status := 0
			if restErr.Response != nil {
				status = restErr.Response.StatusCode
			}
			log.Printf("Discord API error: StatusCode=%d, Message=%q, ResponseBody=%s", status, restErr.Message, string(restErr.ResponseBody))

			// Provide user-friendly messages based on error type
			switch status {
			case 429:
				// Build a message including rate limit headers so moderators can see why the bot was throttled
				var sb strings.Builder
				sb.WriteString("⏱️ Discord rate limit reached. The bot is being throttled. Please wait a moment and try again.\n")
				if restErr.Response != nil && restErr.Response.Header != nil {
					h := restErr.Response.Header
					sb.WriteString("Rate limit headers:\n")
					sb.WriteString(fmt.Sprintf("- X-RateLimit-Limit: %s\n", h.Get("X-RateLimit-Limit")))
					sb.WriteString(fmt.Sprintf("- X-RateLimit-Remaining: %s\n", h.Get("X-RateLimit-Remaining")))
					sb.WriteString(fmt.Sprintf("- X-RateLimit-Reset: %s\n", h.Get("X-RateLimit-Reset")))
					sb.WriteString(fmt.Sprintf("- X-RateLimit-Reset-After: %s\n", h.Get("X-RateLimit-Reset-After")))
					sb.WriteString(fmt.Sprintf("- X-RateLimit-Global: %s\n", h.Get("X-RateLimit-Global")))
					sb.WriteString(fmt.Sprintf("- Retry-After: %s\n", h.Get("Retry-After")))
				} else {
					sb.WriteString("(no rate-limit headers available)\n")
				}
				if _, e := s.ChannelMessageSend(m.ChannelID, sb.String()); e != nil {
					log.Printf("failed to send rate limit message: %v", e)
				}
			case 403:
				if _, e := s.ChannelMessageSend(m.ChannelID, "❌ Permission denied. The bot lacks the required permissions (Manage Threads, Manage Messages)."); e != nil {
					log.Printf("failed to send permission error message: %v", e)
				}
			case 404:
				if _, e := s.ChannelMessageSend(m.ChannelID, "⚠️ Thread or forum not found. The post may have been deleted."); e != nil {
					log.Printf("failed to send not found message: %v", e)
				}
			case 500, 502, 503, 504:
				if _, e := s.ChannelMessageSend(m.ChannelID, "🔧 Discord API is experiencing issues. Please try again in a moment."); e != nil {
					log.Printf("failed to send server error message: %v", e)
				}
			default:
				if _, e := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Failed to update thread (Error %d). Check bot permissions or try again.", status)); e != nil {
					log.Printf("failed to send generic error message: %v", e)
				}
			}
			return
		}
		// Fallback for non-REST errors
		if _, e := s.ChannelMessageSend(m.ChannelID, "❌ Failed to update thread (unknown error). Please check logs or try again."); e != nil {
			log.Printf("failed to send fallback error message: %v", e)
		}
		return
	}
	log.Printf("debug: ChannelEdit succeeded: name=%q applied_tags=%v", updated.Name, updated.AppliedTags)

	// success reaction or message
	if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Updated thread: %s", newName)); err != nil {
		log.Printf("failed to send confirmation message: %v", err)
	}
}

func isThreadChannel(ch *discordgo.Channel) bool {
	// thread channel types: 11 public thread, 12 private thread, 15 announcement thread in some libs
	switch ch.Type {
	case discordgo.ChannelTypeGuildPublicThread, discordgo.ChannelTypeGuildPrivateThread:
		return true
	default:
		return false
	}
}

// addPrefixIfMissing adds prefix + space if the name doesn't already start with that prefix
func addPrefixIfMissing(name, prefix string) string {
	// Only remove our known status prefixes at the start (e.g., [Solved], [Duplicate], etc.)
	// This preserves user-added brackets like "[Help!] my issue"
	knownPrefixes := []string{
		"[Solved]", "[Devs aware]", "[Duplicate]",
		"[False report]", "[Known issue]", "[Wrong channel]",
	}

	stripped := strings.TrimSpace(name)
	// Remove any known prefixes (case-insensitive) at the start
	for {
		found := false
		for _, kp := range knownPrefixes {
			if strings.HasPrefix(strings.ToLower(stripped), strings.ToLower(kp)) {
				stripped = strings.TrimSpace(stripped[len(kp):])
				found = true
				break
			}
		}
		if !found {
			break
		}
	}

	// Now prepend the desired prefix
	return prefix + " " + stripped
}

// userCanManagePosts checks if a user has MANAGE_MESSAGES or MANAGE_CHANNELS (moderator-like)
func (h *handler) userCanManagePosts(s *discordgo.Session, userID string, ch *discordgo.Channel) (bool, error) {
	// fetch member permissions in this channel
	perms, err := s.UserChannelPermissions(userID, ch.ID)
	if err != nil {
		return false, err
	}
	// If the config defines allowed role IDs, check whether the member has one of those roles
	if h.cfg != nil && len(h.cfg.AllowedRoleIDs) > 0 {
		// fetch member to examine roles
		member, err := s.GuildMember(ch.GuildID, userID)
		if err != nil {
			return false, err
		}
		for _, r := range member.Roles {
			for _, allowed := range h.cfg.AllowedRoleIDs {
				if r == allowed {
					return true, nil
				}
			}
		}
		return false, nil
	}

	// If the config defines allowed permission names, map them to bits and require at least one
	if h.cfg != nil && len(h.cfg.AllowedPermissions) > 0 {
		for _, name := range h.cfg.AllowedPermissions {
			switch name {
			case "ADMINISTRATOR":
				if perms&discordgo.PermissionAdministrator != 0 {
					return true, nil
				}
			case "MANAGE_CHANNELS":
				if perms&discordgo.PermissionManageChannels != 0 {
					return true, nil
				}
			case "MANAGE_ROLES":
				if perms&discordgo.PermissionManageRoles != 0 {
					return true, nil
				}
			case "MANAGE_MESSAGES":
				if perms&discordgo.PermissionManageMessages != 0 {
					return true, nil
				}
			}
		}
		return false, nil
	}

	// default behaviour: require ManageRoles or ManageChannels or ManageMessages or Administrator
	const needed = discordgo.PermissionManageChannels | discordgo.PermissionManageRoles | discordgo.PermissionManageMessages | discordgo.PermissionAdministrator
	return (perms & needed) != 0, nil
}
