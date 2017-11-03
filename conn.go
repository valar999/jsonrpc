package jsonrpc

import (
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

type Conn struct {
	api      reflect.Value
	methods  map[string]method
	conn     io.ReadWriteCloser
	pending  map[uint]*Call
	Seq      uint
	seqMutex sync.Mutex
}

const msgSep byte = 10

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

func NewConn(conn io.ReadWriteCloser) *Conn {
	return &Conn{
		conn:    conn,
		pending: make(map[uint]*Call),
	}
}

func (c *Conn) Serve() error {
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
				for id, call := range c.pending {
					delete(c.pending, id)
					call.Error = err
					call.done()
				}
				return err
			}
		}
		if data.Method == "" {
			// Response
			if data.Id == nil {
				log.Println("rpc: wrong response, no id", data)
				continue
			}
			idFloat, ok := data.Id.(float64)
			if !ok {
				log.Println("rpc: wrong response, id not int",
					data)
				continue
			}
			id := uint(idFloat)
			call := c.pending[id]
			if call == nil {
				log.Println("rpc: no receiver for response",
					data)
				continue
			}
			delete(c.pending, id)
			if data.Error == "" {
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
			funcName := strings.Replace(funcParts[1], ".", "_", -1)
			method, ok := c.methods[funcName]
			if ok {
				go c.callMethod(method, data)
			} else {
				c.sendError(data,
					"rpc: can't find method "+funcName)
			}
		}
	}
}

func (c *Conn) sendError(data msg, errmsg string) {
	buf, err := json.Marshal(response{
		Id:     data.Id,
		Result: null,
		Error:  errmsg,
	})
	if err != nil {
		log.Fatal(err)
		return
	}
	c.conn.Write(append(buf, msgSep))
}

func (c *Conn) callMethod(method method, data msg) {
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
			Error:  null,
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

func (c *Conn) Go(method string, args interface{}, reply interface{}, done chan *Call) *Call {
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

	c.seqMutex.Lock()
	id := c.Seq
	c.Seq++
	c.seqMutex.Unlock()

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
	c.pending[id] = call
	if _, err := c.conn.Write(append(data, msgSep)); err != nil {
		call.Error = err
		return call
	}
	return call
}

func (c *Conn) Call(method string, args interface{}, reply interface{}) error {
	call := <-c.Go(method, args, reply, make(chan *Call, 1)).Done
	return call.Error
}

func (c *Conn) Notify(method string, args interface{}) error {
	req := notify{
		Id:     null,
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

func getMethods(api interface{}) (methods map[string]method, err error) {
	methods = make(map[string]method)
	apiType := reflect.TypeOf(api)
	for i := 0; i < apiType.NumMethod(); i++ {
		meth := apiType.Method(i)
		name := meth.Name
		if meth.Type.NumIn() != 3 {
			err = fmt.Errorf(
				"method %s has wrong number of ins: %d",
				name, meth.Type.NumIn())
			return
		}
		methods[name] = method{
			Func:       meth.Func,
			ParamsType: meth.Type.In(1),
			ReplyType:  meth.Type.In(2),
		}
		// TODO change only first char also
		methods[strings.ToLower(name)] = methods[name]
	}
	return
}

func (c *Conn) Register(api interface{}) error {
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

func Dial(network, address string) (*Conn, error) {
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	client := NewConn(conn)
	go client.Serve()
	return client, nil
}

func (c *Conn) Close() error {
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}
