package receiver

import "strings"

// IsInboundEmailKey reports whether an S3 object is a raw inbound message.
// SES writes staged messages at the bucket root; anything containing a slash is
// a nested object (for example a receiver config file or a dedup marker) and
// must not be processed or deleted as mail.
func IsInboundEmailKey(key string) bool {
	return !strings.Contains(key, "/")
}
