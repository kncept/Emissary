package storage

import (
	"errors"
	"strings"
)

// mailboxRecordName is the single record filename used by the filesystem and
// S3 mailbox implementations.
const mailboxRecordName = "mailbox.json"

// NormalizeMailbox canonicalizes the fields that every storage backend stores
// the same way. It returns "invalid mailbox" for a missing id or name so
// callers do not each repeat that validation.
func NormalizeMailbox(m Mailbox) (Mailbox, error) {
	m.ID = NormalizeMailboxID(m.ID)
	m.Name = strings.TrimSpace(m.Name)
	if m.ParentID != nil {
		parentID := NormalizeMailboxID(*m.ParentID)
		m.ParentID = &parentID
	}
	if m.ID == "" || m.Name == "" {
		return Mailbox{}, errors.New("invalid mailbox")
	}
	return m, nil
}

// ValidateMailboxParent enforces the parent invariant shared by all backends:
// a non-empty parent must not be the mailbox itself and must already exist.
// exists is supplied by the backend so this check stays pure.
func ValidateMailboxParent(m Mailbox, exists func(string) bool) error {
	if m.ParentID == nil || *m.ParentID == "" {
		return nil
	}
	if *m.ParentID == m.ID {
		return errors.New("mailbox cannot be its own parent")
	}
	if !exists(*m.ParentID) {
		return ErrNotFound
	}
	return nil
}
