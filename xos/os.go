package xos

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"reflect"
)

// 替换exec.CommandContext
func CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, arg...)
}

// 替换exec.CommandContext
func Command(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

// 替换filepath.Join
func Join(elem ...string) string {
	return filepath.Join(elem...)
}

func TLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true}
}

func Copy(dst io.Writer, src io.Reader) (written int64, err error) {
	return io.Copy(dst, src)
}

func ListenAndServe(address string, handler http.Handler) error {
	return http.ListenAndServe(address, handler)
}

func SocketCallMethod(ep any, methodName string, services map[string]reflect.Value, requestCls, responseCls string) (results []reflect.Value, err error) {
	var method reflect.Value
	for _, service := range services {
		method = service.MethodByName(methodName)
		if method.IsValid() {
			break
		}
	}
	if !method.IsValid() {
		s := fmt.Sprintf("no found method %s", methodName)
		if responseCls != "*socket.NoReturn" {
			err = errors.New(s)
			return
		}
	}
	if method.Kind() != reflect.Func {
		s := fmt.Sprintf("%s is not a method", methodName)
		if responseCls != "*socket.NoReturn" {
			err = errors.New(s)
			return
		}
	}

	args := []reflect.Value{
		reflect.ValueOf(ep),
		//reflect.ValueOf(context),
		//reflect.ValueOf(request),
	}

	type Common struct {
		Version string `json:"version"`  //请求版本
		Uid     uint   `json:"uid"`      //用户id
		UaToken string `json:"ua_token"` //token串
	}

	type Request struct {
		RequestId  uint64  `json:"request_id"`
		MethodName string  `json:"method_name"`
		Data       any     `json:"data"`
		Timestamp  int64   `json:"timestamp"`
		Timeout    uint32  `json:"timeout"`
		Common     *Common `json:"common"`
	}

	request := &Request{}

	type Empty struct {
	}

	switch requestCls {
	case "*socket.Request":
		args = append(args, reflect.ValueOf(request))
	case "*socket.Empty":
		args = append(args, reflect.ValueOf(&Empty{}))
	}

	defer func() {
		recoverVal := recover()
		if recoverVal == nil {
			return
		}
		s := fmt.Sprintf("%v", recoverVal)
		err = errors.New(s)
		panic(err)
	}()

	results = method.Call(args) // 分发到各 rpc 业务处理函数
	return
}
