package jsonrpc

import (
	"net"
	"sync"
)

type APIFactory interface {
	NewConn(conn *Conn) interface{}
}

type Server struct {
	sync.Mutex
	apiFactory APIFactory
	listeners  map[net.Listener]bool
	conns      map[*Conn]bool
}

func NewServer(api APIFactory) *Server {
	return &Server{
		apiFactory: api,
		listeners:  make(map[net.Listener]bool),
		conns:      make(map[*Conn]bool),
	}
}

func (s *Server) ListenAndServe(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	return s.Serve(listener)
}

func (s *Server) Serve(listener net.Listener) error {
	s.Lock()
	s.listeners[listener] = true
	s.Unlock()
	defer func() {
		listener.Close()
		s.Lock()
		delete(s.listeners, listener)
		s.Unlock()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			// TODO handle some not crit error
			return err
		}
		c := NewConn(conn)
		api := s.apiFactory.NewConn(c)
		if err := c.Register(api); err != nil {
			return err
		}
		s.Lock()
		s.conns[c] = true
		s.Unlock()
		go func() {
			c.Serve()
			s.Lock()
			delete(s.conns, c)
			s.Unlock()
		}()
	}
}

func (s *Server) Close() error {
	s.Lock()
	for listener := range s.listeners {
		if err := listener.Close(); err != nil {
			return err
		}
	}
	for conn := range s.conns {
		if err := conn.Close(); err != nil {
			return err
		}
	}
	s.Unlock()
	return nil
}
