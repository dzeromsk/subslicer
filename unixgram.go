package subslicer

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func UnixgramPair() (c1, c2 *net.UnixConn, err error) {
	addr1, err := TempUnixgramAddr("", "unixgram1")
	if err != nil {
		return
	}

	addr2, err := TempUnixgramAddr("", "unixgram2")
	if err != nil {
		return
	}

	c1, err = net.ListenUnixgram("unixgram", addr1)
	if err != nil {
		return
	}
	defer os.Remove(addr1.Name)

	c2, err = net.DialUnix("unixgram", addr2, addr1)
	if err != nil {
		return
	}
	defer os.Remove(addr2.Name)

	err = unix.Connect(connFd(c1), &unix.SockaddrUnix{Name: addr2.Name})
	if err != nil {
		return
	}

	return
}

func connFd(conn net.Conn) int {
	c := reflect.Indirect(reflect.ValueOf(conn))
	fd := reflect.Indirect(c.FieldByName("fd"))
	pfd := reflect.Indirect(fd.FieldByName("pfd"))

	return int(pfd.FieldByName("Sysfd").Int())
}

func TempUnixgramAddr(dir, pattern string) (*net.UnixAddr, error) {
	if dir == "" {
		dir = os.TempDir()
	}

	var prefix, suffix string
	if pos := strings.LastIndex(pattern, "*"); pos != -1 {
		prefix, suffix = pattern[:pos], pattern[pos+1:]
	} else {
		prefix = pattern
	}

	var name string
	var nconflict int
	for i := 0; i < 10000; i++ {
		name = filepath.Join(dir, prefix+nextRandom()+suffix)
		if _, err := os.Stat(name); err != nil {
			if nconflict++; nconflict > 10 {
				randmu.Lock()
				randx = reseed()
				randmu.Unlock()
			}
			continue
		}
		break
	}
	return net.ResolveUnixAddr("unixgram", name)
}

var randx uint32
var randmu sync.Mutex

func reseed() uint32 {
	return uint32(time.Now().UnixNano() + int64(os.Getpid()))
}

func nextRandom() string {
	randmu.Lock()
	r := randx
	if r == 0 {
		r = reseed()
	}
	r = r*1664525 + 1013904223 // constants from Numerical Recipes
	randx = r
	randmu.Unlock()
	return strconv.Itoa(int(1e9 + r%1e9))[1:]
}
