package emailsubmission

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const submissionSaveRetries = 8

// submissionID derives a stable id from the client's creation id. A retried
// /set with the same createdIds map therefore resolves to the same submission
// instead of sending the message a second time.
func submissionID(accountID, creationID, emailID string) string {
	sum := sha256.Sum256([]byte(accountID + "\x00" + creationID + "\x00" + emailID))
	return "S" + base64.RawURLEncoding.EncodeToString(sum[:12])
}

// jmapSubmission is the RFC 8621 EmailSubmission object. Every server-set
// property is present, with empty/null values where the minimal profile has no
// DSN/MDN or undo support.
type jmapSubmission struct {
	ID             string                                `json:"id"`
	IdentityID     string                                `json:"identityId"`
	EmailID        string                                `json:"emailId"`
	ThreadID       string                                `json:"threadId"`
	Envelope       storage.SubmissionEnvelope            `json:"envelope"`
	SendAt         string                                `json:"sendAt"`
	UndoStatus     string                                `json:"undoStatus"`
	DeliveryStatus map[string]storage.SubmissionDelivery `json:"deliveryStatus"`
	DSNBlobIDs     []string                              `json:"dsnBlobIds"`
	MDNBlobIDs     []string                              `json:"mdnBlobIds"`
}

func toJMAPSubmission(record storage.SubmissionRecord) jmapSubmission {
	rcptTo := append([]storage.SubmissionAddress(nil), record.Envelope.RcptTo...)
	if rcptTo == nil {
		rcptTo = []storage.SubmissionAddress{}
	}
	dsn := append([]string(nil), record.DSNBlobIDs...)
	if dsn == nil {
		dsn = []string{}
	}
	mdn := append([]string(nil), record.MDNBlobIDs...)
	if mdn == nil {
		mdn = []string{}
	}
	return jmapSubmission{
		ID:             record.ID,
		IdentityID:     record.IdentityID,
		EmailID:        record.EmailID,
		ThreadID:       record.ThreadID,
		Envelope:       storage.SubmissionEnvelope{MailFrom: record.Envelope.MailFrom, RcptTo: rcptTo},
		SendAt:         record.SendAt,
		UndoStatus:     record.UndoStatus,
		DeliveryStatus: record.DeliveryStatus,
		DSNBlobIDs:     dsn,
		MDNBlobIDs:     mdn,
	}
}

// mutateSubmissions applies fn to the account's durable collection under CAS.
// Concurrent Lambda invocations both keep their changes instead of the later
// writer overwriting the earlier one.
func (h *Handler) mutateSubmissions(ctx context.Context, accountID string, fn func(map[string]storage.SubmissionRecord) error) error {
	var lastErr error
	for attempt := 0; attempt < submissionSaveRetries; attempt++ {
		records, revision, err := h.deps.Submissions.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if err := fn(records); err != nil {
			return err
		}
		_, err = h.deps.Submissions.Save(ctx, accountID, records, revision)
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrSubmissionConflict) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("save submissions for %q after %d retries: %w", accountID, submissionSaveRetries, lastErr)
}

// submissionFilter is an RFC 8621 section 7.3 FilterCondition. Compound
// conditions are rejected before decoding so they cannot become a match-all
// filter.
type submissionFilter struct {
	IdentityIDs []string `json:"identityIds"`
	EmailIDs    []string `json:"emailIds"`
	ThreadIDs   []string `json:"threadIds"`
	UndoStatus  string   `json:"undoStatus"`
	Before      *string  `json:"before"`
	After       *string  `json:"after"`
	beforeTime  *time.Time
	afterTime   *time.Time
}

var submissionFilterFields = map[string]bool{
	"identityIds": true,
	"emailIds":    true,
	"threadIds":   true,
	"undoStatus":  true,
	"before":      true,
	"after":       true,
}

var submissionSortFields = map[string]bool{
	"emailId":  true,
	"threadId": true,
	"sentAt":   true,
}

func parseSubmissionFilter(args json.RawMessage) (*submissionFilter, error) {
	if err := base.ValidateConditionObject(args, "filter", submissionFilterFields); err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(args, &envelope); err != nil {
		return nil, base.Errorf("invalidArguments", "arguments must be an object")
	}
	raw, ok := envelope["filter"]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var filter submissionFilter
	if err := json.Unmarshal(raw, &filter); err != nil {
		return nil, base.Errorf("invalidArguments", "filter must be a condition object")
	}
	if filter.UndoStatus != "" && filter.UndoStatus != "pending" && filter.UndoStatus != "final" && filter.UndoStatus != "canceled" {
		return nil, base.Errorf("invalidArguments", "invalid undoStatus %q", filter.UndoStatus)
	}
	for name, value := range map[string]*string{"before": filter.Before, "after": filter.After} {
		if value == nil {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, *value)
		if err != nil {
			return nil, base.Errorf("invalidArguments", "invalid %s date %q: %v", name, *value, err)
		}
		if name == "before" {
			filter.beforeTime = &parsed
		} else {
			filter.afterTime = &parsed
		}
	}
	return &filter, nil
}

func containsID(values []string, id string) bool {
	for _, value := range values {
		if value == id {
			return true
		}
	}
	return false
}

func (f *submissionFilter) matches(record storage.SubmissionRecord) bool {
	if f == nil {
		return true
	}
	if len(f.IdentityIDs) > 0 && !containsID(f.IdentityIDs, record.IdentityID) {
		return false
	}
	if len(f.EmailIDs) > 0 && !containsID(f.EmailIDs, record.EmailID) {
		return false
	}
	if len(f.ThreadIDs) > 0 && !containsID(f.ThreadIDs, record.ThreadID) {
		return false
	}
	if f.UndoStatus != "" && f.UndoStatus != record.UndoStatus {
		return false
	}
	if f.beforeTime != nil || f.afterTime != nil {
		sentAt, err := time.Parse(time.RFC3339, record.SendAt)
		if err != nil {
			return false
		}
		if f.beforeTime != nil && !sentAt.Before(*f.beforeTime) {
			return false
		}
		if f.afterTime != nil && sentAt.Before(*f.afterTime) {
			return false
		}
	}
	return true
}

// submissionSortProperty validates the comparator using the shared helper and
// then returns the property name, so query state can be scoped to the sort.
func submissionSortProperty(sort []interface{}) (string, bool, error) {
	if _, err := base.SortAscending(sort, submissionSortFields); err != nil {
		return "", false, err
	}
	if len(sort) == 0 {
		return "", false, nil
	}
	comparator, _ := sort[0].(map[string]interface{})
	property, _ := comparator["property"].(string)
	ascending := true
	if value, present := comparator["isAscending"]; present {
		ascending, _ = value.(bool)
	}
	return property, ascending, nil
}

func sortedSubmissionIDs(records map[string]storage.SubmissionRecord, filter *submissionFilter, property string, ascending bool) []string {
	ids := make([]string, 0, len(records))
	for id, record := range records {
		if filter.matches(record) {
			ids = append(ids, id)
		}
	}
	if property == "" {
		property = "sentAt"
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := records[ids[i]], records[ids[j]]
		var comparison int
		switch property {
		case "emailId":
			comparison = strings.Compare(left.EmailID, right.EmailID)
		case "threadId":
			comparison = strings.Compare(left.ThreadID, right.ThreadID)
		default:
			comparison = strings.Compare(left.SendAt, right.SendAt)
		}
		if comparison == 0 {
			return ids[i] < ids[j]
		}
		if ascending {
			return comparison < 0
		}
		return comparison > 0
	})
	return ids
}
