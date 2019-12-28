package freezer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	nsjailpb "github.com/dzeromsk/subslicer/freezer/pb"

	"github.com/golang/protobuf/proto"
	"github.com/justincormack/go-memfd"
)

var (
	FreezerDir = "/sys/fs/cgroup/freezer"
	MemoryDir  = "/sys/fs/cgroup/memory"
)

type Freezer struct {
	*exec.Cmd

	Name      string
	Chroot    string
	Configure func(*Freezer) *nsjailpb.NsJailConfig

	nsjail     *memfd.Memfd
	wrapper    *memfd.Memfd
	config     *memfd.Memfd
	state      *os.File
	freezerDir string
}

func NewFreezer(name string, arg ...string) (*Freezer, error) {
	return NewSandboxContext(context.Background(), name, arg...)
}

func NewSandboxContext(ctx context.Context, name string, arg ...string) (f *Freezer, err error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}

	// if filepath.Base(name) == name {
	// 	if _, err := exec.LookPath(name); err != nil {
	// 		return nil, err
	// 	}
	// }

	// ctx is hidden in Cmd struct
	f = &Freezer{
		Cmd: exec.CommandContext(ctx, "/dev/null", arg...),
	}
	f.Name = name

	f.freezerDir, err = ioutil.TempDir(FreezerDir, "gofreezer")
	if err != nil {
		return nil, err
	}

	tasks := filepath.Join(f.freezerDir, "tasks")
	state := filepath.Join(f.freezerDir, "freezer.state")

	f.state, err = os.OpenFile(state, os.O_WRONLY, 0666)
	if err != nil {
		return nil, err
	}

	f.config, err = memfd.CreateNameFlags("freezer:config", memfd.Cloexec)
	if err != nil {
		return nil, err
	}

	f.nsjail, err = createNsjail()
	if err != nil {
		return nil, err
	}

	f.wrapper, err = createWrapper(tasks, procPath(f.nsjail.File))
	if err != nil {
		return nil, err
	}

	f.Configure = configure

	// runtime.SetFinalizer(f, (*Freezer).Close)

	return f, nil
}

func (f *Freezer) Close() error {
	// TODO(dzeromsk): multierr or something
	var err error
	if err2 := f.Thaw(); err2 != nil {
		err = err2
	}
	files := []io.Closer{f.config, f.wrapper, f.state, f.nsjail}
	for _, f := range files {
		if err2 := f.Close(); err2 != nil {
			err = err2
		}
	}
	if err2 := os.Remove(f.freezerDir); err2 != nil {
		// TOOD(dzeromsk): proper retry + backoff, or poll
		time.Sleep(500 * time.Millisecond)
		if err3 := os.Remove(f.freezerDir); err3 != nil {
			err = err3
		} else {
			// err = err2
		}
	}
	// runtime.SetFinalizer(f, nil)
	return err
}

func (f *Freezer) Run() error {
	if err := f.Start(); err != nil {
		return err
	}
	return f.Wait()
}

func (f *Freezer) Start() error {
	if f.Process != nil {
		return errors.New("freezer: already started")
	}
	// serialize nsjail config to file
	if err := proto.MarshalText(f.config, f.Configure(f)); err != nil {
		return err
	}

	// run command in nsjail
	f.Args = append([]string{
		"nsjail", "--quiet", "--config", procPath(f.config.File), "--", f.Name,
	}, f.Args[1:]...)

	// we call nsjail via wrapper with cgroup freezer
	f.Path = procPath(f.wrapper.File)
	f.Dir = ""
	f.Env = nil

	return f.Cmd.Start()
}

func (f *Freezer) Freeze() error {
	_, err := f.state.WriteString("FROZEN")
	return err
}

func (f *Freezer) Thaw() error {
	_, err := f.state.WriteString("THAWED")
	return err
}

func configure(f *Freezer) *nsjailpb.NsJailConfig {
	if f.Chroot == "" {
		f.Chroot = "/"
	}
	if f.Dir == "" {
		f.Dir = "/"
	}
	var passFd []int32
	for _, f := range f.ExtraFiles {
		passFd = append(passFd, int32(f.Fd()))
	}
	config := &nsjailpb.NsJailConfig{
		Mount: []*nsjailpb.MountPt{
			{
				Src:    proto.String(f.Chroot),
				Dst:    proto.String("/"),
				IsBind: proto.Bool(true),
				Rw:     proto.Bool(false),
				IsDir:  proto.Bool(true),
			},
			{
				Src:    proto.String("/dev/urandom"),
				Dst:    proto.String("/dev/urandom"),
				IsBind: proto.Bool(true),
			},
		},
		Uidmap: []*nsjailpb.IdMap{{
			InsideId:  proto.String("root"),
			OutsideId: proto.String("nobody"),
		}},
		Gidmap: []*nsjailpb.IdMap{{
			InsideId:  proto.String("root"),
			OutsideId: proto.String("nogroup"),
		}},
		Cwd:             proto.String(f.Dir),
		MountProc:       proto.Bool(true),
		Envar:           f.Env,
		PassFd:          passFd,
		Hostname:        proto.String("freezer"),
		LogLevel:        nsjailpb.LogLevel_WARNING.Enum(),
		RlimitAsType:    nsjailpb.RLimit_INF.Enum(),
		RlimitFsizeType: nsjailpb.RLimit_INF.Enum(),

		CloneNewnet: proto.Bool(false),
	}
	return config
}

func procPath(f *os.File) string {
	return fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), f.Fd())
}

const wrapperTemp = `#!/bin/bash
set -e
echo $$ > %s
exec -c -a nsjail %s "$@"
`

func createWrapper(tasks, nsjail string) (*memfd.Memfd, error) {
	f, err := memfd.CreateNameFlags("freezer:wrapper", memfd.Cloexec)
	if err != nil {
		return nil, err
	}
	_, err = f.WriteString(fmt.Sprintf(wrapperTemp, tasks, nsjail))
	if err != nil {
		return nil, err
	}
	return f, nil
}

func createNsjail() (*memfd.Memfd, error) {
	f, err := memfd.CreateNameFlags("freezer:nsjail", memfd.Cloexec)
	if err != nil {
		return nil, err
	}
	data, err := Asset("nsjail")
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		return nil, err
	}
	return f, nil
}
