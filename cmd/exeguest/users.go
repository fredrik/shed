//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// guestUser is a login identity resolved from the image's /etc/passwd.
type guestUser struct {
	Name   string
	UID    uint32
	GID    uint32
	Home   string
	Shell  string
	Groups []uint32 // supplementary groups incl. primary
}

// sessionTarget is the identity ssh sessions run as, resolved once at
// sshd startup: the host-requested user when the image has it, root
// otherwise.
var sessionTarget = &guestUser{Name: "root", Home: "/root", Shell: "/bin/sh"}

func resolveSessionTarget(preferred string) {
	if preferred != "" && preferred != "root" {
		if u, err := lookupUser(preferred); err == nil {
			sessionTarget = u
			fmt.Printf("exeguest: ssh sessions run as %s\n", u.Name)
			return
		}
	}
	if u, err := lookupUser("root"); err == nil {
		sessionTarget = u
	}
}

func lookupUser(name string) (*guestUser, error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 || fields[0] != name {
			continue
		}
		uid, err1 := strconv.ParseUint(fields[2], 10, 32)
		gid, err2 := strconv.ParseUint(fields[3], 10, 32)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad passwd entry for %s", name)
		}
		u := &guestUser{
			Name:  name,
			UID:   uint32(uid),
			GID:   uint32(gid),
			Home:  fields[5],
			Shell: fields[6],
		}
		if u.Home == "" {
			u.Home = "/"
		}
		if u.Shell == "" {
			u.Shell = "/bin/sh"
		} else if _, err := os.Stat(u.Shell); err != nil {
			u.Shell = "/bin/sh"
		}
		u.Groups = lookupGroups(name, u.GID)
		return u, nil
	}
	return nil, fmt.Errorf("no passwd entry for %s", name)
}

func lookupGroups(name string, primary uint32) []uint32 {
	groups := []uint32{primary}
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return groups
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		gid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil || uint32(gid) == primary {
			continue
		}
		for _, member := range strings.Split(fields[3], ",") {
			if member == name {
				groups = append(groups, uint32(gid))
				break
			}
		}
	}
	return groups
}
