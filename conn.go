package jsonrpc

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"
)

type Conn interface {
	Serve() error
	Go(string, interface{}, interface{}, chan *Call) *Call
	Call(method string, args interface{}, reply interface{}) error
	Notify(method string, args interface{}) error
	NotifyResponse(args interface{}) error
	Register(api interface{}) error
	RemoteAddr() string
	Synchronous(funcName string, value bool)
	Close() error
	Closed() bool
	CloseChan() chan bool
}

type conn struct {
	sync.Mutex
	api       reflect.Value
	methods   map[string]Method
	conn      io.ReadWriteCloser
	Seq       uint
	pending   map[uint]*Call
	syncMutex *sync.Mutex
	closed    bool
	closeChan chan bool
}

var ErrClosed = errors.New("connection is closed")

const msgSep byte = 10

var Null = json.RawMessage([]byte("null"))

func isNull(value json.RawMessage) bool {
	if string(value) == "null" {
		return true
	}
	return false
}

type msg struct {
	Id     interface{}     `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
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

type Method struct {
	Func        reflect.Value
	ParamsType  reflect.Type
	ReplyType   reflect.Type
	synchronous bool
}

type Error string

func (e Error) Error() string {
	return string(e)
}

type Call struct {
	method string
	args   interface{}
	Reply  interface{}
	Error  error
	Done   chan *Call
}

func NewConn(c io.ReadWriteCloser) *conn {
	return &conn{
		conn:      c,
		pending:   make(map[uint]*Call),
		closeChan: make(chan bool, 1),
		syncMutex: new(sync.Mutex),
	}
}

func (c *conn) Serve() error {
	if c.conn == nil {
		return nil
	}
	dec := json.NewDecoder(c.conn)
	for {
		var data msg
		err := dec.Decode(&data)
		if err != nil {
			switch err.(type) {
			case *json.UnmarshalTypeError:
				log.Printf("%v %T", err, err)
				continue
			default:
				c.Close()
				c.Lock()
				c.closed = true
				for id, call := range c.pending {
					delete(c.pending, id)
					call.Error = err
					call.done()
				}
				c.Unlock()
				return err
			}
		}
		if data.Method == "" {
			// Response
			if data.Id == nil {
				continue
			}
			idFloat, ok := data.Id.(float64)
			if !ok {
				log.Println("rpc: wrong response, id not int",
					data)
				continue
			}
			id := uint(idFloat)
			c.Lock()
			call, ok := c.pending[id]
			c.Unlock()
			if call == nil || !ok {
				log.Println("rpc: no receiver for response",
					data)
				continue
			}
			c.Lock()
			delete(c.pending, id)
			c.Unlock()
			if isNull(data.Error) {
				err := json.Unmarshal(data.Result, call.Reply)
				if err != nil {
					call.Error = err
				}
			} else {
				call.Error = Error(data.Error)
			}
			call.done()
		} else {
			// Request
			funcParts := strings.SplitN(data.Method, ".", 2)
			var funcName string
			if len(funcParts) >= 2 {
				funcName = strings.Replace(
					funcParts[1], ".", "_", -1)
			} else if len(funcParts) == 1 {
				funcName = funcParts[0]
			}
			method, ok := c.methods[strings.ToLower(funcName)]
			if ok {
				if method.synchronous {
					c.Lock()
					go func(m Method, d msg) {
						c.syncMutex.Lock()
						c.Unlock()
						c.callMethod(m, d)
						c.syncMutex.Unlock()
					}(method, data)
				} else {
					go c.callMethod(method, data)
				}
			} else {
				c.sendError(data,
					"rpc: can't find method "+funcName)
			}
		}
	}
}

func (c *conn) sendError(data msg, errmsg string) {
	buf, err := json.Marshal(response{
		Id:     data.Id,
		Result: Null,
		Error:  errmsg,
	})
	if err != nil {
		log.Fatal(err)
		return
	}
	c.conn.Write(append(buf, msgSep))
}

func (c *conn) callMethod(method Method, data msg) {
	params := reflect.New(method.ParamsType)
	if err := json.Unmarshal(data.Params, params.Interface()); err != nil {
		c.sendError(data, err.Error())
		return
	}
	reply := reflect.New(method.ReplyType.Elem())
	var ret []reflect.Value
	ret = method.Func.Call([]reflect.Value{c.api,
		reflect.Indirect(params), reply})
	var retErr reflect.Value
	retErr = ret[0]
	if err := retErr.Interface(); err != nil {
		err := err.(error)
		c.sendError(data, err.Error())
		return
	}
	if data.Id != nil {
		reply = reply.Elem()
		buf, err := json.Marshal(response{
			Id:     data.Id,
			Result: reply.Interface(),
			Error:  Null,
		})
		if err != nil {
			c.sendError(data, err.Error())
			return
		}
		c.conn.Write(append(buf, msgSep))
	}
}

func (call *Call) done() {
	select {
	case call.Done <- call:
		// ok
	default:
		log.Println("rpc: insufficient doneChan capacity")
	}
}

func (c *conn) Go(method string, args interface{}, reply interface{}, done chan *Call) *Call {
	c.Lock()
	call := &Call{
		method: method,
		args:   args,
		Reply:  reply,
	}
	if done == nil {
		done = make(chan *Call, 10) // buffered.
	} else {
		if cap(done) == 0 {
			log.Panic("rpc: done channel is unbuffered")
		}
	}
	call.Done = done
	if c.closed {
		c.Unlock()
		call.Error = ErrClosed
		call.done()
		return call
	}
	id := c.Seq
	c.Seq++
	c.pending[id] = call
	c.Unlock()

	req := request{
		Id:     id,
		Method: method,
		Params: args,
	}
	data, err := json.Marshal(req)
	if err != nil {
		call.Error = err
		return call
	}
	if _, err := c.conn.Write(append(data, msgSep)); err != nil {
		if err == io.EOF {
			c.closed = true
		}
		call.Error = err
		c.Lock()
		delete(c.pending, id)
		c.Unlock()
		call.done()
		return call
	}
	return call
}

func (c *conn) Call(method string, args interface{}, reply interface{}) error {
	call := <-c.Go(method, args, reply, make(chan *Call, 1)).Done
	return call.Error
}

func (c *conn) Notify(method string, args interface{}) error {
	req := notify{
		Id:     Null,
		Method: method,
		Params: args,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(data, msgSep)); err != nil {
		return err
	}
	return nil
}

func (c *conn) NotifyResponse(args interface{}) error {
	req := response{
		Id:     0,
		Result: args,
		Error:  Null,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(data, msgSep)); err != nil {
		return err
	}
	return nil
}

func getMethods(api interface{}) (methods map[string]Method, err error) {
	methods = make(map[string]Method)
	apiType := reflect.TypeOf(api)
	for i := 0; i < apiType.NumMethod(); i++ {
		meth := apiType.Method(i)
		name := meth.Name
		if meth.Type.NumIn() != 3 {
			continue
		}
		methods[strings.ToLower(name)] = Method{
			Func:       meth.Func,
			ParamsType: meth.Type.In(1),
			ReplyType:  meth.Type.In(2),
		}
	}
	return
}

func (c *conn) Register(api interface{}) error {
	if c.methods != nil {
		return errors.New("we can register only one API")
	}
	methods, err := getMethods(api)
	if err != nil {
		return err
	}
	c.api = reflect.ValueOf(api)
	c.methods = methods
	return nil
}

func Dial(network, address string) (*conn, error) {
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	client := NewConn(conn)
	go client.Serve()
	return client, nil
}

func DialTimeout(network, address string, timeout time.Duration) (*conn, error) {
	conn, err := net.DialTimeout(network, address, timeout)
	if err != nil {
		return nil, err
	}
	client := NewConn(conn)
	go client.Serve()
	return client, nil
}

func (c *conn) RemoteAddr() string {
	conn, ok := c.conn.(net.Conn)
	if ok {
		return conn.RemoteAddr().String()
	} else {
		return ""
	}
}

func (c *conn) Synchronous(funcName string, value bool) {
	c.Lock()
	defer c.Unlock()
	name := strings.ToLower(funcName)
	method, ok := c.methods[name]
	if ok {
		method.synchronous = value
		c.methods[name] = method
	}
}

func (c *conn) Close() error {
	c.Lock()
	if !c.closed {
		c.closeChan <- true
	}
	c.closed = true
	c.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		return err
	}
	return nil
}

func (c *conn) Closed() bool {
	c.Lock()
	defer c.Unlock()
	return c.closed
}

func (c *conn) CloseChan() chan bool {
	return c.closeChan
}
