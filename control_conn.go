package subslicer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log"
	"net"
)

type ControlConn struct {
	*net.UnixConn
	state state
}

var (
	errControlFault   = errors.New("control faulted during init")
	errUnknownCommand = errors.New("unknown command")
	errHandlerBusy    = errors.New("handler is busy")
	errInvalidMagic   = errors.New("invalid magic")
	errKVParserFailed = errors.New("kv parser failed")
)

const magic = 0x47697244

type state int

const (
	stateStarting state = iota
	stateInitializing
	stateReady
	stateProcessing
	stateFault
	stateError
)

const (
	cmdStart   = "START"
	cmdRunning = "RUNNING"
	cmdDone    = "DONE"
	cmdFault   = "FAULT"
	cmdInvoke  = "INVOKE"
	cmdError   = "ERROR"
)

func (c *ControlConn) init(args map[string]string) error {
	ctx := context.Background()

	if err := c.send(cmdStart, args); err != nil {
		return err
	}

	c.state = stateStarting
	for {
		cmd, _, err := c.receive(ctx)
		if err != nil {
			return err
		}

		switch cmd {
		case cmdRunning:
			if c.state == stateStarting {
				// todo: collect running stats
				c.state = stateInitializing
			}
		case cmdDone:
			if c.state == stateInitializing {
				// todo: freeze handler
				c.state = stateReady
				return nil
			}
		case cmdFault:
			return errControlFault
		default:
			log.Println("Unknown command:", cmd, c.state)
			return errUnknownCommand
		}
	}
}

func (c *ControlConn) Invoke(ctx context.Context, args map[string]string) error {
	if c.state != stateReady {
		return errHandlerBusy
	}

	if err := c.send(cmdInvoke, args); err != nil {
		return err
	}

	c.state = stateProcessing
	for {
		cmd, a, err := c.receive(ctx)
		if err != nil {
			return err
		}
		switch cmd {
		case cmdDone:
			if c.state == stateProcessing {
				// todo: freeze handler
				c.state = stateReady
				return nil
			}
		case cmdError:
			log.Println("ERROR details:", a)
			c.state = stateError
			return nil
		case cmdFault:
			log.Println("ERROR details:", a)
			c.state = stateFault
			return nil
			// todo: handle handler faults
		default:
			log.Println("Unknown commandYYY:", cmd, c.state)
			return errUnknownCommand
		}
	}
}

func (c *ControlConn) send(cmd string, args map[string]string) error {
	var body bytes.Buffer
	for k, v := range args {
		body.Write([]byte(k))
		body.Write([]byte("\x00"))
		body.Write([]byte(v))
		body.Write([]byte("\x00"))
	}
	msg := make([]byte, 16+body.Len())
	binary.BigEndian.PutUint32(msg[0:], magic)
	binary.BigEndian.PutUint32(msg[4:], uint32(body.Len()))
	copy(msg[8:], cmd)
	copy(msg[16:], body.Bytes())
	_, err := c.Write(msg)
	return err
}

func (c *ControlConn) receive(ctx context.Context) (string, map[string]string, error) {
	msg := make([]byte, 4096)
	n, addr, err := c.ReadFrom(msg)
	if err != nil {
		return "", nil, err
	}
	_ = addr
	_ = n
	// log.Printf("packet-received: bytes=%d from=%s", n, addr.String())
	m := binary.BigEndian.Uint32(msg[0:4])

	if m != magic {
		return "", nil, errInvalidMagic
	}
	end := bytes.IndexByte(msg[8:16], 0)
	cmd := string(msg[8 : 8+end])

	var args map[string]string
	sz := binary.BigEndian.Uint32(msg[4:8])
	// log.Println("received", cmd, len(cmd))

	if sz > 0 {
		kv := bytes.Split(msg[16:16+sz], []byte("\x00"))
		kv = kv[:len(kv)-1]

		if len(kv)%2 != 0 {
			return "", nil, errKVParserFailed
		}
		args = map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			args[string(kv[i])] = string(kv[i+1])
		}
	}
	return cmd, args, nil
}
