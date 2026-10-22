package helper

/*
#include <launch.h>
#include <stdlib.h>

static int tl_activate(const char *name, int *fd) {
	int *fds = NULL;
	size_t cnt = 0;
	int err = launch_activate_socket(name, &fds, &cnt);
	if (err != 0) return err;
	if (cnt < 1) { free(fds); return -1; }
	*fd = fds[0];
	free(fds);
	return 0;
}
*/
import "C"

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"syscall"
	"unsafe"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"

	"github.com/mihasm/tetherly/internal/ipc"
)

const adminGID = 80

// Listen returns the socket launchd made for us (socket activation, key
// "Listener" in the job's Sockets) or, when started by hand, one it creates.
// activated says which it was.
func Listen() (l net.Listener, activated bool, err error) {
	name := C.CString("Listener")
	defer C.free(unsafe.Pointer(name))
	var fd C.int
	if C.tl_activate(name, &fd) == 0 {
		f := os.NewFile(uintptr(fd), "launchd-socket")
		l, err := net.FileListener(f)
		f.Close()
		if err == nil {
			return l, true, nil
		}
		log.Warn().Err(err).Msg("launchd socket unusable, creating our own")
	}
	_ = os.Remove(ipc.SocketPath)
	l, err = net.Listen("unix", ipc.SocketPath)
	if err != nil {
		return nil, false, err
	}
	_ = os.Chmod(ipc.SocketPath, 0o666)
	return l, false, nil
}

// Serve answers clients until the listener is closed.
func (h *Helper) Serve(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go h.serveConn(c)
	}
}

func (h *Helper) serveConn(c net.Conn) {
	defer c.Close()
	priv := privileged(c)
	r := bufio.NewReaderSize(c, 64<<10)
	enc := json.NewEncoder(c)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var req ipc.Request
			var resp ipc.Response
			if jerr := json.Unmarshal(line, &req); jerr != nil {
				resp = ipc.Response{Error: "bad request: " + jerr.Error()}
			} else {
				resp = h.Handle(req, priv)
			}
			if enc.Encode(resp) != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// privileged: root, a member of the admin group, or the console user.
func privileged(c net.Conn) bool {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var cred *unix.Xucred
	_ = raw.Control(func(fd uintptr) {
		cred, err = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	})
	if err != nil || cred == nil {
		return false
	}
	if cred.Uid == 0 {
		return true
	}
	for i := 0; i < int(cred.Ngroups) && i < len(cred.Groups); i++ {
		if cred.Groups[i] == adminGID {
			return true
		}
	}
	if st, err := os.Stat("/dev/console"); err == nil {
		if s, ok := st.Sys().(*syscall.Stat_t); ok && s.Uid == cred.Uid {
			return true
		}
	}
	return false
}
