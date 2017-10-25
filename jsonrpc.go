package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"strings"
	"sync"
)

// keys for ctx
type key string

const (
	ConnKey key = "conn"
)

var null = json.RawMessage([]byte("null"))

type msg struct {
	Id     interface{}     `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

type request struct {
	Id     uint        `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type response struct {
	Id     interface{} `json:"id"`
	Result interface{} `json:"result"`
	Error  interface{} `json:"error"`
}

type notify struct {
	Id     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params interface{}     `json:"params"`
}

type method struct {
	Func       reflect.Value
	ParamsType reflect.Type
	ReplyType  reflect.Type
	ctx        context.Context
}

type Server interface {
	Register(api interface{}) error
	ListenAndServe(string) error
	ListenAndServeWithCtx(context.Context, string) error
	Serve(net.Listener) error
	ServeWithCtx(context.Context, net.Listener) error
	ServeConn(io.ReadWriteCloser) error
	ServeConnWithCtx(context.Context, io.ReadWriteCloser) error
	Call(string, interface{}, interface{}) error
	Notify(string, interface{}) error
	Close() error
}

type server struct {
	api        reflect.Value
	methods    map[string]method
	Conn       io.ReadWriteCloser
	NewConnect func(context.Context, io.ReadWriteCloser) (context.Context, error)
	response   map[uint]chan msg
	Seq        uint
	seqMutex   *sync.Mutex
	MsgSep     byte
}

func New() *server {
	server := &server{
		NewConnect: newConnect,
		response:   make(map[uint]chan msg),
		seqMutex:   new(sync.Mutex),
		MsgSep:     10, // "\n"
	}
	return server
}

func (s *server) Register(api interface{}) error {
	if s.methods != nil {
		return errors.New("we can register only one API")
	}
	methods, err := getMethods(api)
	if err != nil {
		return err
	}
	s.api = reflect.ValueOf(api)
	s.methods = methods
	return nil
}

func NewConn(conn io.ReadWriteCloser) *server {
	return NewConnWithCtx(nil, conn)
}
func NewConnWithCtx(ctx context.Context, conn io.ReadWriteCloser) *server {
	server := New()
	server.Conn = conn
	go server.ServeConnWithCtx(ctx, conn)
	return server
}

func Dial(network, address string) (*server, error) {
	return DialWithCtx(nil, network, address)
}

func DialWithCtx(ctx context.Context, network, address string) (*server, error) {
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	return NewConnWithCtx(ctx, conn), nil
}

func getMethods(api interface{}) (methods map[string]method, err error) {
	methods = make(map[string]method)
	apiType := reflect.TypeOf(api)
	for i := 0; i < apiType.NumMethod(); i++ {
		meth := apiType.Method(i)
		name := meth.Name
		if meth.Type.NumIn() != 3 && meth.Type.NumIn() != 4 {
			err = fmt.Errorf(
				"method %s has wrong number of ins: %d",
				name, meth.Type.NumIn())
			return
		}
		inStart := meth.Type.NumIn() - 2
		methods[name] = method{
			Func:       meth.Func,
			ParamsType: meth.Type.In(inStart + 0),
			ReplyType:  meth.Type.In(inStart + 1),
		}
		// TODO change only first char also
		methods[strings.ToLower(name)] = methods[name]
	}
	return
}

func newConnect(ctx context.Context, conn io.ReadWriteCloser) (context.Context, error) {
	if ctx == nil {
		return nil, nil
	}
	if ctx.Value(ConnKey) == nil {
		return context.WithValue(ctx, ConnKey, conn), nil
	} else {
		return nil, errors.New("conn already in ctx")
	}
}

func (s *server) ListenAndServe(address string) error {
	return s.ListenAndServeWithCtx(nil, address)
}

func (s *server) ListenAndServeWithCtx(ctx context.Context, address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	return s.ServeWithCtx(ctx, listener)
}

func (s *server) Serve(listener net.Listener) error {
	return s.ServeWithCtx(nil, listener)
}

func (s *server) ServeWithCtx(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			// TODO handle some not crit error
			return err
		}
		go s.ServeConnWithCtx(ctx, conn)
	}
	return nil
}

