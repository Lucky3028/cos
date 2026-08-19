package appserver

import "github.com/Lucky3028/cos/internal/domain"

type ListScope = domain.ListScope
type ThreadListRequest = domain.ThreadListRequest
type ThreadPage = domain.ThreadPage
type SessionStore = domain.SessionStore
type Thread = domain.Thread
type Conversation = domain.Conversation
type ConversationItemKind = domain.ConversationItemKind
type ConversationItem = domain.ConversationItem

const (
	CurrentDirectory              = domain.CurrentDirectory
	AllThreads                    = domain.AllThreads
	ConversationItemKindUser      = domain.ConversationItemKindUser
	ConversationItemKindAssistant = domain.ConversationItemKindAssistant
	ConversationItemKindActivity  = domain.ConversationItemKindActivity
)

var sanitizeSingleLine = domain.SanitizeSingleLine
var sanitizeTerminalText = domain.SanitizeTerminalText

// Version is the cos version sent to app-server and shown by the CLI.
// GoReleaser overrides this value with the release tag version at build time.
var Version = "dev"

const (
	defaultThreadPageSize = domain.DefaultThreadPageSize
	maxSearchPages        = domain.MaxSearchPages
)
