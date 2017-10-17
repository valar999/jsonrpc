package jsonrpc

import (
	"bytes"
	"encoding/json"
	"net"
	"testing"
	"time"
)

type API struct {
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

func TestServer(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	cliDec := json.NewDecoder(cli)
	server := NewApi(new(API))
	go server.ServeConn(srv)
	cli.Write([]byte(`{"id":1,"method":"API.Add","params":[2,3]}`))
	var data Response
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
	server := NewApi(new(API))
	go server.ServeConn(srv)
	cli.Write([]byte(`{"id":1,"method":"API.AddSlow","params":[1,2,50]}`))
	cli.Write([]byte(`{"id":2,"method":"API.AddSlow","params":[1,3,10]}`))
	var data1, data2 Response
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

func TestClient(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	server := NewApi(new(API))
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
