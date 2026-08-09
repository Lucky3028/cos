package tui

import (
	"github.com/Lucky3028/cos/internal/domain"
	"github.com/Lucky3028/cos/internal/lock"
)

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

var sanitizeTerminalText = domain.SanitizeTerminalText
var sanitizeSingleLine = domain.SanitizeSingleLine
var writerLockStatus = lock.WriterLockStatus
var isLocked = lock.IsLocked
var lockStatus = lock.LockStatus

const defaultThreadPageSize = domain.DefaultThreadPageSize
