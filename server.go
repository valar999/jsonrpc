package jsonrpc

import (
	"net"
)

type APIFactory interface {
	NewConn(conn *Conn) interface{}
}

type Server struct {
	apiFactory APIFactory
}

func NewServer(api APIFactory) *Server {
	return &Server{
		apiFactory: api,
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
	defer listener.Close()
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
		go c.Serve()
	}
}
