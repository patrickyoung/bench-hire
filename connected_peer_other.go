//go:build !linux

package main

import (
	"errors"
	"net"
)

func appPeerPID(*net.UnixConn) (int, error) {
	return 0, errors.New("connected-app worker calls currently require Linux process identity")
}
func appProcessIdentity(int) (int, string, error) {
	return 0, "", errors.New("connected-app worker calls currently require Linux process identity")
}
func appExecutionContains(int, appExecution) bool { return false }
