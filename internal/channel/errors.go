package channel

import "errors"

// ErrBusy reports that another turn is already running. A transport turns it
// into one friendly line instead of showing the caller a raw error.
var ErrBusy = errors.New("a turn is already running")
