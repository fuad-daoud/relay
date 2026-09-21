package herdr

import "errors"

// ErrNoSocket is returned when herdr's unix socket cannot be reached: no
// HERDR_SOCKET_PATH and no default socket, an older herdr that does not speak
// the events protocol, a permission error, or a stub Herdr (relay serve,
// tests). Subscribe wraps it, so daemon code falls back to polling with a
// single errors.Is check rather than parsing the underlying dial or protocol
// error.
var ErrNoSocket = errors.New("herdr socket unavailable")
