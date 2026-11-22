// Package searchsnippet implements the SearchSnippet method from RFC 8621
// section 5.
package searchsnippet

import (
	"context"
	"encoding/json"
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// MethodGet is the SearchSnippet/get method name.
const MethodGet = "SearchSnippet/get"

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodGet} }

// MethodHandler is the contract the dispatcher expects from SearchSnippet
// handlers.
type MethodHandler interface {
	SearchSnippetGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the SearchSnippet methods.
type Handler struct{ deps *base.Deps }

// New creates a SearchSnippet method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// maxPreviewBytes is RFC 8621 section 5's hard limit on the preview property.
const maxPreviewBytes = 255

type snippet struct {
	EmailID string  `json:"emailId"`
	Subject *string `json:"subject"`
	Preview *string `json:"preview"`
}

// SearchSnippetGet returns one SearchSnippet per known email id. The filter is
// parsed with the same helpers as Email/query, so snippets agree with the
// search that produced the ids. Subject and preview are highlighted with
// <mark> tags when text from the filter matches; they are null when it does
// not (RFC 8621 section 5).
func (h *Handler) SearchSnippetGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		EmailIDs []string        `json:"emailIds"`
		Filter   json.RawMessage `json:"filter,omitempty"`
	}
	if err := base.RejectUnknownArguments(args, base.ArgumentSet(
		"accountId", "emailIds", "filter")); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	filter, err := email.ParseEmailFilter(args)
	if err != nil {
		return nil, err
	}

	// Thread and keyword conditions need the same account-wide view that
	// Email/query builds, otherwise a filter could match a different set.
	all, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		return nil, err
	}
	filterContext, err := email.NewFilterContext(ctx, h.deps, accountID, all)
	if err != nil {
		return nil, err
	}
	terms := filterTerms(filter)

	list := make([]snippet, 0, len(req.EmailIDs))
	var notFound []string
	for _, id := range req.EmailIDs {
		listed, err := h.deps.Storage.GetEmail(ctx, accountID, id)
		if err != nil || listed == nil || listed.Ref == nil || listed.Email == nil ||
			!strings.EqualFold(listed.Ref.AccountID, accountID) {
			notFound = append(notFound, id)
			continue
		}
		result := snippet{EmailID: id}
		if len(terms) > 0 && filterContext.Matches(ctx, listed, filter) {
			if subject := highlight(listed.Email.Subject, terms, 0); subject != "" {
				result.Subject = &subject
			}
			if preview := previewSnippet(listed.Email, terms); preview != "" {
				result.Preview = &preview
			}
		}
		list = append(list, result)
	}
	return base.GetResult{
		AccountID: accountID,
		List:      list,
		NotFound:  notFound,
	}.Marshal()
}

// filterTerms extracts the text values carried by a filter (including nested
// filter operators). These are the words/phrases RFC 8621 section 5 says to
// wrap in <mark> tags. Longer phrases are tried first so a multi-word match
// is highlighted as a unit rather than as separate words.
func filterTerms(filter *email.EmailFilter) []string {
	if filter == nil {
		return nil
	}
	var terms []string
	var collect func(*email.EmailFilter)
	collect = func(current *email.EmailFilter) {
		if current == nil {
			return
		}
		if current.Operator != "" {
			for _, child := range current.Conditions {
				collect(child)
			}
			return
		}
		values := []*string{
			current.Text, current.Subject, current.Body,
			current.From, current.To, current.Cc, current.Bcc,
		}
		for _, value := range values {
			addTerm(&terms, value)
		}
		if len(current.Header) == 2 {
			addTerm(&terms, &current.Header[1])
		}
	}
	collect(filter)
	sort.Slice(terms, func(i, j int) bool {
		if len(terms[i]) != len(terms[j]) {
			return len(terms[i]) > len(terms[j])
		}
		return terms[i] < terms[j]
	})
	return terms
}

func addTerm(terms *[]string, value *string) {
	if value == nil {
		return
	}
	phrase := strings.TrimSpace(*value)
	if phrase == "" {
		return
	}
	seen := make(map[string]bool, len(*terms)+1)
	for _, term := range *terms {
		seen[term] = true
	}
	appendTerm := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		*terms = append(*terms, term)
	}
	appendTerm(phrase)
	for _, word := range strings.Fields(phrase) {
		appendTerm(word)
	}
}

