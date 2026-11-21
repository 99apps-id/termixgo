package browser

import "testing"

// A session whose protocol connection dropped reports itself dead, so Open
// starts a fresh browser instead of handing back one that fails every action.
func TestSessionDeadWhenConnectionClosed(t *testing.T) {
	conn := &cdpConn{pending: map[int]chan cdpMessage{}, done: make(chan struct{})}
	session := &Session{conn: conn}
	if session.dead() {
		t.Fatalf("a live connection is not dead")
	}
	conn.shutdown(nil)
	if !session.dead() {
		t.Fatalf("a closed connection should make the session dead")
	}
	closed := &Session{closed: true}
	if !closed.dead() {
		t.Fatalf("a closed session is dead")
	}
}

// A session busy with an action is treated as alive without waiting on it.
func TestSessionBusyIsAlive(t *testing.T) {
	conn := &cdpConn{pending: map[int]chan cdpMessage{}, done: make(chan struct{})}
	conn.shutdown(nil)
	session := &Session{conn: conn}
	session.actionMu.Lock()
	defer session.actionMu.Unlock()
	if session.dead() {
		t.Fatalf("a busy session must not block or be reported dead")
	}
}
