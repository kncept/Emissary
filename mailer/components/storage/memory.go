package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/kncept/Emissary/mailer/email"
)

var _ Storage = (*MemoryStorage)(nil)

type memoryAccount struct {
	mailboxes map[string]Mailbox
	// emails keyed by stable message id within the account.
	emails map[string]*email.ListedEmail
	raw    map[string][]byte
}

// MemoryStorage is a small in-memory store. It is useful for local
// development and tests; messages are lost when the process exits.
type MemoryStorage struct {
	mu                sync.RWMutex
	accounts          map[string]*memoryAccount
	drafts            map[string]map[string][]byte
	sessionIDs        map[string]map[string]string
	jmapStates        map[string]jmapStateRecord
	jmapStateRevision uint64
}

// jmapStateRecord models the revisioned blob held by the in-memory store.
type jmapStateRecord struct {
	data     []byte
	revision string
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		accounts:   make(map[string]*memoryAccount),
		drafts:     make(map[string]map[string][]byte),
		sessionIDs: make(map[string]map[string]string),
		jmapStates: make(map[string]jmapStateRecord),
	}
}

func (s *MemoryStorage) Name() string {
	return "MemoryStorage"
}

func (s *MemoryStorage) accountLocked(accountID string) *memoryAccount {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return nil
	}
	acct, ok := s.accounts[accountID]
	if !ok {
		acct = &memoryAccount{
			mailboxes: make(map[string]Mailbox),
			emails:    make(map[string]*email.ListedEmail),
			raw:       make(map[string][]byte),
		}
		s.accounts[accountID] = acct
	}
	return acct
}

func (s *MemoryStorage) ensureDefaultMailboxesLocked(acct *memoryAccount) {
	for _, mb := range DefaultMailboxes() {
		if _, ok := acct.mailboxes[mb.ID]; !ok {
			copy := mb
			acct.mailboxes[mb.ID] = copy
		}
	}
}

