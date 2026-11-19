package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Policy is what an account is allowed to change. The zero value allows
// everything.
type Policy struct {
	// Account names the account in refusals.
	Account string
	// ReadOnly forbids every write.
	ReadOnly bool
	// WriteTo, when not empty, lists the only chats that may be written to.
	WriteTo []Chat
}

// RefusedError reports a write stopped by the account's guardrails.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return e.Reason }

// ownerOnly ends every refusal, so an agent asks instead of working around it.
const ownerOnly = ". Only the account's owner can change that, see `tg config`"

// allow checks a write to one chat. action names it, like "sending".
func (p Policy) allow(action string, chatID int64) error {
	if p.ReadOnly {
		return &RefusedError{fmt.Sprintf("account %q is read-only, %s is off", p.Account, action) + ownerOnly}
	}
	if len(p.WriteTo) == 0 || slices.ContainsFunc(p.WriteTo, func(c Chat) bool { return c.ID == chatID }) {
		return nil
	}
	allowed := make([]string, len(p.WriteTo))
	for i, c := range p.WriteTo {
		allowed[i] = fmt.Sprintf("%s (%d)", c.Title, c.ID)
	}
	return &RefusedError{fmt.Sprintf("account %q may only write to: %s. %s to chat %d is off",
		p.Account, strings.Join(allowed, ", "), capitalize(action), chatID) + ownerOnly}
}

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

// Guard wraps a backend so that every write is checked against the policy
// first. The policy is loaded on each write: a change to the configuration
// applies at once, even to a TUI that is already running.
func Guard(be Backend, policy func() (Policy, error)) Backend {
	return &guarded{Backend: be, policy: policy}
}

type guarded struct {
	Backend
	policy func() (Policy, error)
}

func (g *guarded) check(action string, chatID int64) error {
	p, err := g.policy()
	if err != nil {
		return err // an unreadable policy allows nothing
	}
	return p.allow(action, chatID)
}

func (g *guarded) Send(ctx context.Context, chatID int64, req SendRequest) (Message, error) {
	if err := g.check("sending", chatID); err != nil {
		return Message{}, err
	}
	return g.Backend.Send(ctx, chatID, req)
}

func (g *guarded) MarkRead(ctx context.Context, chatID int64, maxID int) error {
	if err := g.check("marking as read", chatID); err != nil {
		return err
	}
	return g.Backend.MarkRead(ctx, chatID, maxID)
}

// CreateGroup is refused under any restriction: a new chat cannot be on an
// allowlist.
func (g *guarded) CreateGroup(ctx context.Context, title string) (Chat, error) {
	p, err := g.policy()
	if err != nil {
		return Chat{}, err
	}
	if p.ReadOnly || len(p.WriteTo) > 0 {
		return Chat{}, &RefusedError{fmt.Sprintf("account %q is restricted, creating groups is off", p.Account) + ownerOnly}
	}
	return g.Backend.CreateGroup(ctx, title)
}
