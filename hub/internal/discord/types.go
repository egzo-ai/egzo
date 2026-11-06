// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package discord is a small Discord client: what egzo-discord needs and nothing else. It speaks the gateway (a WebSocket that resumes
// after a drop) and the REST API (with Discord's rate limits) of API v10, from Discord's documentation. Every type holds only the fields
// the bridge reads or writes: what Discord adds is ignored, and components go through as JSON the bridge builds and checks itself.
package discord

import "encoding/json"

// APIVersion is the version of Discord's API this client speaks.
const APIVersion = 10

// Snowflake is a Discord id. Discord sends them as strings.
type Snowflake string

// Intents a bot asks for when it identifies.
const (
	IntentGuilds         = 1 << 0
	IntentGuildMessages  = 1 << 9
	IntentMessageContent = 1 << 15 // privileged: enabled in the Developer Portal
)

// User is a Discord account.
type User struct {
	ID       Snowflake `json:"id"`
	Username string    `json:"username"`
	Bot      bool      `json:"bot,omitempty"`
}

// Member is a user in a guild.
type Member struct {
	User        *User       `json:"user,omitempty"`
	Nick        string      `json:"nick,omitempty"`
	Roles       []Snowflake `json:"roles"`
	Permissions string      `json:"permissions,omitempty"` // in interactions: what the member may do in the channel
}

// Message is a message in a channel.
type Message struct {
	ID               Snowflake         `json:"id"`
	ChannelID        Snowflake         `json:"channel_id"`
	GuildID          Snowflake         `json:"guild_id,omitempty"`
	Author           User              `json:"author"`
	Member           *Member           `json:"member,omitempty"`
	Content          string            `json:"content"`
	MentionRoles     []Snowflake       `json:"mention_roles"`
	MessageReference *MessageReference `json:"message_reference,omitempty"`
	Flags            int               `json:"flags,omitempty"`
}

// MessageReference is what a reply points at.
type MessageReference struct {
	MessageID Snowflake `json:"message_id,omitempty"`
	ChannelID Snowflake `json:"channel_id,omitempty"`
	GuildID   Snowflake `json:"guild_id,omitempty"`
	// FailIfNotExists false posts a reply as a plain message when what it answers was deleted.
	FailIfNotExists *bool `json:"fail_if_not_exists,omitempty"`
}

// Channel types the bridge cares about.
const (
	ChannelText          = 0
	ChannelPublicThread  = 11
	ChannelPrivateThread = 12
)

// ChannelObfuscated is set on a channel the bot cannot view (2026-08-12): its name is hidden and it cannot be used.
const ChannelObfuscated = 1 << 17

// Channel is a channel or a thread.
type Channel struct {
	ID       Snowflake `json:"id"`
	Type     int       `json:"type"`
	GuildID  Snowflake `json:"guild_id,omitempty"`
	Name     string    `json:"name,omitempty"`
	ParentID Snowflake `json:"parent_id,omitempty"` // a thread's channel
	Flags    int       `json:"flags,omitempty"`
}

// Obfuscated reports whether the bot can no longer view the channel.
func (c Channel) Obfuscated() bool { return c.Flags&ChannelObfuscated != 0 }

// Role is a guild's role. The bridge makes one per agent.
type Role struct {
	ID          Snowflake `json:"id"`
	Name        string    `json:"name"`
	Managed     bool      `json:"managed,omitempty"`
	Mentionable bool      `json:"mentionable,omitempty"`
}

// Guild is what GUILD_CREATE says of a guild the bot is in.
type Guild struct {
	ID          Snowflake `json:"id"`
	Name        string    `json:"name"`
	Unavailable bool      `json:"unavailable,omitempty"`
	Channels    []Channel `json:"channels"`
	Threads     []Channel `json:"threads"`
	Roles       []Role    `json:"roles"`
}

// Interaction types.
const (
	InteractionPing         = 1
	InteractionCommand      = 2
	InteractionComponent    = 3
	InteractionAutocomplete = 4
	InteractionModalSubmit  = 5
)

