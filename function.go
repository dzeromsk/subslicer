package subslicer

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/dzeromsk/subslicer/freezer"
	nsjailpb "github.com/dzeromsk/subslicer/freezer/pb"

	"github.com/golang/protobuf/proto"
)

type Function struct {
	*freezer.Freezer
	Handler string
	Dir     string
	User    string
	Group   string

	shmem   *shmem
	control ControlConn
	runtime *Runtime
}

type Runtime struct {
	Name string

	ConsoleAddr *net.UnixAddr
	LogsAddr    *net.UnixAddr
	Cmd         string
	Args        []string
	User        string
	Group       string
	Chroot      string
}

const (
	shmemName = "slicershmem1"
)

func NewFunction(r Runtime, dir, handler string) (f *Function, err error) {
	f = new(Function)
	f.runtime = &r
	f.Handler = handler
	f.User = r.User
	f.Group = r.Group

	f.Dir, err = filepath.Abs(dir)
	if err != nil {
		return
	}

	f.shmem, err = NewShmem(shmemName)
	if err != nil {
		return
	}

	// Control
	var client *net.UnixConn
	var server *net.UnixConn
	client, server, err = UnixgramPair()
	if err != nil {
		return
	}
	defer client.Close()

	// Bootstrap
	f.Freezer, err = freezer.NewFreezer(r.Cmd, r.Args...)
	if err != nil {
		return
	}
	f.Chroot = r.Chroot

	console, err := net.DialUnix("unix", nil, r.ConsoleAddr)
	if err != nil {
		return
	}
	defer console.Close()

	logs, err := net.DialUnix("unix", nil, r.LogsAddr)
	if err != nil {
		return
	}
	defer logs.Close()

	for _, file := range []struct {
		name string
		*os.File
	}{
		{"_LAMBDA_CONTROL_SOCKET", file(client)},
		{"_LAMBDA_CONSOLE_SOCKET", file(console)},
		{"_LAMBDA_LOG_FD", file(logs)},
		{"_LAMBDA_SHARED_MEM_FD", file(f.shmem)},
	} {
		f.ExtraFiles = append(f.ExtraFiles, file.File)
		f.Env = append(f.Env,
			fmt.Sprintf("%s=%d", file.name, file.Fd()),
		)
	}

	f.Env = append(f.Env,
		"_HANDLER="+f.Handler,

		"AWS_LAMBDA_FUNCTION_NAME=test",
		"_X_AMZN_TRACE_ID=Parent=4631f93d66676d9e",
		"_LAMBDA_RUNTIME_LOAD_TIME=10746081534797",
		"_LAMBDA_SB_ID=0",

		"_AWS_XRAY_DAEMON_ADDRESS=127.0.0.1",     // ip
		"_AWS_XRAY_DAEMON_PORT=9090",             // port
		"AWS_XRAY_DAEMON_ADDRESS=127.0.0.1:9090", // ip:port
		"AWS_XRAY_CONTEXT_MISSING=ERROR",

		"AWS_DEFAULT_REGION=not implemented",
		"AWS_EXECUTION_ENV=not implemented",
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE=not implemented",
		"AWS_LAMBDA_FUNCTION_VERSION=not implemented",
		"AWS_LAMBDA_LOG_GROUP_NAME=not implemented",
		"AWS_LAMBDA_LOG_STREAM_NAME=not implemented",
		"AWS_LAMBDA_RUNTIME_API=not implemented",
		"AWS_REGION=not implemented",

		"LAMBDA_TASK_ROOT=/var/task",
		"LAMBDA_RUNTIME_DIR=/var/runtime",
		"LANG=en_US.UTF-8",
		"LD_LIBRARY_PATH=/var/lang/lib:/lib64:/usr/lib64:/var/runtime:/var/runtime/lib:/var/task:/var/task/lib",
		"PATH=/var/lang/bin:/usr/local/bin:/usr/bin/:/bin",
		"PYTHONPATH=/tmp/:/var/task/:/var/runtime/",
		"TZ=:UTC",
		"LOG_LEVEL=DEBUG",
	)

	f.Configure = f.configure()
	f.Stdout = os.Stdout
	f.Stderr = os.Stderr

	if err := f.Start(); err != nil {
		f.Close()
		return nil, err
	}

	go func() {
		f.Wait()
		client.Close()
		server.Close()
	}()

	args := map[string]string{
		"invokeid":    fakeGuid(), //strconv.Itoa(f.invokeid),
		"handler":     f.Handler,
		"mode":        "event",
		"supressinit": "0", // int
		"awskey":      "not implemented",
		"awssecret":   "not implemented",
		"awssession":  "not implemented",
	}

	f.control = ControlConn{UnixConn: server}
	if err := f.control.init(args); err != nil {
		f.Close()
		return nil, err
	}

	// f.Stdout = nil
	// f.Stderr = nil

	return
}

