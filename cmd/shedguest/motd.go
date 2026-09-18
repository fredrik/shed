package main

import "bytes"

// renderMOTD fills the placeholders in a baked-in /etc/motd. The image is
// shared by every VM and boots on whichever kernel the daemon has, so the
// VM's name and the running kernel are only known at login.
func renderMOTD(motd []byte, host, kernel string) []byte {
	motd = bytes.ReplaceAll(motd, []byte("<vmname>"), []byte(host))
	return bytes.ReplaceAll(motd, []byte("<kernel>"), []byte(kernel))
}
