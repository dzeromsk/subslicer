package subslicer

import (
	"encoding/binary"
	"io"
	"os"
	"syscall"

	"github.com/fabiokung/shm"
)

const (
	shmemSize = 6394536

	bodyMax   = 6291556
	bodyBegin = 8
	bodyEnd   = 8 + bodyMax

	debugMax   = 102968
	debugBegin = 8 + bodyMax
	debugEnd   = 8 + bodyMax + debugMax
)

// TODO(dzeromsk): implement proper writer!

type shmem struct {
	file *os.File
	// memfile *memfd.Memfd
	mmap []byte

	off int
}

func NewShmem(name string) (*shmem, error) {
	s, err := shm.Open(name, os.O_RDWR|os.O_CREATE, 0777)
	if err != nil {
		return nil, err
	}
	fd := int(s.Fd())
	if err := syscall.Ftruncate(fd, shmemSize); err != nil {
		return nil, err
	}
	mmap, err := syscall.Mmap(fd, 0, shmemSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	if err := shm.Unlink(name); err != nil {
		return nil, err
	}
	mmap[0] = 0
	for bp := 1; bp < len(mmap); bp *= 2 {
		copy(mmap[bp:], mmap[:bp])
	}
	return &shmem{file: s, mmap: mmap}, nil
}

func (s *shmem) Close() error {
	syscall.Munmap(s.mmap)
	return s.file.Close()
}

func (s *shmem) Reset() {
	s.off = 0
	binary.LittleEndian.PutUint32(s.mmap[0:4], 0)
	binary.LittleEndian.PutUint32(s.mmap[4:8], 0)
}

// ReadFrom reads from reader to body
func (s *shmem) ReadFrom(r io.Reader) (int64, error) {
	// TODO(dzeromsk): bounds!
	n, err := r.Read(s.mmap[bodyBegin+s.off : bodyEnd])
	s.off += n
	binary.LittleEndian.PutUint32(s.mmap[0:4], uint32(s.off))
	return int64(n), err
}

func (s *shmem) Write(data []byte) (int, error) {
	// TODO(dzeromsk): bounds!
	n := copy(s.mmap[bodyBegin+s.off:bodyEnd], data)
	s.off += n
	binary.LittleEndian.PutUint32(s.mmap[0:4], uint32(s.off))
	return n, nil
}

func (s *shmem) Debug() []byte {
	end := debugBegin + binary.LittleEndian.Uint32(s.mmap[4:8])
	if end > debugEnd {
		end = debugEnd
	}
	return s.mmap[debugBegin:end]
}

func (s *shmem) Response() []byte {
	end := bodyBegin + binary.LittleEndian.Uint32(s.mmap[debugEnd:])
	if end > bodyEnd {
		end = bodyEnd
	}
	return s.mmap[bodyBegin:end]
}

func (s *shmem) File() (*os.File, error) {
	return s.file, nil
}