func (s *MemoryStorage) ListMailboxes(_ context.Context, accountID string) ([]Mailbox, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return nil, errors.New("accountID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	mailboxes := make([]Mailbox, 0, len(acct.mailboxes))
	for _, mb := range acct.mailboxes {
		copy := mb
		mailboxes = append(mailboxes, copy)
	}
	SortMailboxes(mailboxes)
	return mailboxes, nil
}

func (s *MemoryStorage) GetMailbox(_ context.Context, accountID, mailboxID string) (Mailbox, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	mailboxID = NormalizeMailboxID(mailboxID)
	if accountID == "" || mailboxID == "" {
		return Mailbox{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	mb, ok := acct.mailboxes[mailboxID]
	if !ok {
		return Mailbox{}, ErrNotFound
	}
	return mb, nil
}

func (s *MemoryStorage) CreateMailbox(_ context.Context, accountID string, mailbox Mailbox) (Mailbox, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return Mailbox{}, errors.New("invalid mailbox")
	}
	mailbox, err := NormalizeMailbox(mailbox)
	if err != nil {
		return Mailbox{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	if _, ok := acct.mailboxes[mailbox.ID]; ok {
		return Mailbox{}, ErrMailboxExists
	}
	if err := ValidateMailboxParent(mailbox, func(parentID string) bool {
		_, ok := acct.mailboxes[parentID]
		return ok
	}); err != nil {
		return Mailbox{}, err
	}
	acct.mailboxes[mailbox.ID] = mailbox
	return mailbox, nil
}

func (s *MemoryStorage) UpdateMailbox(_ context.Context, accountID string, mailbox Mailbox) (Mailbox, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return Mailbox{}, errors.New("invalid mailbox")
	}
	mailbox, err := NormalizeMailbox(mailbox)
	if err != nil {
		return Mailbox{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	if _, ok := acct.mailboxes[mailbox.ID]; !ok {
		return Mailbox{}, ErrNotFound
	}
	if err := ValidateMailboxParent(mailbox, func(parentID string) bool {
		_, ok := acct.mailboxes[parentID]
		return ok
	}); err != nil {
		return Mailbox{}, err
	}
	acct.mailboxes[mailbox.ID] = mailbox
	return mailbox, nil
}

func (s *MemoryStorage) DeleteMailbox(_ context.Context, accountID, mailboxID string) error {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	mailboxID = NormalizeMailboxID(mailboxID)
	if accountID == "" || mailboxID == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	if _, ok := acct.mailboxes[mailboxID]; !ok {
		return ErrNotFound
	}
	for _, child := range acct.mailboxes {
		if child.ParentID != nil && *child.ParentID == mailboxID {
			return ErrMailboxNotEmpty
		}
	}
	for _, msg := range acct.emails {
		if msg != nil && msg.Ref != nil && strings.EqualFold(msg.Ref.MailboxID, mailboxID) {
			return ErrMailboxNotEmpty
		}
	}
	delete(acct.mailboxes, mailboxID)
	return nil
}

// AddEmail implements the Storage interface. The stable id is generated here
// so the caller can use it to create the message's metadata. When raw is
// non-nil it is retained so GetEmailRaw can return the original bytes.
func (s *MemoryStorage) AddEmail(_ context.Context, accountID, mailboxID string, message *email.UserEmail, raw []byte) (string, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	mailboxID = NormalizeMailboxID(mailboxID)
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	if accountID == "" {
		return "", errors.New("accountID is required")
	}
	if message == nil {
		return "", errors.New("invalid email")
	}
	token, err := newMessageToken()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	if _, ok := acct.mailboxes[mailboxID]; !ok {
		// Require destination mailbox to exist; default mailboxes are ensured.
		return "", ErrNotFound
	}
	if _, ok := acct.emails[token]; ok {
		return "", errors.New("email already exists")
	}
	acct.emails[token] = &email.ListedEmail{
		Ref:   &email.EmailRef{ID: token, AccountID: accountID, MailboxID: mailboxID},
		Email: cloneMessage(message),
	}
	if raw != nil {
		acct.raw[token] = append([]byte(nil), raw...)
	}
	return token, nil
}

// Add inserts a listed email. It is not part of Storage and exists for local
// development and tests, which supply their own ids and mailboxes.
func (s *MemoryStorage) Add(listed *email.ListedEmail) {
	if listed == nil || listed.Ref == nil || listed.Ref.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accountID := strings.ToLower(strings.TrimSpace(listed.Ref.AccountID))
	if accountID == "" {
		return
	}
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	mailboxID := NormalizeMailboxID(listed.Ref.MailboxID)
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	if _, ok := acct.mailboxes[mailboxID]; !ok {
		acct.mailboxes[mailboxID] = Mailbox{ID: mailboxID, Name: mailboxID, IsSubscribed: true}
	}
	id := listed.Ref.ID
	// Accept legacy full keys by storing under their stable token when derivable.
	if token, ok := email.DeliveredMessageToken(id); ok {
		id = token
	}
	copied := clone(listed)
	copied.Ref.ID = id
	copied.Ref.AccountID = accountID
	copied.Ref.MailboxID = mailboxID
	acct.emails[id] = copied
}

// ListEmails implements the Storage interface.
func (s *MemoryStorage) ListEmails(_ context.Context, accountID, mailboxID string) ([]*email.ListedEmail, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return nil, fmt.Errorf("accountID is required")
	}
	mailboxID = NormalizeMailboxID(mailboxID)

	s.mu.Lock()
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	s.mu.Unlock()

	s.mu.RLock()
	defer s.mu.RUnlock()
	acct = s.accounts[accountID]
	if acct == nil {
		return []*email.ListedEmail{}, nil
	}
	emails := make([]*email.ListedEmail, 0, len(acct.emails))
	for _, e := range acct.emails {
		if mailboxID != "" && !strings.EqualFold(e.Ref.MailboxID, mailboxID) {
			continue
		}
		emails = append(emails, clone(e))
	}
	SortListedEmails(emails)
	return emails, nil
}

func (s *MemoryStorage) resolveEmailLocked(accountID, id string) (*memoryAccount, string, *email.ListedEmail, bool) {
	candidates, err := emailIDCandidates(accountID, id, nil, nil)
	if err != nil {
		return nil, "", nil, false
	}
	acct := s.accounts[candidates.AccountID]
	if acct == nil {
		return nil, "", nil, false
	}
	// Accept a stable token or the token from a legacy full delivered key.
	for _, token := range candidates.Tokens {
		if e, ok := acct.emails[token]; ok {
			return acct, token, e, true
		}
	}
	return nil, "", nil, false
}

func (s *MemoryStorage) GetEmail(_ context.Context, accountID, id string) (*email.ListedEmail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, _, e, ok := s.resolveEmailLocked(accountID, id)
	if !ok {
		return nil, ErrNotFound
	}
	return clone(e), nil
}

func (s *MemoryStorage) GetEmailRaw(_ context.Context, accountID, id string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	acct, resolvedID, e, ok := s.resolveEmailLocked(accountID, id)
	if !ok {
		return nil, ErrNotFound
	}
	if raw, ok := acct.raw[resolvedID]; ok {
		return append([]byte(nil), raw...), nil
	}
	return email.RawFromUserEmail(e.Email), nil
}

func (s *MemoryStorage) MoveEmail(_ context.Context, accountID, id, destMailboxID string) (*email.ListedEmail, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	destMailboxID = NormalizeMailboxID(destMailboxID)
	if destMailboxID == "" {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	acct := s.accountLocked(accountID)
	s.ensureDefaultMailboxesLocked(acct)
	if _, ok := acct.mailboxes[destMailboxID]; !ok {
		return nil, ErrNotFound
	}
	_, _, e, ok := s.resolveEmailLocked(accountID, id)
	if !ok {
		return nil, ErrNotFound
	}
	e.Ref.MailboxID = destMailboxID
	return clone(e), nil
}

func (s *MemoryStorage) DeleteEmail(_ context.Context, accountID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	acct, resolvedID, _, ok := s.resolveEmailLocked(accountID, id)
	if !ok {
		return ErrNotFound
	}
	delete(acct.emails, resolvedID)
	delete(acct.raw, resolvedID)
	return nil
}

// PutDraft implements DraftStorage.
func (s *MemoryStorage) PutDraft(_ context.Context, sessionID, creationID string, message *email.UserEmail) (string, error) {
	id, err := draftKey(creationID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal draft: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts[sessionID] == nil {
		s.drafts[sessionID] = make(map[string][]byte)
	}
	s.drafts[sessionID][id] = body
	return id, nil
}

// GetDraft implements DraftStorage.
func (s *MemoryStorage) GetDraft(_ context.Context, sessionID, id string) (*email.UserEmail, error) {
	key, err := sessionObjectKey(sessionID, id)
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(key, sessionRoot+"/"+sessionID+"/")

	s.mu.RLock()
	defer s.mu.RUnlock()
	body, ok := s.drafts[sessionID][relative]
	if !ok {
		return nil, ErrNotFound
	}
	var message email.UserEmail
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	return &message, nil
}

// DeleteDraft implements DraftStorage. It is idempotent.
func (s *MemoryStorage) DeleteDraft(_ context.Context, sessionID, id string) error {
	key, err := sessionObjectKey(sessionID, id)
	if err != nil {
		return err
	}
	relative := strings.TrimPrefix(key, sessionRoot+"/"+sessionID+"/")

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.drafts[sessionID], relative)
	return nil
}

// LoadCreatedIDs implements SessionStateStore.
func (s *MemoryStorage) LoadCreatedIDs(_ context.Context, sessionID string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make(map[string]string, len(s.sessionIDs[sessionID]))
	for id, real := range s.sessionIDs[sessionID] {
		ids[id] = real
	}
	return ids, nil
}

// SaveCreatedIDs implements SessionStateStore.
func (s *MemoryStorage) SaveCreatedIDs(_ context.Context, sessionID string, ids map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := make(map[string]string, len(ids))
	for id, real := range ids {
		copy[id] = real
	}
	s.sessionIDs[sessionID] = copy
	return nil
}

// LoadJMAPState implements JMAPStateStore.
func (s *MemoryStorage) LoadJMAPState(_ context.Context, accountID string) ([]byte, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.jmapStates[accountID]
	if !ok {
		return nil, "", nil
	}
	return append([]byte(nil), record.data...), record.revision, nil
}

// SaveJMAPState implements JMAPStateStore with a compare-and-swap on revision.
func (s *MemoryStorage) SaveJMAPState(_ context.Context, accountID string, data []byte, revision string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.jmapStates[accountID]
	currentRevision := ""
	if exists {
		currentRevision = current.revision
	}
	if currentRevision != revision {
		return "", ErrJMAPStateConflict
	}
	s.jmapStateRevision++
	newRevision := fmt.Sprintf("r%d", s.jmapStateRevision)
	s.jmapStates[accountID] = jmapStateRecord{
		data:     append([]byte(nil), data...),
		revision: newRevision,
	}
	return newRevision, nil
}

func clone(listed *email.ListedEmail) *email.ListedEmail {
	copied := &email.ListedEmail{
		Ref:   nil,
		Email: cloneMessage(listed.Email),
	}
	if listed.Ref != nil {
		ref := *listed.Ref
		copied.Ref = &ref
	}
	return copied
}

func cloneMessage(e *email.UserEmail) *email.UserEmail {
	if e == nil {
		return nil
	}
	copied := *e
	copied.ReplyTo = append([]email.Address(nil), e.ReplyTo...)
	copied.To = append([]email.Address(nil), e.To...)
	copied.Cc = append([]email.Address(nil), e.Cc...)
	copied.Bcc = append([]email.Address(nil), e.Bcc...)
	return &copied
}
