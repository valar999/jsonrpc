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
	Id     int         `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type response struct {
	Id     interface{}     `json:"id"`
	Result interface{}     `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type notify struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type method struct {
	Func       reflect.Value
	ParamsType reflect.Type
	ReplyType  reflect.Type
}

type Server struct {
	api      reflect.Value
	methods  map[string]method
	conn     io.ReadWriteCloser
	response map[int]chan msg
	Seq      int
	seqMutex *sync.Mutex
}

func Dial(network, address string) (*Server, error) {
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	return NewConn(conn), nil
}

func NewConn(conn net.Conn) *Server {
	server := &Server{
		conn:     conn,
		response: make(map[int]chan msg),
		seqMutex: new(sync.Mutex),
	}
	go server.ServeConn(conn)
	return server
}

func NewApi(api interface{}) *Server {
	apiType := reflect.TypeOf(api)
	methods := make(map[string]method)
	for i := 0; i < apiType.NumMethod(); i++ {
		meth := apiType.Method(i)
		name := meth.Name
		methods[name] = method{
			Func:       meth.Func,
			ParamsType: meth.Type.In(1),
			ReplyType:  meth.Type.In(2),
		}
		// TODO change only first char also
		methods[strings.ToLower(name)] = methods[name]
	}
	return &Server{
		api:      reflect.ValueOf(api),
		methods:  methods,
		response: make(map[int]chan msg),
		seqMutex: new(sync.Mutex),
	}
}

// call api method, this function is called from ServeConn()
func (s *Server) callMethod(conn io.ReadWriteCloser, method method, data msg) {
	params := reflect.New(method.ParamsType)
	err := json.Unmarshal(data.Params, params.Interface())
	if err != nil {
		// TODO write to conn Error reply
		log.Println(err)
		return
	}
	reply := reflect.New(method.ReplyType.Elem())
	ret := method.Func.Call([]reflect.Value{s.api,
		reflect.Indirect(params), reply})
	if err := ret[0].Interface(); err != nil {
		log.Println("err", err)
		return
	}
	reply = reply.Elem()
	buf, err := json.Marshal(response{
		Id:     data.Id,
		Result: reply.Interface(),
		Error:  null,
	})
	if err != nil {
		log.Println(err)
		return
	}
	conn.Write(buf)
}

func (s *Server) ServeConn(conn io.ReadWriteCloser) {
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
			return
		}
		if data.Id == nil && data.Method != "" {
			log.Println("notify")
		} else if data.Method == "" && data.Id != nil {
			id, ok := data.Id.(float64)
			if !ok {
				log.Println("wrong response", data)
				continue
			}
			responseChan := s.response[int(id)]
			if responseChan == nil {
				log.Println("no receiver for response", data)
				continue
			}
			responseChan <- data
		} else if data.Id != nil {
			// Request
			funcParts := strings.Split(data.Method, ".")
			funcName := funcParts[len(funcParts)-1]
			method := s.methods[funcName]
			go s.callMethod(conn, method, data)
		}
	}
}

func (s *Server) Call(method string, args interface{}, reply interface{}) error {
	s.seqMutex.Lock()
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
	if _, err := s.conn.Write(data); err != nil {
		return err
	}
	response := <-s.response[id]
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if err := json.Unmarshal(response.Result, reply); err != nil {
		return err
	}
	return nil
}

func (s *Server) Notify(method string, args interface{}) error {
	req := notify{
		Method: method,
		Params: args,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := s.conn.Write(data); err != nil {
		return err
	}
	return nil
}
