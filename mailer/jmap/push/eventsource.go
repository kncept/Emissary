package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	// pingBounds satisfy RFC 8620 section 7.3: the minimum may not exceed 30
	// seconds and the maximum may not be less than 300 seconds.
	minPingSeconds = 10
	maxPingSeconds = 300
	defaultPing    = 30
)

// ServeHTTP implements the authenticated EventSource resource from RFC 8620
// section 7.3. Claims must already be attached to the request context (the
// HTTP layer accepts either an Authorization header or a token query
// parameter). The account is the authenticated user's account unless an
// explicitly requested account is permitted by the claims.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Subject == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	accountID := claims.Subject
	if requested := strings.TrimSpace(r.URL.Query().Get("accountId")); requested != "" && requested != accountID {
		if !base.CanAccessAccount(r.Context(), requested) {
			http.Error(w, "account not accessible", http.StatusForbidden)
			return
		}
		accountID = requested
	}

	types, allTypes, err := parseEventTypes(r.URL.Query().Get("types"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	closeAfter, err := parseCloseAfter(r.URL.Query().Get("closeafter"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ping, err := parsePing(r.URL.Query().Get("ping"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sub, err := h.subscribe(r.Context(), accountID, types, allTypes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	defer h.unsubscribe(sub)

	// Buffered proxies must not delay events, and the server's global write
	// timeout must not terminate a stream that is intentionally long-lived.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})

	h.Remember(r.Context(), accountID)

	// Resolve Last-Event-ID before entering the stream. An initial connection
	// without an id establishes the baseline instead of replaying all history.
	states, err := h.Snapshot(r.Context(), accountID)
	if err != nil {
		slog.Warn("JMAP push: initial state snapshot failed", "account", accountID, "err", err)
	}
	lastID := r.Header.Get("Last-Event-ID")
	if lastID != "" && len(states) > 0 {
		changed := missedTypes(decodeEventID(lastID), states, types, allTypes)
		if len(changed) > 0 {
			h.setLast(accountID, states)
			event := NewStateChange(accountID, changed)
			if err := writeStateEvent(w, controller, event, encodeEventID(states)); err != nil {
				return
			}
			if closeAfter == closeAfterState {
				return
			}
		}
	} else {
		h.setLast(accountID, states)
	}

	refreshTick := time.NewTicker(h.pollInterval)
	defer refreshTick.Stop()
	pingDelay := h.pingInterval(ping)
	var pingC <-chan time.Time
	var pingTimer *time.Timer
	if ping > 0 {
		pingTimer = time.NewTimer(pingDelay)
		defer pingTimer.Stop()
		pingC = pingTimer.C
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.done:
			return
		case <-refreshTick.C:
			if claims.SID != "" && h.sessionAlive != nil && !h.sessionAlive(r.Context(), claims.SID) {
				return
			}
			if err := h.Refresh(r.Context(), accountID); err != nil {
				slog.Warn("JMAP push: refresh failed", "account", accountID, "err", err)
			}
		case <-pingC:
			// RFC 8620: a ping is due this long after the previous event, not
			// merely after connection setup.
			payload, _ := json.Marshal(map[string]int{"interval": ping})
			if err := writeEvent(w, controller, "ping", "", string(payload)); err != nil {
				return
			}
			resetPingTimer(pingTimer, pingDelay)
			pingC = pingTimer.C
		case event := <-sub.events:
			id, err := h.EventID(r.Context(), accountID)
			if err != nil {
				slog.Warn("JMAP push: event id failed", "account", accountID, "err", err)
			}
			if err := writeStateEvent(w, controller, event, id); err != nil {
				return
			}
			if pingTimer != nil {
				resetPingTimer(pingTimer, pingDelay)
			}
			if closeAfter == closeAfterState {
				return
			}
		}
	}
}

func resetPingTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

const (
	closeAfterState = "state"
	closeAfterNo    = "no"
)

func parseCloseAfter(raw string) (string, error) {
	if raw == "" {
		return closeAfterNo, nil
	}
	switch raw {
	case closeAfterState, closeAfterNo:
		return raw, nil
	default:
		return "", fmt.Errorf("closeafter must be %q or %q", closeAfterState, closeAfterNo)
	}
}

func parseEventTypes(raw string) ([]string, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "*" {
		return nil, true, nil
	}
	names := make([]string, 0)
	for _, name := range strings.Split(trimmed, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, true, nil
	}
	return names, false, nil
}

func parsePing(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultPing, nil
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds < 0 {
		return 0, errors.New("ping must be a non-negative integer")
	}
	if seconds == 0 {
		return 0, nil
	}
	if seconds < minPingSeconds {
		return minPingSeconds, nil
	}
	if seconds > maxPingSeconds {
		return maxPingSeconds, nil
	}
	return seconds, nil
}

// missedTypes compares a Last-Event-ID snapshot with the current state and
// returns only the tokens for the types this connection subscribed to.
func missedTypes(seen map[string]string, states map[base.StateKind]string, types []string, allTypes bool) map[string]string {
	changed := make(map[string]string)
	for kind, token := range states {
		name, ok := typeNames[kind]
		if !ok {
			continue
		}
		if !allTypes && !contains(types, name) {
			continue
		}
		if previous, ok := seen[name]; ok && previous == token {
			continue
		}
		changed[name] = token
	}
	return changed
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// setLast records the observed baseline without publishing an event.
func (h *Hub) setLast(accountID string, states map[base.StateKind]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(states) == 0 {
		delete(h.last, accountID)
		return
	}
	h.last[accountID] = states
}

func writeStateEvent(w io.Writer, controller *http.ResponseController, event StateChange, id string) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := writeEvent(w, controller, "state", id, string(payload)); err != nil {
		return err
	}
	return nil
}

func writeEvent(w io.Writer, controller *http.ResponseController, name, id, data string) error {
	var builder strings.Builder
	if name != "" {
		fmt.Fprintf(&builder, "event: %s\n", name)
	}
	if id != "" {
		fmt.Fprintf(&builder, "id: %s\n", id)
	}
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(&builder, "data: %s\n", line)
	}
	builder.WriteByte('\n')
	if _, err := io.WriteString(w, builder.String()); err != nil {
		return err
	}
	return controller.Flush()
}
