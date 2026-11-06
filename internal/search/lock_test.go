package search

import (
	"errors"
	"fmt"
	"testing"
)

type codedError struct{ code int }

func (e codedError) Error() string { return fmt.Sprintf("sqlite: code %d", e.code) }
func (e codedError) Code() int     { return e.code }

func TestIsLockError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"busy code", codedError{code: 5}, true},
		{"locked code", codedError{code: 6}, true},
		{"wrapped busy code", fmt.Errorf("index: %w", codedError{code: 5}), true},
		{"database is locked text", errors.New("database is locked (5) (SQLITE_BUSY)"), true},
		{"table is locked text", errors.New("database table is locked"), true},
		{"constraint code", codedError{code: 19}, false},
		{"unrelated", errors.New("syntax error near FROM"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLockError(tc.err); got != tc.want {
				t.Fatalf("isLockError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
