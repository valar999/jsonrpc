package jsonrpc

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

type TestAPIFactory struct {
}

func (f *TestAPIFactory) NewConn(conn io.ReadWriteCloser) interface{} {
	return new(API)
}

type API struct {
	notify int
}

func (a *API) Add(args [2]int, reply *int) error {
	*reply = args[0] + args[1]
	return nil
}

func (a *API) AddSlow(args [3]int, reply *int) error {
	time.Sleep(time.Millisecond * time.Duration(args[2]))
	*reply = args[0] + args[1]
	return nil
}

func (a *API) Notify(args [2]int, reply *int) error {
	a.notify++
	return nil
}

type responseT struct {
	Id     interface{}     `json:"id"`
	Result interface{}     `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func TestServer(t *testing.T) {
	server := NewServer(new(TestAPIFactory))
	listener, _ := net.Listen("tcp", "localhost:0")
	go server.Serve(listener)

	client, _ := Dial("tcp", listener.Addr().String())
	var reply int
	client.Call("API.Add", [2]int{1, 2}, &reply)
	if reply != 3 {
		t.Error("wrong call reply", reply)
	}
}

func TestServerConn(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := NewConn(srv)
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	cli.Write([]byte(`{"id":1,"method":"API.Add","params":[2,3]}`))
	var data responseT
	if err := cliDec.Decode(&data); err != nil {
		t.Fail()
	}
	if data.Id.(float64) != 1 {
		t.Error("id != 1")
	}
	if data.Result.(float64) != 5 {
		t.Error("result != 5")
	}
	if !bytes.Equal(data.Error, null) {
		t.Error("error != null")
	}
}

func TestServerConnWithTwoSlow(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := NewConn(srv)
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	cli.Write([]byte(`{"id":1,"method":"API.AddSlow","params":[1,2,50]}`))
	cli.Write([]byte(`{"id":2,"method":"API.AddSlow","params":[1,3,10]}`))
	var data1, data2 response
	start := time.Now()
	if err := cliDec.Decode(&data1); err != nil {
		t.Fail()
	}
	t1 := time.Since(start)
	if err := cliDec.Decode(&data2); err != nil {
		t.Fail()
	}
	tDiff := time.Since(start) - t1
	if tDiff < time.Millisecond*35 || tDiff > time.Millisecond*50 {
		t.Error("tDiff =", tDiff)
	}
	if t1 < time.Millisecond*5 || t1 > time.Millisecond*17 {
		t.Error("t1 =", t1)
	}
	if data1.Id.(float64) == 1 {
		t.Error("first result from call1")
	}
	if data1.Result.(float64) != 4 {
		t.Error("result != 4")
	}
	if data2.Result.(float64) != 3 {
		t.Error("result != 3")
	}
}

func TestNotify(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	api := new(API)
	server := NewConn(srv)
	if err := server.Register(api); err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	cli.Write([]byte(`{"id":null,"method":"API.notify","params":[2,3]}`))
	cli.Write([]byte(`{"id":null,"method":"API.notify","params":[2,3]}`))
	time.Sleep(time.Millisecond * 100)
	if api.notify != 2 {
		t.Error("notification doesn't work")
	}
}

func TestClient(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	server := NewConn(srv)
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	go server.Serve()

	client := NewConn(cli)
	go client.Serve()
	var reply int
	if err := client.Call("API.Add", [2]int{1, 2}, &reply); err != nil {
		t.Error(err)
	}
	if reply != 3 {
		t.Error("wrong call reply", reply)
	}
	if err := client.Call("API.Add", [2]int{1, 2}, &reply); err != nil {
		t.Error(err)
	}
	// initial seq = 0
	if client.Seq != 2 {
		t.Error("seq =", client.Seq)
	}
	if len(client.pending) != 0 {
		t.Error("pending not empty")
	}
}

func TestSecondAPI(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	server := NewConn(srv)
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	err := server.Register(new(API))
	if err == nil {
		t.Error("registered second api")
	}
}

func TestUnknownMethod(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := NewConn(srv)
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	cli.Write([]byte(`{"id":1,"method":"API.AddX","params":[2,3]}`))
	var data responseT
	if err := cliDec.Decode(&data); err != nil {
		t.Fail()
	}
	if data.Result != nil {
		t.Error("result is not null")
	}
	if data.Error == nil {
		t.Error("error is null")
	}
}

func TestClosedClientConn(t *testing.T) {
	listener, _ := net.Listen("tcp", "localhost:0")
	defer listener.Close()
	client, _ := Dial("tcp", listener.Addr().String())
	var reply int
	client.Go("API.AddSlow", [3]int{1, 2, 50}, &reply, nil)
	conn, _ := listener.Accept()
	c := NewConn(conn)
	api := new(API)
	c.Register(api)
	client.Close()
	err := c.Serve()
	if err != io.EOF {
		t.Error("Serve() return", err)
	}
}

func TestClosedServerConn(t *testing.T) {
	listener, _ := net.Listen("tcp", "localhost:0")
	defer listener.Close()
	client, _ := Dial("tcp", listener.Addr().String())
	var reply int
	call := client.Go("API.AddSlow", [3]int{1, 2, 50}, &reply, nil)
	conn, _ := listener.Accept()
	conn.Close()
	<-call.Done
	_, ok := call.Error.(*net.OpError)
	if !ok && call.Error != io.EOF {
		t.Errorf("call return %T", call.Error)
	}
}
