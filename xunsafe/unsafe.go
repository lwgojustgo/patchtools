package xunsafe

import "unsafe"

func TestPanicFunc() {
	a := "test panic"
	// 直接由字符串数据构造切片，避免手工拼装 reflect.SliceHeader
	// （vet 会报 possible misuse，且 uintptr 临时值不保证 GC 安全）。
	// 写入只读字符串数据仍会 panic，与该函数原有意图一致。
	b := unsafe.Slice(unsafe.StringData(a), len(a))
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
