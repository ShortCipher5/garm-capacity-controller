//go:build linux

package nativezfs

import (
	"golang.org/x/sys/unix"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ExecHelper runs only in the separate pinned child helper. It preserves only
// the pinned executable and held user namespace across exec, never ambient
// supervisor listeners. The fixed grammar permits only construction and one-way budget sealing; no destroy command.
func ExecHelper() error {
	if os.Geteuid() != 0 || !validCommand(os.Args[1:]) {
		return ErrRefused
	}
	var st unix.Stat_t
	if unix.Fstat(3, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Mode&0111 == 0 || st.Mode&07000 != 0 {
		return ErrRefused
	}
	kind, e := unix.IoctlRetInt(4, unix.NS_GET_NSTYPE)
	if e != nil || kind != unix.CLONE_NEWUSER {
		return ErrRefused
	}
	return cleanExec(os.Args[1:])
}
func cleanExec(args []string) error {
	if unix.CloseRange(5, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) != nil {
		return ErrRefused
	}
	return syscall.Exec("/proc/self/fd/3", append([]string{"zfs"}, args...), []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"})
}
func validCommand(a []string) bool {
	if len(a) == 11 && a[0] == "create" && a[1] == "-u" && a[2] == "-o" && strings.HasPrefix(a[3], "quota=") && a[4] == "-o" && a[5] == "mountpoint=legacy" && a[6] == "-o" && a[7] == "canmount=noauto" && a[8] == "-o" && a[9] == "zoned=off" && artifactDataset(a[10]) {
		q, e := strconv.ParseInt(strings.TrimPrefix(a[3], "quota="), 10, 64)
		return e == nil && q > 0 && a[3] == "quota="+strconv.FormatInt(q, 10)
	}

	if len(a) == 7 && a[0] == "get" && a[1] == "-H" && a[2] == "-p" && a[3] == "-o" && a[4] == "value,source" && (a[5] == "zoned" || a[5] == "quota") && dataset(a[6]) {
		return true
	}
	if len(a) == 3 && a[0] == "set" && strings.HasPrefix(a[1], "quota=") && sealTarget(a[2]) {
		q, e := strconv.ParseInt(strings.TrimPrefix(a[1], "quota="), 10, 64)
		return e == nil && q > 0 && a[1] == "quota="+strconv.FormatInt(q, 10)
	}

	if len(a) == 1 && a[0] == "version" {
		return true
	}
	if len(a) == 7 && a[0] == "get" && a[1] == "-H" && a[2] == "-p" && a[3] == "-o" && a[4] == "name,property,value" && a[5] == strings.Join(properties, ",") && dataset(a[6]) {
		return true
	}
	if len(a) == 3 && (a[0] == "zone" && dataset(a[2]) || a[0] == "unzone" && sealParent(a[2])) && a[1] == "/proc/self/fd/4" {
		return true
	}
	if len(a) == 11 && a[0] == "create" && a[1] == "-u" && a[2] == "-o" && strings.HasPrefix(a[3], "quota=") && a[4] == "-o" && a[5] == "mountpoint=none" && a[6] == "-o" && a[7] == "canmount=off" && a[8] == "-o" && a[9] == "zoned=off" && dataset(a[10]) {
		q, e := strconv.ParseInt(strings.TrimPrefix(a[3], "quota="), 10, 64)
		return e == nil && q > 0 && a[3] == "quota="+strconv.FormatInt(q, 10)
	}
	if len(a) == 3 && a[0] == "set" && a[1] == "zoned=on" && dataset(a[2]) {
		return true
	}
	return len(a) == 7 && a[0] == "create" && a[1] == "-u" && a[2] == "-o" && a[3] == "mountpoint=legacy" && a[4] == "-o" && a[5] == "canmount=noauto" && dataset(a[6]) && (strings.HasSuffix(a[6], "/runtime") || strings.HasSuffix(a[6], "/pool"))
}

func sealParent(s string) bool {
	if !dataset(s) {
		return false
	}
	i := strings.LastIndex(s, "/domain-")
	return i > 0 && birthID(s[i+8:])
}
func sealTarget(s string) bool {
	if !dataset(s) {
		return false
	}
	i := strings.LastIndex(s, "/domain-")
	if i < 1 || len(s) < i+8+64+1 || !birthID(s[i+8:i+8+64]) {
		return false
	}
	suffix := s[i+8+64:]
	if suffix == "/runtime" {
		return true
	}
	for _, n := range sealedCategories {
		if suffix == "/pool/"+n {
			return true
		}
	}
	return false
}

func artifactDataset(s string) bool {
	if !dataset(s) {
		return false
	}
	i := strings.LastIndex(s, "/control-")
	return i > 0 && birthID(s[i+9:])
}