// Interaction is a slash command, a click, an autocomplete or a form submitted. It must be answered within 3 seconds.
type Interaction struct {
	ID            Snowflake       `json:"id"`
	ApplicationID Snowflake       `json:"application_id"`
	Type          int             `json:"type"`
	Token         string          `json:"token"`
	GuildID       Snowflake       `json:"guild_id,omitempty"`
	ChannelID     Snowflake       `json:"channel_id,omitempty"`
	Member        *Member         `json:"member,omitempty"`
	Message       *Message        `json:"message,omitempty"` // the message a component is on
	Data          InteractionData `json:"data"`
}

// InteractionData is what the interaction carries: the command and its options, or the component and its values.
type InteractionData struct {
	Name          string          `json:"name,omitempty"`
	Options       []CommandOption `json:"options,omitempty"`
	CustomID      string          `json:"custom_id,omitempty"`
	ComponentType int             `json:"component_type,omitempty"`
	Values        []string        `json:"values,omitempty"`
	Components    json.RawMessage `json:"components,omitempty"` // a submitted form's fields
}

// CommandOption is an option of a command as a user gave it: a value, or a subcommand with options of its own.
type CommandOption struct {
	Name    string          `json:"name"`
	Type    int             `json:"type"`
	Value   json.RawMessage `json:"value,omitempty"`
	Focused bool            `json:"focused,omitempty"` // autocomplete: the option being typed
	Options []CommandOption `json:"options,omitempty"`
}

// AllowedMentions says whom a message may ping. The bridge sends one on every message: what an agent writes pings nobody.
type AllowedMentions struct {
	Parse       []string    `json:"parse"`
	Users       []Snowflake `json:"users,omitempty"`
	RepliedUser bool        `json:"replied_user,omitempty"`
}

// MessageFlagComponentsV2 makes a message's components its whole content (Components V2): content and embeds cannot be used with it.
const MessageFlagComponentsV2 = 1 << 15

// MessageCreate is a message to send or an edit. Components are the bridge's JSON: Discord checks them, and so does the bridge first.
type MessageCreate struct {
	Content          string            `json:"content,omitempty"`
	Components       json.RawMessage   `json:"components,omitempty"`
	Flags            int               `json:"flags,omitempty"`
	AllowedMentions  *AllowedMentions  `json:"allowed_mentions"`
	MessageReference *MessageReference `json:"message_reference,omitempty"`
	Attachments      []Attachment      `json:"attachments,omitempty"`
	Files            []File            `json:"-"` // sent as multipart parts files[n]
}

// Attachment describes a file sent with a message.
type Attachment struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
}

// File is a file uploaded with a message, such as a text too long to post.
type File struct {
	Name string
	Data []byte
}

// Interaction response types.
const (
	ResponseMessage         = 4
	ResponseDeferredMessage = 5
	ResponseDeferredUpdate  = 6
	ResponseUpdateMessage   = 7
	ResponseAutocomplete    = 8
	ResponseModal           = 9
)

// InteractionResponse answers an interaction.
type InteractionResponse struct {
	Type int `json:"type"`
	Data any `json:"data,omitempty"`
}

// Ephemeral is a message only the person who asked sees.
const Ephemeral = 1 << 6

// Choice is an autocomplete answer.
type Choice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Command is an application command the bridge registers.
type Command struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Type        int             `json:"type,omitempty"`
	Options     json.RawMessage `json:"options,omitempty"`
	Contexts    []int           `json:"contexts,omitempty"` // 0: in a guild only
}

// Permissions a bridge looks for in an interaction's member.
const (
	PermissionAdministrator = 1 << 3
	PermissionManageRoles   = 1 << 28
)

// Has reports whether a member's permissions, as an interaction carries them, include perm. An administrator has them all.
func (m *Member) Has(perm uint64) bool {
	if m == nil {
		return false
	}
	var p uint64
	for _, r := range m.Permissions {
		if r < '0' || r > '9' {
			return false
		}
		p = p*10 + uint64(r-'0')
	}
	return p&PermissionAdministrator != 0 || p&perm == perm
}

// Event is one event of the gateway, as it came: its name, its number and its data.
type Event struct {
	Type string
	Seq  int64
	Data json.RawMessage
}