func (f *Function) Invoke(ctx context.Context) error {
	start := time.Now()
	id := fakeGuid()

	args := map[string]string{
		"invokeid":           id,  //strconv.Itoa(f.invokeid),
		"needdebuglogs":      "1", // if 0 shmem is different?
		"deadlinens":         "0",
		"mode":               "event",
		"clientcontext":      "{}",
		"x-amzn-trace-id":    "x=1",
		"invokedFunctionArn": "not implemented",
		"awskey":             "not implemented",
		"awssecret":          "not implemented",
		"awssession":         "not implemented",
		"cognitoidentityid":  "not implemented",
		"cognitopoolid":      "not implemented",
	}

	fmt.Println("START RequestId:", id, "Version: $LATEST")

	// run!
	err := f.control.Invoke(ctx, args)

	d := duration(start)
	fmt.Printf(
		"REPORT RequestId: %s\tDuration: %.2f ms\t Billed Duration: %.f ms\tMemory Size: %s MB\tMax Memory Used: %d MB\n",
		id, d, math.Ceil(d/100)*100, "1024", -1,
	)
	fmt.Println("END RequestId:", id)
	return err
}

func duration(start time.Time) float64 {
	d := float64(time.Now().Sub(start).Nanoseconds())
	return d / 1e6
}

