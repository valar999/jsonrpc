# JSON-RPC 1.0 [![GoDoc](https://godoc.org/github.com/valar999/jsonrpc?status.svg)](http://godoc.org/github.com/valar999/jsonrpc) [![Build Status](https://travis-ci.org/valar999/jsonrpc.svg)](https://travis-ci.org/valar999/jsonrpc) [![Coverage Status](https://coveralls.io/repos/valar999/jsonrpc/badge.svg?branch=master&service=github)](https://coveralls.io/github/valar999/jsonrpc?branch=master)

jsonrpc is a library that support of bidirectional calls and notifies

Implements [JSON-RPC 1.0](http://www.jsonrpc.org/specification_v1).

- Call from both sides
- IDs could be not int
- Support of [Notification](http://www.jsonrpc.org/specification_v1#a1.3Notification)


## Installation

```sh
go get github.com/valar999/jsonrpc
```

## Example
```go
package main

import "github.com/valar999/jsonrpc"

type API struct {
}

func (a *API) Add(args [2]int, reply *int) error {
        *reply = args[0] + args[1]
        return nil
}

func main() {
        server := jsonrpc.NewApi(new(API))
	server.ListenAndServe(":3333")
}

```