func termPattern(terms []string) *regexp.Regexp {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, regexp.QuoteMeta(term))
	}
	matcher, err := regexp.Compile("(?i)" + strings.Join(parts, "|"))
	if err != nil {
		return nil
	}
	return matcher
}

// highlight returns text with every matching term wrapped in <mark> tags and
// HTML metacharacters escaped, as required by RFC 8621 section 5. A maxBytes
// value greater than zero limits the result to a body-sized window.
func highlight(text string, terms []string, maxBytes int) string {
	if text == "" || len(terms) == 0 {
		return ""
	}
	matcher := termPattern(terms)
	if matcher == nil {
		return ""
	}
	first := matcher.FindStringIndex(text)
	if first == nil {
		return ""
	}
	if maxBytes <= 0 {
		return renderHighlight(text, 0, len(text), matcher)
	}
	left, right := 48, 160
	for {
		start := runeBoundary(text, first[0]-left)
		end := runeBoundary(text, first[1]+right)
		out := renderHighlight(text, start, end, matcher)
		if len(out) <= maxBytes {
			return out
		}
		if left == 0 && right == 0 {
			return shortenMarked(out, maxBytes)
		}
		if left > right {
			left -= min(left, 16)
		} else {
			right -= min(right, 16)
		}
	}
}

// runeBoundary keeps a window from cutting a multi-byte UTF-8 character.
func runeBoundary(text string, index int) int {
	if index < 0 {
		return 0
	}
	if index > len(text) {
		return len(text)
	}
	for index < len(text) && !utf8.RuneStart(text[index]) {
		index++
	}
	return index
}

func renderHighlight(text string, start, end int, matcher *regexp.Regexp) string {
	var out strings.Builder
	if start > 0 {
		out.WriteString("...")
	}
	last := start
	for _, match := range matcher.FindAllStringIndex(text[start:end], -1) {
		from, to := start+match[0], start+match[1]
		out.WriteString(html.EscapeString(text[last:from]))
		out.WriteString("<mark>")
		out.WriteString(html.EscapeString(text[from:to]))
		out.WriteString("</mark>")
		last = to
	}
	out.WriteString(html.EscapeString(text[last:end]))
	if end < len(text) {
		out.WriteString("...")
	}
	return out.String()
}

// shortenMarked keeps the <mark> tags intact when a very long matched term
// cannot fit in the RFC's 255-octet preview budget.
func shortenMarked(out string, maxBytes int) string {
	if len(out) <= maxBytes {
		return out
	}
	const open, close = "<mark>", "</mark>"
	start := strings.Index(out, open)
	end := strings.LastIndex(out, close)
	if start < 0 || end < 0 || end <= start+len(open) {
		return cutBytes(out, maxBytes)
	}
	termStart := start + len(open)
	excess := len(out) - maxBytes
	term := out[termStart:end]
	kept := len(term) - excess
	if kept <= 0 {
		kept = 1
	}
	return out[:termStart] + cutBytes(term, kept) + out[end:]
}

func cutBytes(text string, size int) string {
	if size >= len(text) {
		return text
	}
	for size > 0 && !utf8.RuneStart(text[size]) {
		size--
	}
	return text[:size]
}

var htmlBlocks = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
var htmlTags = regexp.MustCompile(`(?is)<!--.*?-->|<[^>]+>`)

// previewSnippet uses the same plaintext body that Email/query searches, with
// a minimal HTML fallback for HTML-only messages.
func previewSnippet(email *mailemail.UserEmail, terms []string) string {
	text := email.Text
	if strings.TrimSpace(text) == "" && email.HTML != "" {
		text = htmlTags.ReplaceAllString(htmlBlocks.ReplaceAllString(email.HTML, " "), " ")
		text = strings.Join(strings.Fields(html.UnescapeString(text)), " ")
	}
	return highlight(text, terms, maxPreviewBytes)
}

var _ MethodHandler = (*Handler)(nil)