// call api method, this function is called from ServeConn()
func (s *server) callMethod(ctx context.Context, ctxChan chan context.Context, conn io.ReadWriteCloser, method method, data msg) {
	params := reflect.New(method.ParamsType)
	if err := json.Unmarshal(data.Params, params.Interface()); err != nil {
		s.sendError(conn, data, err.Error())
		return
	}
	reply := reflect.New(method.ReplyType.Elem())
	var ret []reflect.Value
	if ctx != nil {
		ret = method.Func.Call([]reflect.Value{s.api,
			reflect.ValueOf(ctx),
			reflect.Indirect(params), reply})
	} else {
		ret = method.Func.Call([]reflect.Value{s.api,
			reflect.Indirect(params), reply})
	}
	var retErr reflect.Value
	if len(ret) == 1 {
		retErr = ret[0]
	} else if len(ret) == 2 {
		retErr = ret[1]
		ctxNew := ret[0].Interface().(context.Context)
		ctxChan <- ctxNew
	}
	if err := retErr.Interface(); err != nil {
		err := err.(error)
		s.sendError(conn, data, err.Error())
		return
	}
	if data.Id != nil {
		reply = reply.Elem()
		buf, err := json.Marshal(response{
			Id:     data.Id,
			Result: reply.Interface(),
			Error:  null,
		})
		if err != nil {
			s.sendError(conn, data, err.Error())
			return
		}
		conn.Write(append(buf, s.MsgSep))
	}
}

func (s *server) ServeConn(conn io.ReadWriteCloser) error {
	return s.ServeConnWithCtx(nil, conn)
}

func (s *server) ServeConnWithCtx(ctx context.Context, conn io.ReadWriteCloser) error {
	ctx, err := s.NewConnect(ctx, conn)
	if err != nil {
		return err
	}
	decChan := s.getJsonDecoder(conn)
	ctxChan := make(chan context.Context)
	for {
		select {
		case ctx = <-ctxChan:
		case data, ok := <-decChan:
			if !ok {
				if conn == s.Conn {
					data.Error = "conn closed"
					for id := range s.response {
						s.response[id] <- data
					}
				}
				return errors.New("decChan closed")
			}
			if data.Method == "" {
				// Response
				if data.Id == nil {
					log.Println("wrong response", data)
					continue
				}
				id, ok := data.Id.(float64)
				if !ok {
					log.Println("wrong response", data)
					continue
				}
				responseChan := s.response[uint(id)]
				if responseChan == nil {
					log.Println("no receiver for response", data)
					continue
				}
				responseChan <- data
			} else {
				// Request
				funcParts := strings.SplitN(data.Method, ".", 2)
				funcName := strings.Replace(funcParts[1], ".", "_", -1)
				method, ok := s.methods[funcName]
				if ok {
					go s.callMethod(ctx, ctxChan, conn, method, data)
				} else {
					s.sendError(conn, data,
						"rpc: can't find method "+funcName)
				}
			}
		}
	}
}

func (s *server) getJsonDecoder(conn io.ReadWriteCloser) <-chan msg {
	out := make(chan msg)
	go func() {
		dec := json.NewDecoder(conn)
		for {
			var data msg
			err := dec.Decode(&data)
			if err != nil {
				_, ok := err.(*json.UnmarshalTypeError)
				if ok {
					log.Printf("%v %T", err, err)
					continue
				}
				close(out)
				return
			}
			out <- data
		}
	}()
	return out
}

func (s *server) sendError(conn io.ReadWriteCloser, data msg, errmsg string) {
	buf, err := json.Marshal(response{
		Id:     data.Id,
		Result: null,
		Error:  errmsg,
	})
	if err != nil {
		log.Fatal(err)
		return
	}
	conn.Write(append(buf, s.MsgSep))
}

func (s *server) Call(method string, args interface{}, reply interface{}) error {
	s.seqMutex.Lock()
	for {
		_, ok := s.response[s.Seq]
		if !ok {
			break
		}
		s.Seq++
	}
	id := s.Seq
	s.Seq++
	s.response[id] = make(chan msg)
	s.seqMutex.Unlock()
	req := request{
		Id:     id,
		Method: method,
		Params: args,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := s.Conn.Write(append(data, s.MsgSep)); err != nil {
		return err
	}
	response := <-s.response[id]
	delete(s.response, id)
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if err := json.Unmarshal(response.Result, reply); err != nil {
		return err
	}
	return nil
}

func (s *server) Notify(method string, args interface{}) error {
	req := notify{
		Id:     null,
		Method: method,
		Params: args,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := s.Conn.Write(append(data, s.MsgSep)); err != nil {
		return err
	}
	return nil
}

func (s *server) Close() error {
	if s.Conn != nil {
		return s.Conn.Close()
	}
	return nil
}
