package emailsubmission

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// submissionAddress is the RFC 8621 section 7 Envelope Address object.
type submissionAddress struct {
	Email      string             `json:"email"`
	Parameters map[string]*string `json:"parameters,omitempty"`
}

// submissionEnvelope is the RFC 8621 section 7 Envelope object.
type submissionEnvelope struct {
	MailFrom submissionAddress   `json:"mailFrom"`
	RcptTo   []submissionAddress `json:"rcptTo"`
}

// submissionCreate is the subset of a create object used by the sender and the
// durable record. Property validation happens on the raw JSON before decoding.
type submissionCreate struct {
	IdentityID string              `json:"identityId"`
	EmailID    string              `json:"emailId"`
	Envelope   *submissionEnvelope `json:"envelope"`
	SendAt     *string             `json:"sendAt"`
}

// envelopeRecipient pairs a recipient address with its optional ESMTP
// parameters. Headers never carry parameters; only a client-supplied envelope
// can supply them.
type envelopeRecipient struct {
	Address    mailemail.Address
	Parameters map[string]*string
}

// validateEnvelopeObject checks the RFC 8621 Envelope shape before it is
// decoded into a typed value. It returns the sorted property paths that make
// the create invalid, so the caller can report them in an invalidProperties
// SetError instead of silently ignoring unknown fields.
func validateEnvelopeObject(raw json.RawMessage) []string {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return []string{"envelope"}
	}
	invalid := make(map[string]bool)
	for name := range envelope {
		if name != "mailFrom" && name != "rcptTo" {
			invalid["envelope."+name] = true
		}
	}
	for _, name := range []string{"mailFrom", "rcptTo"} {
		value, ok := envelope[name]
		if !ok {
			invalid["envelope."+name] = true
			continue
		}
		if name == "mailFrom" {
			// RFC 8621 7 permits (but does not require) an empty MAIL FROM.
			// This profile accepts it as the null reverse path.
			if names := validateAddressObject(value, "envelope.mailFrom", true); len(names) > 0 {
				for _, n := range names {
					invalid[n] = true
				}
			}
			continue
		}
		var recipients []json.RawMessage
		if err := json.Unmarshal(value, &recipients); err != nil {
			invalid["envelope.rcptTo"] = true
			continue
		}
		for index, recipient := range recipients {
			if names := validateAddressObject(recipient, "envelope.rcptTo", false); len(names) > 0 {
				for _, n := range names {
					invalid[strings.Replace(n, "envelope.rcptTo", "envelope.rcptTo["+strconv.Itoa(index)+"]", 1)] = true
				}
			}
		}
	}
	properties := make([]string, 0, len(invalid))
	for name := range invalid {
		properties = append(properties, name)
	}
	sort.Strings(properties)
	return properties
}

func validateAddressObject(raw json.RawMessage, prefix string, allowEmptyEmail bool) []string {
	var address map[string]json.RawMessage
	if err := json.Unmarshal(raw, &address); err != nil {
		return []string{prefix}
	}
	invalid := make([]string, 0, 2)
	for name := range address {
		if name != "email" && name != "parameters" {
			invalid = append(invalid, prefix+"."+name)
		}
	}
	rawEmail, ok := address["email"]
	if !ok {
		invalid = append(invalid, prefix+".email")
	} else {
		var email string
		if err := json.Unmarshal(rawEmail, &email); err != nil ||
			(!allowEmptyEmail && strings.TrimSpace(email) == "") {
			invalid = append(invalid, prefix+".email")
		}
	}
	if rawParameters, ok := address["parameters"]; ok && string(rawParameters) != "null" {
		var parameters map[string]*string
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			invalid = append(invalid, prefix+".parameters")
		}
	}
	sort.Strings(invalid)
	return invalid
}

// addressKey compares the local part case-sensitively and only the domain
// case-insensitively (RFC 5321 section 2.4).
func addressKey(address string) string {
	address = strings.TrimSpace(address)
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return address
	}
	return address[:at] + "@" + strings.ToLower(address[at+1:])
}

// dedupeAddresses returns the distinct addresses in first-seen order.
func dedupeAddresses(groups ...[]mailemail.Address) []mailemail.Address {
	result := make([]mailemail.Address, 0)
	seen := make(map[string]bool)
	for _, group := range groups {
		for _, addr := range group {
			key := addressKey(addr.Address)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, addr)
		}
	}
	return result
}

func storageAddress(address submissionAddress) storage.SubmissionAddress {
	var parameters map[string]*string
	if address.Parameters != nil {
		parameters = make(map[string]*string, len(address.Parameters))
		for name, value := range address.Parameters {
			if value == nil {
				parameters[name] = nil
				continue
			}
			copy := *value
			parameters[name] = &copy
		}
	}
	return storage.SubmissionAddress{Email: address.Email, Parameters: parameters}
}

func senderEnvelope(from submissionAddress, recipients []envelopeRecipient) *mailemail.SubmissionEnvelope {
	rcptTo := make([]mailemail.EnvelopeAddress, 0, len(recipients))
	for _, recipient := range recipients {
		rcptTo = append(rcptTo, mailemail.EnvelopeAddress{
			Email:      recipient.Address.Address,
			Parameters: copyParameters(recipient.Parameters),
		})
	}
	return &mailemail.SubmissionEnvelope{
		MailFrom: mailemail.EnvelopeAddress{Email: from.Email, Parameters: copyParameters(from.Parameters)},
		RcptTo:   rcptTo,
	}
}

