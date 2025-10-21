package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

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
	if content == "" || !strings.HasPrefix(content, ".") {
		return
	}

	// parse command token (first word)
	token := strings.Fields(content)[0]
	cmd := strings.TrimPrefix(strings.ToLower(token), ".")

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
	if !has {
		// optionally notify
		if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("<@%s> you don't have permission to run that command.", m.Author.ID)); err != nil {
			log.Printf("failed to send permission message: %v", err)
		}
		return
	}

	// Fetch parent (forum) channel raw JSON to resolve tag IDs and applied tags
	parentRaw, err := h.getChannelRaw(ch.ParentID)
	if err != nil {
		log.Printf("failed to fetch parent channel raw: %v", err)
		return
	}

	// parse available_tags
	var parentData struct {
		AvailableTags []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"available_tags"`
	}
	if err := json.Unmarshal(parentRaw, &parentData); err != nil {
		log.Printf("failed to unmarshal parent data: %v", err)
		return
	}

	tagID := ""
	dotTagIDs := map[string]bool{}
	for _, t := range parentData.AvailableTags {
		if strings.HasPrefix(t.Name, ".") {
			dotTagIDs[t.ID] = true
		}
		if t.Name == cfg.TagName {
			tagID = t.ID
		}
	}
	if tagID == "" {
		if _, e := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Tag %s not found in the forum. Please create it first.", cfg.TagName)); e != nil {
			log.Printf("failed to send tag missing message: %v", e)
		}
		return
	}

	// fetch this thread raw to read applied_tags
	chRaw, err := h.getChannelRaw(ch.ID)
	if err != nil {
		log.Printf("failed to fetch channel raw: %v", err)
		return
	}
	var chData struct {
		AppliedTags []string `json:"applied_tags"`
	}
	if err := json.Unmarshal(chRaw, &chData); err != nil {
		log.Printf("failed to unmarshal channel data: %v", err)
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

	// perform edits: ChannelEditComplex supports Name and AppliedTags
	edit := &discordgo.ChannelEdit{
		Name: newName,
	}
	// Note: discordgo's ChannelEdit does not include AppliedTags field in older versions;
	// use ChannelEditComplex via REST if necessary. We'll attempt with ChannelEditComplex when available.

	// First try to edit name (discordgo ChannelEdit accepts id and new name)
	if _, err := s.ChannelEdit(ch.ID, edit.Name); err != nil {
		log.Printf("failed to edit channel name: %v", err)
		if _, e := s.ChannelMessageSend(m.ChannelID, "failed to edit thread title (missing permission?)"); e != nil {
			log.Printf("failed to send error message: %v", e)
		}
		return
	}

	// Update applied tags by calling the channel patch endpoint directly
	if err := h.patchChannelAppliedTags(ch.ID, newApplied); err != nil {
		// Some servers may not allow editing applied tags; log and notify
		log.Printf("failed to edit applied tags: %v", err)
		if _, e := s.ChannelMessageSend(m.ChannelID, "failed to update applied tags (missing permission or API error)"); e != nil {
			log.Printf("failed to send error message: %v", e)
		}
		return
	}

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
	if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
		return name
	}
	return strings.TrimSpace(prefix + " " + name)
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

// getChannelRaw fetches the raw JSON for a channel via the REST API
func (h *handler) getChannelRaw(channelID string) ([]byte, error) {
	url := "https://discord.com/api/v10" + discordgo.EndpointChannels + channelID
	// Use the session's request helpers: make a standard HTTP GET with Authorization
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+h.token)
	req.Header.Set("User-Agent", "DiscordBot (kotatsu, 0.1)")
	cli := &http.Client{}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// patchChannelAppliedTags patches the channel's applied_tags via PATCH /channels/{id}
func (h *handler) patchChannelAppliedTags(channelID string, applied []string) error {
	url := "https://discord.com/api/v10" + discordgo.EndpointChannels + channelID
	body := map[string]interface{}{
		"applied_tags": applied,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PATCH", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+h.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DiscordBot (kotatsu, 0.1)")
	cli := &http.Client{}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}