func (fn *Function) configure() func(f *freezer.Freezer) *nsjailpb.NsJailConfig {
	chroot := filepath.Clean(fn.Chroot)
	// runtimeChroot := fmt.Sprintf("%s/../lambda-%s/", chroot, fn.runtime.Name)

	mounts := []*nsjailpb.MountPt{
		{
			Src:    proto.String(chroot),
			Dst:    proto.String("/"),
			IsBind: proto.Bool(true),
			Rw:     proto.Bool(false),
			IsDir:  proto.Bool(true),
		},
		{
			Fstype:  proto.String("tmpfs"),
			IsBind:  proto.Bool(false),
			Options: proto.String("size=536870912"),

			// Src:    proto.String(filepath.Join(chroot, "tmp")),
			// IsBind: proto.Bool(true),

			Dst: proto.String("/tmp"),

			IsDir: proto.Bool(true),
			Rw:    proto.Bool(true),
		},
		{
			Src:    proto.String(fn.Dir),
			Dst:    proto.String("/var/task"),
			IsBind: proto.Bool(true),
			Rw:     proto.Bool(false),
			IsDir:  proto.Bool(true),
		},
		{
			Src:    proto.String("/dev/urandom"),
			Dst:    proto.String("/dev/urandom"),
			IsBind: proto.Bool(true),
		},
		{
			Src:    proto.String("/dev/random"),
			Dst:    proto.String("/dev/random"),
			IsBind: proto.Bool(true),
		},
		{
			Src:    proto.String("/dev/zero"),
			Dst:    proto.String("/dev/zero"),
			IsBind: proto.Bool(true),
		},
		{
			Src:    proto.String("/dev/null"),
			Dst:    proto.String("/dev/null"),
			IsBind: proto.Bool(true),
		},
		// {
		// 	Src:    proto.String("/dev"),
		// 	Dst:    proto.String("/dev"),
		// 	IsBind: proto.Bool(true),
		// 	Rw:     proto.Bool(false),
		// 	IsDir:  proto.Bool(true),
		// },
	}

	// lang := filepath.Join(runtimeChroot, "var/lang")
	// rapid := filepath.Join(runtimeChroot, "var/rapid")
	// runtime := filepath.Join(runtimeChroot, "var/runtime")

	// if _, err := os.Stat(lang); err == nil {
	// 	mounts = append(mounts, &nsjailpb.MountPt{
	// 		Src:    proto.String(lang),
	// 		Dst:    proto.String("/var/lang"),
	// 		IsBind: proto.Bool(true),
	// 		Rw:     proto.Bool(false),
	// 		IsDir:  proto.Bool(true),
	// 	})
	// }

	// if _, err := os.Stat(rapid); err == nil {
	// 	mounts = append(mounts, &nsjailpb.MountPt{
	// 		Src:    proto.String(rapid),
	// 		Dst:    proto.String("/var/rapid"),
	// 		IsBind: proto.Bool(true),
	// 		Rw:     proto.Bool(false),
	// 		IsDir:  proto.Bool(true),
	// 	})
	// }

	// if _, err := os.Stat(runtime); err == nil {
	// 	mounts = append(mounts, &nsjailpb.MountPt{
	// 		Src:    proto.String(runtime),
	// 		Dst:    proto.String("/var/runtime"),
	// 		IsBind: proto.Bool(true),
	// 		Rw:     proto.Bool(false),
	// 		IsDir:  proto.Bool(true),
	// 	})
	// }

	return func(f *freezer.Freezer) *nsjailpb.NsJailConfig {
		var passFd []int32
		for _, f := range f.ExtraFiles {
			passFd = append(passFd, int32(f.Fd()))
		}
		config := &nsjailpb.NsJailConfig{
			// ChrootDir: proto.String(chroot),
			// Mode:  nsjailpb.Mode_EXECVE.Enum(),
			Mount: mounts,
			Uidmap: []*nsjailpb.IdMap{{
				InsideId:  proto.String("root"),
				OutsideId: proto.String(fn.User),
			}},
			Gidmap: []*nsjailpb.IdMap{{
				InsideId:  proto.String("root"),
				OutsideId: proto.String(fn.Group),
			}},
			Cwd:             proto.String("/var/task"),
			MountProc:       proto.Bool(true),
			Envar:           f.Env,
			PassFd:          passFd,
			Hostname:        proto.String("slicer"),
			LogLevel:        nsjailpb.LogLevel_WARNING.Enum(),
			RlimitAsType:    nsjailpb.RLimit_INF.Enum(),
			RlimitFsizeType: nsjailpb.RLimit_INF.Enum(),
			RlimitCpuType:   nsjailpb.RLimit_INF.Enum(),
			// RlimitNprocType:  nsjailpb.RLimit_INF.Enum(),
			RlimitNofile:     proto.Uint64(1024),
			RlimitNofileType: nsjailpb.RLimit_SOFT.Enum(),
			TimeLimit:        proto.Uint32(0),
			// CgroupMemMax:    proto.Uint64(3 * 1024 * 1024),
			CloneNewnet:   proto.Bool(false),
			SeccompString: seccomp,
		}
		return config
	}
}

func (f *Function) Close() (err error) {
	files := []io.Closer{f.Freezer, f.shmem, f.control}
	for _, f := range files {
		if err2 := f.Close(); err2 != nil {
			err = err2
		}
	}
	if err2 := f.Process.Kill(); err2 != nil {
		err = err2
	}
	if err2 := f.Thaw(); err2 != nil {
		err = err2
	}
	// runtime.SetFinalizer(f, nil)
	return err
}

func (f *Function) Reset() {
	f.shmem.Reset()
}

// func (f *Function) ReadFrom(r io.Reader) (int64, error) {
// 	return f.shmem.ReadFrom(r)
// }

func (f *Function) Write(data []byte) (int, error) {
	return f.shmem.Write(data)
}

func (f *Function) Debug() []byte {
	return f.shmem.Debug()
}

func (f *Function) Response() []byte {
	return f.shmem.Response()
}

type FunctionPool struct {
	New func() (f *Function, err error)

	m            sync.Mutex
	freeFunction []*Function
}

func (p *FunctionPool) Get() (f *Function, err error) {
	p.m.Lock()
	defer p.m.Unlock()
	if n := len(p.freeFunction); n > 0 {
		f = p.freeFunction[n-1]
		p.freeFunction = p.freeFunction[:n-1]
		return
	}
	return p.New()
}

