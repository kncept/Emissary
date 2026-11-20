// Package model holds small JMAP value types shared by multiple object
// subpackages. Keeping them here prevents feature packages from depending on
// each other just to reuse a JSON shape.
package model

// EmailAddress represents a JMAP Email address object.
type EmailAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}
