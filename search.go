package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// trySearchInMessage inspects a non-command message and, if patterns match and config allows,
// queries AniList and responds with an embed. It returns nil when no action was taken.
func (h *handler) trySearchInMessage(s *discordgo.Session, m *discordgo.MessageCreate, ch *discordgo.Channel) error {
	if h.cfg == nil || h.cfg.SearchEnabled == nil || !*h.cfg.SearchEnabled {
		return nil
	}

	// Respect configured channel restrictions: if SearchChannels is non-empty, only operate there
	if len(h.cfg.SearchChannels) > 0 {
		allowed := false
		for _, id := range h.cfg.SearchChannels {
			if id == ch.ID || id == ch.ParentID {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil
		}
	}

	// Do not attempt to search on messages from bots
	if m.Author != nil && m.Author.Bot {
		return nil
	}

	// Define regexes inspired by the Python implementation
	animeRe := regexp.MustCompile("`[\\s\\S]*?`|\\{(.*?)\\}")
	mangaRe := regexp.MustCompile("<.*?https?:\\/\\/.*?>|<a?:.+?:\\d*>|`[\\s\\S]*?`|<(.*?)>")

	// We'll check both media types and prefer the first positive result
	// Allow adult content if the channel is marked NSFW (some types of channels)
	allowAdult := false
	if ch.NSFW {
		allowAdult = true
	}

	// Try anime
	if names := extractNamesFromRegex(animeRe, m.Content); len(names) > 0 {
		// debug
		log.Printf("search: anime regex matched names=%v in channel=%s (nsfw=%v)", names, ch.ID, ch.NSFW)
		// If multiple names, build a compact list response; otherwise send detailed embed
		if len(names) > 1 {
			var lines []string
			for _, n := range names {
				if media, err := searchAniList(n, "ANIME", allowAdult); err == nil && media != nil {
					lines = append(lines, fmt.Sprintf("[**%s**](%s)", media.Title, media.SiteURL))
				}
			}
			if len(lines) > 0 {
				emb := &discordgo.MessageEmbed{Description: strings.Join(lines, "\n"), Color: 0x2f3136}
				_, _ = s.ChannelMessageSendEmbed(m.ChannelID, emb)
			}
			return nil
		}
		// single
		media, err := searchAniList(names[0], "ANIME", allowAdult)
		if err != nil {
			log.Printf("search: AniList error for %q: %v", names[0], err)
		}
		if media == nil {
			log.Printf("search: no AniList results for %q (anime)", names[0])
		} else {
			emb := media.toEmbed()
			_, _ = s.ChannelMessageSendEmbed(m.ChannelID, emb)
		}
		return nil
	}

	// Try manga
	if names := extractNamesFromRegex(mangaRe, m.Content); len(names) > 0 {
		log.Printf("search: manga regex matched names=%v in channel=%s (nsfw=%v)", names, ch.ID, ch.NSFW)
		if len(names) > 1 {
			var lines []string
			for _, n := range names {
				if media, err := searchAniList(n, "MANGA", allowAdult); err == nil && media != nil {
					lines = append(lines, fmt.Sprintf("[**%s**](%s)", media.Title, media.SiteURL))
				}
			}
			if len(lines) > 0 {
				emb := &discordgo.MessageEmbed{Description: strings.Join(lines, "\n"), Color: 0x2f3136}
				_, _ = s.ChannelMessageSendEmbed(m.ChannelID, emb)
			}
			return nil
		}
		media, err := searchAniList(names[0], "MANGA", allowAdult)
		if err != nil {
			log.Printf("search: AniList error for %q: %v", names[0], err)
		}
		if media == nil {
			log.Printf("search: no AniList results for %q (manga)", names[0])
		} else {
			emb := media.toEmbed()
			_, _ = s.ChannelMessageSendEmbed(m.ChannelID, emb)
		}
		return nil
	}

	return nil
}

func extractNamesFromRegex(re *regexp.Regexp, content string) []string {
	matches := re.FindAllStringSubmatch(content, -1)
	var out []string
	for _, m := range matches {
		if len(m) >= 2 && strings.TrimSpace(m[1]) != "" {
			out = append(out, strings.TrimSpace(m[1]))
			continue
		}
		// fallback to whole-match without surrounding ticks/brackets
		full := strings.TrimSpace(m[0])
		// strip surrounding backticks, <>, or braces
		full = strings.Trim(full, "`<>{}")
		if full != "" {
			out = append(out, full)
		}
	}
	return out
}

// aniListMedia is a minimal structure for AniList media data used to build embeds
type aniListMedia struct {
	ID       int
	SiteURL  string
	Title    string
	Desc     string
	Genres   []string
	CoverURL string
	Format   string
	ColorHex string
	// optional timestamp
	StartDate string
}

func (m *aniListMedia) toEmbed() *discordgo.MessageEmbed {
	desc := m.Desc
	if len(desc) > 800 {
		desc = desc[:800] + "..."
	}
	embed := &discordgo.MessageEmbed{
		Title:       m.Title,
		Description: fmt.Sprintf("***%s***\n%s", strings.Join(m.Genres, ", "), desc),
		URL:         m.SiteURL,
		Color:       0x2f3136,
	}
	if m.CoverURL != "" {
		embed.Image = &discordgo.MessageEmbedImage{URL: m.CoverURL}
	}
	return embed
}

// searchAniList queries AniList GraphQL for the given name and media type ("ANIME"/"MANGA").
func searchAniList(name, mediaType string, allowAdult bool) (*aniListMedia, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("empty search")
	}
	// Use the Page -> media search form which returns a list; this matches AniList examples in 2025 docs.
	query := `query ($search: String!, $type: MediaType, $isAdult: Boolean = false) {
		Page(page: 1, perPage: 1) {
			media(search: $search, type: $type, isAdult: $isAdult) {
				id
				siteUrl
				title { romaji english native }
				description(asHtml: false)
				genres
				coverImage { large, color }
				format
				startDate { year month day }
			}
		}
	}`
	vars := map[string]interface{}{
		"search":  name,
		"type":    mediaType,
		"isAdult": allowAdult,
	}
	payload := map[string]interface{}{"query": query, "variables": vars}
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://graphql.anilist.co", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Read body for diagnostics
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("search: AniList response status=%d body=%s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("anilist returned status %d", resp.StatusCode)
	}

	var data struct {
		Data struct {
			Page struct {
				Media []struct {
					ID      int    `json:"id"`
					SiteURL string `json:"siteUrl"`
					Title   struct {
						Romaji  string `json:"romaji"`
						English string `json:"english"`
						Native  string `json:"native"`
					} `json:"title"`
					Description string   `json:"description"`
					Genres      []string `json:"genres"`
					CoverImage  struct {
						Large string `json:"large"`
						Color string `json:"color"`
					} `json:"coverImage"`
					Format    string `json:"format"`
					StartDate struct {
						Year  int `json:"year"`
						Month int `json:"month"`
						Day   int `json:"day"`
					} `json:"startDate"`
				} `json:"media"`
			} `json:"Page"`
		} `json:"data"`
	}
	// Decode from the bytes we already read
	if err := json.Unmarshal(respBody, &data); err != nil {
		log.Printf("search: failed to decode AniList JSON: %v; body=%s", err, string(respBody))
		return nil, err
	}
	if len(data.Data.Page.Media) == 0 {
		return nil, nil
	}
	m := &data.Data.Page.Media[0]
	title := m.Title.English
	if title == "" {
		title = m.Title.Romaji
	}
	if title == "" {
		title = m.Title.Native
	}
	// strip simple HTML from description
	desc := stripTags(m.Description)

	cover := m.CoverImage.Large
	color := m.CoverImage.Color
	startDate := ""
	if m.StartDate.Year != 0 {
		startDate = fmt.Sprintf("%04d-%02d-%02d", m.StartDate.Year, m.StartDate.Month, m.StartDate.Day)
	}
	return &aniListMedia{
		ID:        m.ID,
		SiteURL:   m.SiteURL,
		Title:     title,
		Desc:      desc,
		Genres:    m.Genres,
		CoverURL:  cover,
		Format:    m.Format,
		ColorHex:  color,
		StartDate: startDate,
	}, nil
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string {
	if s == "" {
		return s
	}
	// Remove simple HTML tags
	out := tagRe.ReplaceAllString(s, "")
	// Collapse whitespace
	out = strings.ReplaceAll(out, "\n\n", "\n")
	out = strings.TrimSpace(out)
	return out
}
