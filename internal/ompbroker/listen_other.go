//go:build !linux

package ompbroker

// listenedBy cannot tell a socket's owner off Linux, where brokers never run.
func listenedBy(int, int) bool { return false }
