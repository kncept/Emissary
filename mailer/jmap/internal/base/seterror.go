package base

import (
	"errors"
	"strings"

	"github.com/kncept/Emissary/mailer/components/storage"
)

// SetError is the RFC 8620 section 5.3 SetError object. Every /set response
// uses this type so the JSON shape (and the optional RFC-defined fields) is
// identical across all data types.
type SetError struct {
	Type              string   `json:"type"`
	Description       string   `json:"description"`
	Properties        []string `json:"properties,omitempty"`
	ExistingID        string   `json:"existingId,omitempty"`
	MaxSize           *uint64  `json:"maxSize,omitempty"`
	MaxRecipients     *uint64  `json:"maxRecipients,omitempty"`
	InvalidRecipients []string `json:"invalidRecipients,omitempty"`
	NotFound          []string `json:"notFound,omitempty"`
}

// setErrorTypes enumerates every SetError type the server may emit. The core
// and mail sets come from RFC 8620 section 5.3 and RFC 8621 sections 2.5, 6.3
// and 7.5; the remaining entries are the documented extension types already
// used by individual methods (see the JMAP plan's capability inventory).
var setErrorTypes = map[string]bool{
	// RFC 8620 section 5.3.
	"forbidden":         true,
	"overQuota":         true,
	"tooLarge":          true,
	"rateLimit":         true,
	"notFound":          true,
	"invalidPatch":      true,
	"willDestroy":       true,
	"invalidProperties": true,
	"singleton":         true,
	// RFC 8620 section 5.3 for /copy and method-level errors reused as
	// SetErrors by this server.
	"alreadyExists": true,
	"stateMismatch": true,
	// RFC 8621 mail extensions.
	"mailboxHasChild":   true,
	"mailboxHasEmail":   true,
	"invalidEmail":      true,
	"tooManyRecipients": true,
	"forbiddenMailFrom": true,
	"forbiddenFrom":     true,
	"forbiddenToSend":   true,
	"cannotUnsend":      true,
	"blobNotFound":      true,
	// Documented server extensions used in existing /set responses.
	"serverFail":        true,
	"invalidRecipients": true,
	"noRecipients":      true,
	"invalidArguments":  true,
}

// IsSetErrorType reports whether typ is a declared SetError type. It is used
// by tests to ensure no method invents an undocumented type.
func IsSetErrorType(typ string) bool { return setErrorTypes[typ] }

// NewSetError builds a SetError with the mandatory type and description.
func NewSetError(typ, description string) SetError {
	return SetError{Type: typ, Description: description}
}

// InvalidProperties builds an invalidProperties SetError. RFC 8620 section
// 5.3 recommends a "properties" array listing every offending property.
func InvalidProperties(description string, properties ...string) SetError {
	return SetError{Type: "invalidProperties", Description: description, Properties: append([]string(nil), properties...)}
}

// AlreadyExists builds an alreadyExists SetError. RFC 8620 section 5.3
// requires the "existingId" field on this type.
func AlreadyExists(description, existingID string) SetError {
	return SetError{Type: "alreadyExists", Description: description, ExistingID: existingID}
}

// TooLarge builds a tooLarge SetError. RFC 8621 section 7.5 requires the
// "maxSize" field when this type is used for EmailSubmission/create.
func TooLarge(description string, maxSize uint64) SetError {
	return SetError{Type: "tooLarge", Description: description, MaxSize: &maxSize}
}

// StoreSetError maps a storage failure to the RFC SetError that describes it.
// Every /set method uses this mapping so quota, size and reference failures
// produce the same type across Email, Blob and future binary types. maxSize is
// zero when the caller has no size limit to advertise.
func StoreSetError(err error, fallback string, maxSize uint64) SetError {
	if err == nil {
		return NewSetError("serverFail", fallback)
	}
	if errors.Is(err, storage.ErrBlobQuota) {
		return NewSetError("overQuota", "Blob storage quota exceeded")
	}
	if errors.Is(err, storage.ErrBlobReferenced) {
		return NewSetError("forbidden", "Blob is still referenced by a JMAP object")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "exceeds maximum size") || strings.Contains(message, "too large") ||
		strings.Contains(message, "size limit") {
		if maxSize == 0 {
			return NewSetError("tooLarge", "Message exceeds the server size limit")
		}
		return TooLarge("Message exceeds the server size limit", maxSize)
	}
	return NewSetError("serverFail", fallback)
}

// InvalidEmail builds the RFC 8621 EmailSubmission create SetError. The
// "properties" field lists every invalid Email property.
func InvalidEmail(description string, properties ...string) SetError {
	return SetError{Type: "invalidEmail", Description: description, Properties: append([]string(nil), properties...)}
}

// TooManyRecipients builds the RFC 8621 SetError carrying the mandatory
// maxRecipients field.
func TooManyRecipients(description string, maxRecipients uint64) SetError {
	return SetError{Type: "tooManyRecipients", Description: description, MaxRecipients: &maxRecipients}
}

// InvalidRecipients builds the RFC 8621 SetError listing every bad envelope
// rcptTo address.
func InvalidRecipients(description string, addresses []string) SetError {
	return SetError{Type: "invalidRecipients", Description: description, InvalidRecipients: append([]string(nil), addresses...)}
}

// BlobNotFound builds the RFC 8621 SetError listing missing blob ids.
func BlobNotFound(description string, ids []string) SetError {
	return SetError{Type: "blobNotFound", Description: description, NotFound: append([]string(nil), ids...)}
}
