package subslicer

import (
	"net"
	"os"
)

// TODO(dzeromsk): add support for context

type UNIXServer struct {
	addr    *net.UnixAddr
	ln      *net.UnixListener
	handler func(conn net.Conn)
}

func NewUNIXServer(addr *net.UnixAddr, handler func(conn net.Conn)) (s *UNIXServer, err error) {
	s = new(UNIXServer)
	s.addr = addr
	s.handler = handler
	s.ln, err = net.ListenUnix("unix", addr)
	if err != nil {
		return
	}
	return
}

func (s *UNIXServer) Close() (err error) {
	if err2 := os.Remove(s.addr.Name); err2 != nil {
		err = err2
	}
	if err2 := s.ln.Close(); err2 != nil {
		err = err2
	}
	return
}

func (s *UNIXServer) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go s.handler(conn)
	}
}

type TCPServer struct {
	addr    string
	ln      net.Listener
	handler func(conn net.Conn)
}

func NewTCPServer(addr string, handler func(conn net.Conn)) (s *TCPServer, err error) {
	s = new(TCPServer)
	s.addr = addr
	s.handler = handler
	s.ln, err = net.Listen("tcp", addr)
	if err != nil {
		return
	}
	return
}

func (s *TCPServer) Close() (err error) {
	return s.ln.Close()
}

func (s *TCPServer) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go s.handler(conn)
	}
}

type UDPServer struct {
	addr    string
	conn    net.PacketConn
	handler func(data []byte)
}

func NewUDPServer(addr string, handler func(data []byte)) (s *UDPServer, err error) {
	s = new(UDPServer)
	s.addr = addr
	s.handler = handler
	s.conn, err = net.ListenPacket("udp", addr)
	if err != nil {
		return
	}
	return
}

func (s *UDPServer) Close() (err error) {
	return s.conn.Close()
}

func (s *UDPServer) Serve() error {
	data := make([]byte, 4096)
	for {
		_, _, err := s.conn.ReadFrom(data)
		if err != nil {
			return err
		}
		go s.handler(data)
	}
}
