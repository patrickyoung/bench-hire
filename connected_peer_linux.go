//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// The grant file is readable under Cage. It is therefore not a bearer
// credential. Bind calls to the kernel peer and a recorded Hire execution.
func appPeerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var callErr error
	err = raw.Control(func(fd uintptr) {
		cred, callErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return 0, err
	}
	if callErr != nil {
		return 0, callErr
	}
	if int(cred.Uid) != os.Getuid() {
		return 0, errors.New("connected apps belong to this operator")
	}
	return int(cred.Pid), nil
}

func appProcessIdentity(pid int) (parent int, start string, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, "", errors.New("cannot identify calling process")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return 0, "", errors.New("incomplete process identity")
	}
	parent, err = strconv.Atoi(fields[1])
	return parent, fields[19], err
}

func appExecutionContains(pid int, execution appExecution) bool {
	for n := 0; n < 128 && pid > 1; n++ {
		parent, start, err := appProcessIdentity(pid)
		if err != nil {
			return false
		}
		if pid == execution.PID {
			return start == execution.Start
		}
		pid = parent
	}
	return false
}
