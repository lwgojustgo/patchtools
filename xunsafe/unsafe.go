package xunsafe

import (
	"reflect"
	"unsafe"
)

func TestPanicFunc() {
	a := "test panic"
	sh := (*reflect.StringHeader)(unsafe.Pointer(&a))
	bh := reflect.SliceHeader{
		Data: sh.Data,
		Len:  sh.Len,
		Cap:  sh.Len,
	}
	b := *(*[]byte)(unsafe.Pointer(&bh))
	b[0] = 'H'
}

func String2Bytes(s string) []byte {
	x := (*[2]uintptr)(unsafe.Pointer(&s))
	h := [3]uintptr{x[0], x[1], x[1]}
	return *(*[]byte)(unsafe.Pointer(&h))
}

func Bytes2String(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

func AToByteHelp(v string) []byte {
	x := (*[2]uintptr)(unsafe.Pointer(&v))
	h := [3]uintptr{x[0], x[1], x[1]}
	return *(*[]byte)(unsafe.Pointer(&h))
}
