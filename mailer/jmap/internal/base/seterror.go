package base

// SetError is the RFC 8620 section 5.3 SetError object. Every /set response
// uses this type so the JSON shape (and the optional RFC-defined fields) is
// identical across all data types.
type SetError struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Properties  []string `json:"properties,omitempty"`
	ExistingID  string   `json:"existingId,omitempty"`
	MaxSize     *uint64  `json:"maxSize,omitempty"`
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
	"mailboxHasChild": true,
	"mailboxHasEmail": true,
	"forbiddenFrom":   true,
	"blobNotFound":    true,
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
