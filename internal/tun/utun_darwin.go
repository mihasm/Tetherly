package tun

import (
	"fmt"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/unix"
)

// utun configuration constants for macOS
const (
	AF_SYSTEM         = 32
	SYSPROTO_CONTROL  = 2
	AF_SYS_CONTROL    = 2
	UTUN_CONTROL_NAME = "com.apple.net.utun_control"
)

// utunInterface implements Interface for Darwin using AF_SYSTEM.
type utunInterface struct {
	f    *os.File
	name string
}

func (i *utunInterface) Read(p []byte) (n int, err error) {
	return i.f.Read(p)
}

func (i *utunInterface) Write(p []byte) (n int, err error) {
	return i.f.Write(p)
}

// Close destroys the utun interface. Routes and DNS are owned by the netcfg
// package and removed there before the interface goes away.
func (i *utunInterface) Close() error {
	return i.f.Close()
}

func (i *utunInterface) Name() string {
	return i.name
}

// Configure sets the IP addresses and MTU for the utun interface using the 'ifconfig' command.
func (i *utunInterface) Configure(localIP, remoteIP, mtu string) error {
	// Formula: ifconfig <name> <local> <remote> mtu <val> up
	cmd := exec.Command("ifconfig", i.name, localIP, remoteIP, "mtu", mtu, "up")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig failed: %w (output: %s)", err, string(out))
	}
	return nil
}

// OpenUTUN creates a new utun interface on macOS.
// If index is 0, the system chooses the first available (utun0, utun1, etc.).
func OpenUTUN(index int) (Interface, error) {
	fd, err := unix.Socket(AF_SYSTEM, unix.SOCK_DGRAM, SYSPROTO_CONTROL)
	if err != nil {
		return nil, fmt.Errorf("utun: failed to open system socket: %w", err)
	}

	// 1. Find the control ID for "com.apple.net.utun_control"
	info := struct {
		ctl_id   uint32
		ctl_name [96]byte
	}{}
	copy(info.ctl_name[:], UTUN_CONTROL_NAME)

	// CTLIOCGINFO
	err = ioctl(fd, 0xc0644e03, unsafe.Pointer(&info))
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("utun: failed to get utun control info: %w", err)
	}

	// 2. Connect to the utun control
	sc := struct {
		sc_len      uint8
		sc_family   uint8
		ss_sysaddr  uint16
		sc_id       uint32
		sc_unit     uint32
		sc_reserved [5]uint32
	}{
		sc_len:     32,
		sc_family:  AF_SYSTEM,
		ss_sysaddr: AF_SYS_CONTROL,
		sc_id:      info.ctl_id,
		sc_unit:    uint32(index), // 0 = automatic
	}

	err = connect(fd, unsafe.Pointer(&sc), 32)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("utun: failed to connect to utun control: %w", err)
	}

	// 3. Get the interface name (e.g., utun3)
	nameBuf := make([]byte, 64)
	nameLen := uint32(len(nameBuf))
	// UTUN_OPT_IFNAME (Option 2)
	err = getsockopt(fd, SYSPROTO_CONTROL, 2, unsafe.Pointer(&nameBuf[0]), &nameLen)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("utun: failed to get interface name: %w", err)
	}

	ifname := string(nameBuf[:nameLen-1]) // trim null byte

	// Non-blocking so the Go poller owns the fd: Close then really interrupts a
	// pending Read instead of leaving the interface (and a goroutine) behind.
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("utun: failed to set non-blocking: %w", err)
	}
	return &utunInterface{
		f:    os.NewFile(uintptr(fd), ifname),
		name: ifname,
	}, nil
}

// Wrapper for unix.Ioctl
func ioctl(fd int, request uintptr, argp unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(argp))
	if errno != 0 {
		return errno
	}
	return nil
}

// Wrapper for unix.Connect
func connect(fd int, addr unsafe.Pointer, len uint32) error {
	_, _, errno := unix.Syscall(unix.SYS_CONNECT, uintptr(fd), uintptr(addr), uintptr(len))
	if errno != 0 {
		return errno
	}
	return nil
}

// Wrapper for unix.Getsockopt
func getsockopt(fd int, level, name int, val unsafe.Pointer, len *uint32) error {
	_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, uintptr(fd), uintptr(level), uintptr(name), uintptr(val), uintptr(unsafe.Pointer(len)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}