func (p *FunctionPool) Put(f *Function) {
	p.m.Lock()
	p.freeFunction = append(p.freeFunction, f)
	p.m.Unlock()
}

func (p *FunctionPool) Purge() error {
	p.m.Lock()
	defer p.m.Unlock()
	for n := len(p.freeFunction); n > 0; n-- {
		f := p.freeFunction[n-1]
		p.freeFunction = p.freeFunction[:n-1]
		if err := f.Close(); err != nil {
			return err
		}
	}

	return nil
}

type filer interface {
	File() (f *os.File, err error)
}

func file(c filer) *os.File {
	f, _ := c.File()
	fd := int(f.Fd())

	// set nonblock
	flag, _ := fcntl(fd, syscall.F_GETFL, 0)
	flag |= syscall.O_NONBLOCK
	fcntl(fd, syscall.F_SETFL, flag)

	// clear cloexec
	flag, _ = fcntl(fd, syscall.F_SETFD, 0)
	flag &^= syscall.FD_CLOEXEC
	fcntl(fd, syscall.F_SETFD, flag)

	return f
}

func fcntl(fd int, cmd int, arg int) (val int, err error) {
	r0, _, e1 := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(cmd), uintptr(arg))
	val = int(r0)
	if e1 != 0 {
		err = e1
	}
	return
}

func fakeGuid() string {
	randBuf := make([]byte, 16)
	rand.Read(randBuf)

	hexBuf := make([]byte, hex.EncodedLen(len(randBuf))+4)

	hex.Encode(hexBuf[0:8], randBuf[0:4])
	hexBuf[8] = '-'
	hex.Encode(hexBuf[9:13], randBuf[4:6])
	hexBuf[13] = '-'
	hex.Encode(hexBuf[14:18], randBuf[6:8])
	hexBuf[18] = '-'
	hex.Encode(hexBuf[19:23], randBuf[8:10])
	hexBuf[23] = '-'
	hex.Encode(hexBuf[24:], randBuf[10:])

	hexBuf[14] = '1' // Make it look like a v1 guid

	return string(hexBuf)
}

var seccomp = []string{
	"DENY {",
	"  getpgid,",
	"  getpgrp,",
	"  getsid",
	"}",
	"ERRNO(1) {",
	"  acct,",
	"  add_key,",
	"  bpf,",
	"  capset,",
	"  chroot,",
	"  delete_module,",
	"  fallocate,",
	"  fanotify_init,",
	"  fchmod,",
	"  fchown,",
	"  finit_module,",
	"  init_module,",
	"  ioperm,",
	"  ioprio_set,",
	"  kexec_file_load,",
	"  kexec_load,",
	"  keyctl,",
	"  lookup_dcookie,",
	"  mbind,",
	"  migrate_pages,",
	"  mincore,",
	"  mount,",
	"  move_pages,",
	"  open_by_handle_at,",
	"  perf_event_open,",
	"  pivot_root,",
	"  prctl,",
	"  ptrace,",
	"  quotactl,",
	"  reboot,",
	"  request_key,",
	"  restart_syscall,",
	"  seccomp,",
	"  setdomainname,",
	"  setgid,",
	"  setgroups,",
	"  sethostname,",
	"  set_mempolicy,",
	"  setns,",
	"  setregid,",
	"  setresgid,",
	"  setresuid,",
	"  setreuid,",
	"  settimeofday,",
	"  setuid,",
	"  swapoff,",
	"  swapon,",
	"  sysctl,",
	"  syslog,",
	"  umount,",
	"  unshare,",
	"  vhangup",
	"}",
	"ERRNO(38) {",
	"  afs_syscall,",
	"  create_module,",
	"  epoll_ctl_old,",
	"  epoll_wait_old,",
	"  get_kernel_syms,",
	"  getpmsg,",
	"  get_thread_area,",
	"  kcmp,",
	"  nfsservctl,",
	"  putpmsg,",
	"  query_module,",
	"  security,",
	"  set_thread_area,",
	"  tuxcall,",
	"  uselib,",
	"  vserver",
	"}",
	"DEFAULT ALLOW",
}
