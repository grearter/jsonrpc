package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	fileName = "client"

	// rpc调用超时
	ErrTimeout = errors.New("rpc call timeout")

	//client已关闭
	ErrClientClosed = errors.New("client has closed")
)

type Client struct {
	addr     string
	conn     net.Conn
	codec    *Codec
	calls    map[uint32]*Call
	closing  bool
	shutdown bool
	seqId    uint32
	reqMutex sync.Mutex
	m        sync.Mutex
}

type Call struct {
	id     uint32
	method string
	req    interface{}
	done   chan *Response
	ctx    context.Context
}

func (c *Client) recv() {
	var err error

	for {
		var resp *Response
		err = c.codec.decoder.Decode(&resp)
		if err != nil {
			break
		}

		call, ok := c.calls[resp.Id]
		if !ok {
			continue
		}

		call.done <- resp

		c.m.Lock()
		delete(c.calls, resp.Id)
		c.m.Unlock()
	}

	c.reqMutex.Lock()
	c.m.Lock()
	c.shutdown = true
	for _, call := range c.calls {
		call.done <- &Response{Error: err.Error()}
	}
	c.m.Unlock()
	c.reqMutex.Unlock()

	return
}

func (c *Client) parseCall(method string, in interface{}) (newCall *Call, err error) {
	parts := strings.Split(method, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		err = fmt.Errorf("invalid method '%s'", method)
		return
	}

	newCall = &Call{
		id:     atomic.AddUint32(&c.seqId, 1),
		method: method,
		req:    in,
		done:   make(chan *Response, 1),
	}

	return
}

func (c *Client) Call(method string, in, out interface{}) (err error) {
	newCall, err := c.parseCall(method, in)
	if err != nil {
		return
	}

	go c.do(newCall)

	resp := <-newCall.done

	if resp.Error != "" {
		err = errors.New(resp.Error)
		return
	}

	if out == nil {
		return
	}

	// parse resp.Result to out
	if err = json.Unmarshal(resp.Result, out); err != nil {
		return
	}

	return
}

func (c *Client) CallWithTimeout(method string, in, out interface{}, timeout time.Duration) (err error) {
	newCall, err := c.parseCall(method, in)
	if err != nil {
		return
	}

	go c.do(newCall)

	select {
	case <-time.After(timeout):
		err = ErrTimeout
		return
	case resp := <-newCall.done:
		if resp.Error != "" {
			err = errors.New(resp.Error)
			return
		}

		if out == nil {
			return
		}

		// parse resp.Result to out
		if err = json.Unmarshal(resp.Result, out); err != nil {
			return
		}
	}

	return
}

func (c *Client) do(call *Call) {
	c.m.Lock()
	closing, shutdown := c.closing, c.shutdown
	if closing || shutdown {
		c.m.Unlock()
		call.done <- &Response{Error: ErrClientClosed.Error()}
		return
	}

	c.calls[call.id] = call
	c.m.Unlock()

	err := c.send(call)
	if err != nil {
		c.m.Lock()
		delete(c.calls, call.id)
		c.m.Unlock()
		call.done <- &Response{Error: err.Error()}
		return
	}

	return
}

func (c *Client) send(call *Call) (err error) {
	c.reqMutex.Lock()
	body, _ := json.Marshal(call.req)
	req := &Request{
		Id:     call.id,
		Method: call.method,
		Param:  body,
	}

	err = c.codec.encoder.Encode(req)
	c.reqMutex.Unlock()
	return
}

func (c *Client) Close() {

	c.m.Lock()
	if c.shutdown || c.closing {
		c.m.Unlock()
		return
	}

	_ = c.conn.Close()
	c.m.Unlock()
	return
}

func DialWithTimeout(addr string, timeout time.Duration) (c *Client, err error) {
	dialer := &net.Dialer{
		Timeout: timeout,
	}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return
	}

	c = &Client{
		addr:  addr,
		calls: make(map[uint32]*Call),
		conn:  conn,
		codec: NewCodec(conn),
	}

	go c.recv()
	return
}

func Dial(addr string) (c *Client, err error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}

	c = &Client{
		addr:  addr,
		calls: make(map[uint32]*Call),
		conn:  conn,
		codec: NewCodec(conn),
	}

	go c.recv()
	return
}
