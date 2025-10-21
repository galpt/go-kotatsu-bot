# Kotatsu Forum Tag Bot

A small Discord bot written in Go that automates marking Forum posts with a title prefix and the appropriate forum tag when moderators run a short command in a thread's discussion.

This bot was designed to streamline support workflows in Discord Forum channels: instead of manually editing the thread title and tags, moderators can type a short command (like `.solved`) and the bot will prefix the title and set the correct dot-prefixed tag.


## Features
- Add a prefixed marker to the thread title (for example `[Solved] ...`).
- Apply exactly one dot-tag (like `.Solved`) from the configured set while preserving other non-dot tags.
- Restrict commands to users with moderator-like permissions.
- Optionally restrict the bot to only watch specific Forum parent channel IDs.
- Configurable via `config.yaml` (YAML) or environment variables.

## Supported commands (type in a thread discussion):
- `.solved` — prefix: `[Solved]`, tag: `.Solved`
- `.aware` — prefix: `[Devs aware]`, tag: `.Devs aware`
- `.duplicate` — prefix: `[Duplicate]`, tag: `.Duplicate`
- `.false` — prefix: `[False report]`, tag: `.False report`
- `.known` — prefix: `[Known issue]`, tag: `.Known issue`
- `.wrong` — prefix: `[Wrong channel]`, tag: `.Wrong channel`

## Behavior and rules
- The bot only acts when the command is sent inside a thread (Forum discussion).
- If `forum_parent_ids` are set in the config, the bot ignores threads that are not children of those forum parents.
- The bot will remove any other dot-tags from the configured set and keep other non-dot tags intact.
- Only users with Manage Channels, Manage Roles, Manage Messages, or Administrator permission can trigger the commands. This can be changed in the source.

## Requirements & Permissions
- Go 1.20+
- Discord Bot Token with the following OAuth2 scopes and permissions when invited:
  - Bot scope
  - Manage Channels
  - Manage Threads
  - Read Messages / View Channel
  - Send Messages
  - Read Message History

## Gateway intents
- The bot uses the Message Content intent. Make sure the bot application has Message Content intent enabled in the Developer Portal.

## Installation
1. Build (from the `bot/` folder):

```powershell
go mod tidy
go build
```

2. Create `config.yaml` by copying `example_config.yaml` and filling fields: `discord_token` and `forum_parent_ids` (optional).

3. Run:

> [!NOTE]  
> Supply the raw token (without the leading "Bot ") when setting the environment variable or in the YAML.
```powershell
$env:DISCORD_TOKEN = "<your_token>" # optional, will override config file
# optional: override forum parents
$env:FORUM_PARENT_IDS = "12345,67890"
.
\bot.exe
```

## Configuration file (`config.yaml`)
```yaml
discord_token: "YOUR_BOT_TOKEN_HERE"
forum_parent_ids:
  - "123456789012345678"
```

## Troubleshooting
- If the bot does not respond to commands:
  - Ensure it has the required permissions and that the Message Content intent is enabled.
  - Check that the command is typed inside a thread of a Forum parent (or in a watched forum parent if configured).
  - Review the bot logs for permission or HTTP errors.

## Development notes
- The bot uses discordgo for gateway connections and direct REST calls for forum tag operations (reads `available_tags` and updates `applied_tags`).
- The `commandConfig` map in `commands.go` defines the available commands and their corresponding tag names. Edit this map to add/remove commands or change labels.

## License
- MIT (or your preferred license)
