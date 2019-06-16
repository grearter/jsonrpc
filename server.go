package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	typeOfError      = reflect.TypeOf((*error)(nil)).Elem()
	NoExportedMethod = errors.New("no exported method")
)

type Request struct {
	Id     uint32          `json:"id"`
	Method string          `json:"method"`
	Param  json.RawMessage `json:"param"`
}

func (req *Request) Regular() error {
	parts := strings.Split(req.Method, ".")
	if len(parts) != 2 {
		return fmt.Errorf("invalid method: %s", req.Method)
	}

	if parts[0] == "" {
		return fmt.Errorf("invalid service name: %s", parts[0])
	}

	if parts[1] == "" {
		return fmt.Errorf("invalid serviceMethod name: %s", parts[1])
	}

	return nil
}

type Response struct {
	Id     uint32          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

type Connection struct {
	s     *Server
	c     net.Conn
	codec *Codec
}

func (conn *Connection) Serve() {
	defer conn.c.Close()

	for {
		var req *Request
		err := conn.codec.decoder.Decode(&req)
		if err != nil {
			return
		}

		conn.do(req)
	}
}

func (conn *Connection) do(req *Request) {
	if err := req.Regular(); err != nil {
		conn.replyError(req.Id, err)
		return
	}

	parts := strings.Split(req.Method, ".")
	svc, err := conn.s.getService(parts[0])
	if err != nil {
		conn.replyError(req.Id, err)
		return
	}

	mthd, err := svc.getMethod(parts[1])
	if err != nil {
		conn.replyError(req.Id, err)
		return
	}

	var inParam reflect.Value

	inParam = reflect.New(mthd.inType)

	err = json.Unmarshal(req.Param, inParam.Interface())

	outParam := reflect.New(mthd.outType.Elem())

	returnValues := mthd.method.Func.Call([]reflect.Value{svc.receiverValue, inParam.Elem(), outParam})

	errInter := returnValues[0].Interface()

	if errInter != nil {
		conn.replyError(req.Id, errInter.(error))
		return
	}

	conn.replyResult(req.Id, outParam.Interface())
	return
}

type service struct {
	receiverType  reflect.Type
	receiverValue reflect.Value
	methodMap     map[string]*serviceMethod
}

func (svc *service) getMethod(methodName string) (*serviceMethod, error) {
	svcMethod, ok := svc.methodMap[methodName]

	if !ok {
		return nil, fmt.Errorf("methodName '%s' not exists", methodName)
	}

	return svcMethod, nil
}

type serviceMethod struct {
	method  reflect.Method
	inType  reflect.Type
	outType reflect.Type
}

type Server struct {
	Addr       string
	Listener   net.Listener
	serviceMap map[string]*service
}

// Is this an exported - upper case - name?
func isExported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// Is this type exported or a builtin?
func isExportedOrBuiltinType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// PkgPath will be non-empty even for an exported type,
	// so we need to check the type name as well.
	return isExported(t.Name()) || t.PkgPath() == ""
}

func (s *Server) Register(receiver interface{}) error {
	recvType := reflect.TypeOf(receiver)
	recvValue := reflect.ValueOf(receiver)

	serviceName := reflect.Indirect(recvValue).Type().Name()
	if serviceName == "" {
		return errors.New("invalid service name")
	}

	newService := &service{
		receiverType:  recvType,
		receiverValue: recvValue,
		methodMap:     make(map[string]*serviceMethod),
	}

	for i := 0; i < recvType.NumMethod(); i++ {
		method := recvType.Method(i)
		methodType := method.Type
		methodName := method.Name

		if method.PkgPath != "" {
			continue
		}

		if methodType.NumIn() != 3 {
			continue
		}

		inType := methodType.In(1)

		if !isExportedOrBuiltinType(inType) {
			continue
		}

		outType := methodType.In(2)
		if outType.Kind() != reflect.Ptr {
			continue
		}

		if !isExportedOrBuiltinType(outType) {
			continue
		}

		if methodType.NumOut() != 1 {
			continue
		}

		if methodType.Out(0) != typeOfError {
			continue
		}

		newService.methodMap[methodName] = &serviceMethod{
			method:  method,
			inType:  inType,
			outType: outType,
		}
	}

	if len(newService.methodMap) <= 0 {
		return NoExportedMethod
	}

	if s.serviceMap == nil {
		s.serviceMap = make(map[string]*service)
	}

	s.serviceMap[serviceName] = newService

	return nil
}

func (s *Server) getService(serviceName string) (*service, error) {
	svc, ok := s.serviceMap[serviceName]

	if !ok {
		return nil, fmt.Errorf("serviceName '%s' not exists", serviceName)
	}

	return svc, nil
}

func (s *Server) getMethod(method string) (_service *service, _serviceMethod *serviceMethod, err error) {
	parts := strings.Split(method, ".")

	if len(parts) != 2 {
		err = fmt.Errorf("invalid method(%s)", method)
		return
	}

	serviceName, serviceMethodName := parts[0], parts[1]

	if _, ok := s.serviceMap[serviceName]; !ok {
		err = fmt.Errorf("service '%s' not found", serviceName)
		return
	}

	_service = s.serviceMap[serviceName]

	if _, ok := _service.methodMap[serviceMethodName]; ok {
		err = fmt.Errorf("serviceMethod '%s' not found in service '%s'", serviceName, serviceMethodName)
		return
	}

	return
}

func (conn *Connection) replyError(id uint32, err error) {
	resp := &Response{
		Id:    id,
		Error: err.Error(),
	}

	_ = conn.codec.encoder.Encode(resp)
	return
}

func (conn *Connection) replyResult(id uint32, result interface{}) {
	resultBytes, _ := json.Marshal(result)

	resp := &Response{
		Id:     id,
		Result: resultBytes,
	}

	_ = conn.codec.encoder.Encode(resp)
	return
}

func (s *Server) ListenAndServe() (err error) {
	if s.Listener == nil {
		s.Listener, err = net.Listen("tcp", s.Addr)
		if err != nil {
			return
		}
	}

	err = s.Serve()
	return
}

func (s *Server) Serve() error {

	for {
		rw, err := s.Listener.Accept()
		if err != nil {
			return err
		}

		conn := &Connection{
			c:     rw,
			s:     s,
			codec: NewCodec(rw),
		}

		go conn.Serve()
	}
}

func NewServer(addr string) *Server {
	return &Server{
		Addr: addr,
	}
}
