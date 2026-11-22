package jmap

import (
	"github.com/kncept/Emissary/mailer/jmap/blob"
	"github.com/kncept/Emissary/mailer/jmap/core"
	"github.com/kncept/Emissary/mailer/jmap/email"
	"github.com/kncept/Emissary/mailer/jmap/emailsubmission"
	"github.com/kncept/Emissary/mailer/jmap/identity"
	"github.com/kncept/Emissary/mailer/jmap/mailbox"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
	"github.com/kncept/Emissary/mailer/jmap/searchsnippet"
	"github.com/kncept/Emissary/mailer/jmap/thread"
	"github.com/kncept/Emissary/mailer/jmap/vacationresponse"
)

// MethodHandler is the interface for handling individual JMAP method calls.
// It is the union of the method contracts owned by each object subpackage.
type MethodHandler interface {
	core.MethodHandler
	mailbox.MethodHandler
	thread.MethodHandler
	email.MethodHandler
	searchsnippet.MethodHandler
	identity.MethodHandler
	emailsubmission.MethodHandler
	vacationresponse.MethodHandler
	pushsubscription.MethodHandler
	blob.MethodHandler
}

// ImplicitSetHandler is re-exported for dispatchers and tests that need the
// optional second response emitted by EmailSubmission/set.
type ImplicitSetHandler = emailsubmission.ImplicitSetHandler