func copyParameters(parameters map[string]*string) map[string]*string {
	if parameters == nil {
		return nil
	}
	copy := make(map[string]*string, len(parameters))
	for name, value := range parameters {
		if value == nil {
			copy[name] = nil
			continue
		}
		copied := *value
		copy[name] = &copied
	}
	return copy
}

// applySubmissionPatch validates an EmailSubmission PatchObject against the
// current object. RFC 8620 section 5.3 permits a whole-object patch and lets a
// client repeat server-set properties as long as their value is unchanged.
// Only a pending -> canceled undoStatus transition is mutable (RFC 8621 7.5).
func applySubmissionPatch(record storage.SubmissionRecord, patch json.RawMessage) (storage.SubmissionRecord, *base.SetError, error) {
	currentJSON, err := json.Marshal(toJMAPSubmission(record))
	if err != nil {
		return record, nil, err
	}
	patchedJSON, err := base.ApplyPatch(currentJSON, patch)
	if err != nil {
		return record, nil, err
	}
	var current, patched map[string]json.RawMessage
	if err := json.Unmarshal(currentJSON, &current); err != nil {
		return record, nil, err
	}
	if err := json.Unmarshal(patchedJSON, &patched); err != nil {
		return record, nil, err
	}

	changed := make(map[string]json.RawMessage)
	for name := range current {
		if _, ok := patched[name]; !ok {
			changed[name] = nil
			continue
		}
		if !sameJSON(current[name], patched[name]) {
			changed[name] = patched[name]
		}
	}
	for name, value := range patched {
		if _, ok := current[name]; !ok {
			changed[name] = value
		}
	}

	invalidSet := make(map[string]bool)
	// A null patch value for an unknown property is a no-op in ApplyPatch,
	// so inspect the raw patch keys as well. Nested pointers are never allowed
	// because every nested EmailSubmission property is immutable.
	var patchFields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &patchFields); err != nil {
		return record, nil, err
	}
	for name := range patchFields {
		if !submissionPatchProperties[name] {
			invalidSet[name] = true
		}
	}
	for name := range changed {
		if name != "undoStatus" {
			invalidSet[name] = true
		}
	}
	if len(invalidSet) > 0 {
		invalid := make([]string, 0, len(invalidSet))
		for name := range invalidSet {
			invalid = append(invalid, name)
		}
		sort.Strings(invalid)
		setError := base.InvalidProperties("Immutable EmailSubmission property changed", invalid...)
		return record, &setError, nil
	}

	rawUndoStatus, changedUndoStatus := changed["undoStatus"]
	if !changedUndoStatus {
		return record, nil, nil
	}
	var undoStatus string
	if err := json.Unmarshal(rawUndoStatus, &undoStatus); err != nil ||
		(undoStatus != "pending" && undoStatus != "final" && undoStatus != "canceled") {
		setError := base.InvalidProperties("Invalid undoStatus value", "undoStatus")
		return record, &setError, nil
	}
	if undoStatus == "canceled" && record.UndoStatus == "pending" {
		record.UndoStatus = "canceled"
		return record, nil, nil
	}
	// The advertised profile relays immediately, so every stored record is
	// "final". A cancel attempt on such a record has the RFC 8621 SetError.
	setError := base.NewSetError("cannotUnsend", "This submission has already been relayed")
	return record, &setError, nil
}

func sameJSON(left, right json.RawMessage) bool {
	return canonicalJSON(left) == canonicalJSON(right)
}

func canonicalJSON(raw json.RawMessage) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

// submissionCreateResponse returns only the properties the client did not
// supply, as RFC 8620 section 5.3 requires for a /set "created" entry. The
// Identity and Email ids are always supplied, and the envelope is omitted when
// the client supplied one.
func submissionCreateResponse(record storage.SubmissionRecord, clientSuppliedEnvelope bool) map[string]any {
	full := toJMAPSubmission(record)
	response := map[string]any{
		"id":             full.ID,
		"threadId":       full.ThreadID,
		"sendAt":         full.SendAt,
		"undoStatus":     full.UndoStatus,
		"deliveryStatus": full.DeliveryStatus,
		"dsnBlobIds":     full.DSNBlobIDs,
		"mdnBlobIds":     full.MDNBlobIDs,
	}
	if !clientSuppliedEnvelope {
		response["envelope"] = full.Envelope
	}
	return response
}

// relayedDelivery is the RFC 8621 delivery state for a message accepted by an
// external provider. Successful relaying is not final delivery, and SES/MTA
// provide no per-recipient DSN here, so delivered is "unknown" with a
// RFC-shaped synthetic reply.
func relayedDelivery() storage.SubmissionDelivery {
	return storage.SubmissionDelivery{
		SMTPReply: "250 2.0.0 Relayed to outbound provider",
		Delivered: "unknown",
		Displayed: "unknown",
	}
}

func setErrorFromSendError(err error, maxSize uint64) base.SetError {
	switch {
	case errors.Is(err, sender.ErrRateLimited):
		return base.NewSetError("rateLimit", err.Error())
	case errors.Is(err, sender.ErrMessageTooLarge):
		return base.TooLarge(err.Error(), maxSize)
	default:
		return base.NewSetError("serverFail", "Failed to send email: "+err.Error())
	}
}
