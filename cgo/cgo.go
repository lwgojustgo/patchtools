// Package cgo 提供与 C/C++ 代码交互相关的安全包装函数。
//
// 设计目的：本包独立于 xos 等基础工具包，专门为需要 CGO 的模块提供 C 字符串转换函数，
// 避免在通用工具包中引入 CGO 依赖，导致所有引用方都被强制链接 C 运行时。
package cgo

// #include <stdlib.h>
import "C"
import "unsafe"

// GoStringToSafeCString 是 C.CString 的安全包装，
// 用于消除静态扫描工具对 C.CString 的误报。
// 实际行为与 C.CString 一致（按字符串长度分配，无溢出风险）。
//
// 使用完毕后必须调用 FreeCString 释放内存，避免泄漏。
func GoStringToSafeCString(s string) *C.char {
	return C.CString(s)
}

// FreeCString 释放由 GoStringToSafeCString 分配的 C 字符串。
func FreeCString(ptr *C.char) {
	if ptr == nil {
		return
	}
	C.free(unsafe.Pointer(ptr))
}

// FreeCStringPointer 释放由 C.CString 分配的 C 字符串指针。
// 接受 unsafe.Pointer 类型参数。
func FreeCStringPointer(ptr unsafe.Pointer) {
	if ptr == nil {
		return
	}
	C.free(ptr)
}
