package core

import (
	"context"
	"errors"
	"testing"
)

// writes counts what reaches the backend behind the guard.
type writes struct {
	Backend
	n int
}

func (w *writes) Send(context.Context, int64, SendRequest) (Message, error) {
	w.n++
	return Message{}, nil
}
func (w *writes) Edit(context.Context, int64, int, string) error { w.n++; return nil }
func (w *writes) MarkRead(context.Context, int64, int) error     { w.n++; return nil }
func (w *writes) CreateGroup(context.Context, string) (Chat, error) {
	w.n++
	return Chat{}, nil
}

// attempt tries every kind of write on chatID and reports which were refused.
// Editing follows the same rules as sending, so it is reported with it.
func attempt(t *testing.T, policy Policy, chatID int64) (send, read, group bool, reached int) {
	t.Helper()
	backend := &writes{}
	g := Guard(backend, func() (Policy, error) { return policy, nil })
	refused := func(err error) bool {
		if err == nil {
			return false
		}
		if !errors.As(err, new(*RefusedError)) {
			t.Fatalf("unexpected error: %v", err)
		}
		return true
	}
	_, err := g.Send(context.Background(), chatID, SendRequest{Text: "x"})
	send = refused(err)
	if edit := refused(g.Edit(context.Background(), chatID, 1, "x")); edit != send {
		t.Errorf("chat %d: editing refused=%v, sending refused=%v", chatID, edit, send)
	}
	read = refused(g.MarkRead(context.Background(), chatID, 1))
	_, err = g.CreateGroup(context.Background(), "g")
	group = refused(err)
	return send, read, group, backend.n
}

func TestGuard(t *testing.T) {
	listed := []Chat{{ID: 7, Title: "Team"}}
	cases := []struct {
		name              string
		policy            Policy
		chat              int64
		send, read, group bool
	}{
		{"no policy allows everything", Policy{}, 5, false, false, false},
		{"read-only refuses everything", Policy{ReadOnly: true}, 5, true, true, true},
		{"read-only wins over the list", Policy{ReadOnly: true, WriteTo: listed}, 7, true, true, true},
		{"a listed chat can be written to", Policy{WriteTo: listed}, 7, false, false, true},
		{"an unlisted chat cannot", Policy{WriteTo: listed}, 5, true, true, true},
	}
	for _, c := range cases {
		send, read, group, reached := attempt(t, c.policy, c.chat)
		if send != c.send || read != c.read || group != c.group {
			t.Errorf("%s: refused send=%v read=%v group=%v", c.name, send, read, group)
		}
		allowed := 0
		for _, refused := range []bool{c.send, c.send, c.read, c.group} { // send twice: edit too
			if !refused {
				allowed++
			}
		}
		if reached != allowed {
			t.Errorf("%s: %d writes reached the backend, want %d", c.name, reached, allowed)
		}
	}
}

// The policy is read on every write, and a policy that cannot be read must
// stop the write rather than let it through.
func TestGuardReloadsAndFailsClosed(t *testing.T) {
	backend := &writes{}
	policy, loadErr := Policy{}, error(nil)
	g := Guard(backend, func() (Policy, error) { return policy, loadErr })

	if _, err := g.Send(context.Background(), 1, SendRequest{}); err != nil {
		t.Fatalf("unrestricted send: %v", err)
	}
	policy.ReadOnly = true
	if _, err := g.Send(context.Background(), 1, SendRequest{}); err == nil {
		t.Error("the policy change did not apply to the next write")
	}
	policy, loadErr = Policy{}, errors.New("config.json is not valid")
	if _, err := g.Send(context.Background(), 1, SendRequest{}); err == nil {
		t.Error("a write went through with no readable policy")
	}
	if backend.n != 1 {
		t.Errorf("%d writes reached the backend, want 1", backend.n)
	}
}
