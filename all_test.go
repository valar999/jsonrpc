package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

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

type APICtx struct {
}

func (a *APICtx) Add(ctx context.Context, args [2]int, reply *int) error {
	*reply = args[0] + args[1]
	if ctx.Value(ConnKey) != nil {
		*reply++
	}
	return nil
}

type testkey string

const TestKey testkey = "test"

func (a *APICtx) AddCtxRet(ctx context.Context, args interface{}, reply *int) (context.Context, error) {
	if ctx.Value(TestKey) == nil {
		ctx = context.WithValue(ctx, TestKey, true)
		*reply = 1
	} else {
		*reply = 2
	}
	return ctx, nil
}

type responseT struct {
	Id     interface{}     `json:"id"`
	Result interface{}     `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func TestServer(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := New()
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	go server.ServeConn(srv)
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

func TestServerWithTwoSlow(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := New()
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	go server.ServeConn(srv)
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
	server := New()
	if err := server.Register(api); err != nil {
		t.Fatal(err)
	}
	go server.ServeConn(srv)
	cli.Write([]byte(`{"id":null,"method":"API.notify","params":[2,3]}`))
	cli.Write([]byte(`{"id":null,"method":"API.notify","params":[2,3]}`))
	time.Sleep(time.Millisecond * 50)
	if api.notify != 2 {
		t.Error("notification doesn't work")
	}
}

func TestClient(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	server := New()
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	go server.ServeConn(srv)

	client := NewConn(cli)
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
}

func TestCtx(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := New()
	if err := server.Register(new(APICtx)); err != nil {
		t.Fatal(err)
	}
	go server.ServeConnWithCtx(context.Background(), srv)
	cli.Write([]byte(`{"id":1,"method":"API.add","params":[2,3]}`))
	var data response
	if err := cliDec.Decode(&data); err != nil {
		t.Fail()
	}
	if data.Result.(float64) != 6 {
		t.Error("result != 6")
	}
}

func TestCtxReturn(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := New()
	if err := server.Register(new(APICtx)); err != nil {
		t.Fatal(err)
	}
	go server.ServeConnWithCtx(context.Background(), srv)

	cli.Write([]byte(`{"id":1,"method":"API.addctxret","params":""}`))
	var data response
	if err := cliDec.Decode(&data); err != nil {
		t.Fail()
	}
	if data.Result.(float64) != 1 {
		t.Error("wrong reply", data.Result)
	}

	cli.Write([]byte(`{"id":1,"method":"API.addctxret","params":""}`))
	if err := cliDec.Decode(&data); err != nil {
		t.Fail()
	}
	if data.Result.(float64) != 2 {
		t.Error("wrong reply", data.Result)
	}
}

func TestSecondAPI(t *testing.T) {
	server := New()
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	err := server.Register(new(APICtx))
	if err == nil {
		t.Error("registered second api")
	}
}

func TestUnknownMethod(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := New()
	if err := server.Register(new(API)); err != nil {
		t.Fatal(err)
	}
	go server.ServeConn(srv)
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
