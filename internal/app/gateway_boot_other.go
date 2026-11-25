//go:build !linux

package app

// currentBootTime is unknown off Linux; a restart then reads as a service
// restart or an update, never as a host restart.
func currentBootTime() int64 { return 0 }
