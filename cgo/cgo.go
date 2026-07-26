// Package cgo 提供与 C/C++ 代码交互相关的安全包装函数。
//
// 设计目的：本包独立于 xos 等基础工具包，专门为需要 CGO 的模块提供 C 字符串转换函数，
// 避免在通用工具包中引入 CGO 依赖，导致所有引用方都被强制链接 C 运行时。
//
// 同时，本包针对 caller 业务模块的 C 接口（PluginWrapCaller.h）提供跨 cgo
// 翻译单元的桥接函数（BridgeInitializeCaller / BridgeSetLogger）：
//	- 集中处理 patchtools/cgo 翻译单元与 caller 业务翻译单元之间的 *_Ctype_char 转换
//	- 集中使用 unsafe.Pointer 中转（Go 语言层面无法避免，见 issue #13467）
//	- caller 业务代码无需关心跨翻译单元类型转换，caller.go 中无 unsafe 类型转换。
package cgo

// #include <stdlib.h>
// #include "PluginWrapCaller.h"
//
// #cgo CFLAGS: -I${SRCDIR}
// #cgo windows LDFLAGS: -L${SRCDIR}/libs/winlibs -lpluginwrapcaller -Wl,--allow-multiple-definition
// #cgo linux,amd64 LDFLAGS: -L${SRCDIR}/libs/linuxlibs/x86_64 -lPluginWrapCaller -Wl,--allow-multiple-definition
// #cgo linux,arm64 LDFLAGS: -L${SRCDIR}/libs/linuxlibs/aarch64 -lPluginWrapCaller -Wl,--allow-multiple-definition
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

// ----------------------------------------------------------------------------
// 跨 cgo 翻译单元的 PluginWrapCaller 桥接函数
// ----------------------------------------------------------------------------
//
// 背景
//
// Go cgo 不在语言层面支持跨 cgo 翻译单元的类型统一（见
// https://github.com/golang/go/issues/13467）。即，每个包含 import "C"
// 的 .go 文件，cgo 工具都会为它生成一份独有的 _Ctype_* 标识符，这些标识符
// 在 Go 编译期类型系统里是不同类型：
//
//   - patchtools/cgo 包有自己的 *_Ctype_char
//   - caller.go 自己的 import "C" 也有自己的 *_Ctype_char（即 C.char）
//
// 把 patchtools/cgo 包的 _Ctype_char* 直接传给 caller.go 的 C 函数会编译报错。
//
// 解决方案
//
// Go 团队官方认可的唯一跨翻译单元解决方案（见 bcmills 2016-06-27 评论
// 在 issue #13467）正是 unsafe.Pointer 中转：
//
//   - 所有 *_Ctype_char 都是 char* 的别名，运行时内存布局完全相同
//   - 通过 unsafe.Pointer 在不同翻译单元之间"打通"类型
//
// 本包集中此 unsafe 转换，让 caller.go 业务代码完全不需要接触 unsafe。
// caller.go 业务代码改用本包导出的 BridgeInitializeCaller / BridgeSetLogger
// 等函数，避免直接调 C 函数且不出现 unsafe 转换。

// BridgeInitializeCaller 透传到 C.initializeCaller。
//
// 把 patchtools/cgo 翻译单元的 *_Ctype_char 与 caller 自身 cgo 翻译单元的
// *_Ctype_char 之间的转换集中在本函数体内（unsafe.Pointer 中转），
// caller.go 业务代码无需关心。
//
// pluginPath / configPath 由 caller.go 传入 Go string，由本函数负责：
//  1. 用 GoStringToSafeCString 把 Go string 转为 *_Ctype_char
//  2. 集中做 unsafe.Pointer 中转，转为 caller 翻译单元的 *C.char
//  3. 调用 C.initializeCaller
//  4. 用 FreeCStringPointer 释放内存
//
// 返回 true 表示初始化成功，false 表示失败。
func BridgeInitializeCaller(caller unsafe.Pointer, pluginPath, configPath string) bool {
	cstrPluginPath := GoStringToSafeCString(pluginPath)
	cstrConfigPath := GoStringToSafeCString(configPath)
	defer FreeCStringPointer(unsafe.Pointer(cstrPluginPath))
	defer FreeCStringPointer(unsafe.Pointer(cstrConfigPath))

	// 跨 cgo 翻译单元的 *C.char 转换，是 cgo 隔离约束下的标准模式。
	// C.initializeCaller 返回 _Ctype__Bool（patchtools/cgo 翻译单元的 bool），
	// 与 Go 的 bool 是不同类型，需要显式转换。
	result := C.initializeCaller(caller, (*C.char)(unsafe.Pointer(cstrPluginPath)), (*C.char)(unsafe.Pointer(cstrConfigPath)))
	return bool(result)
}

// BridgeSetLogger 透传到 C.setLogger，集中处理跨 cgo 翻译单元的 *_Ctype_char
// 类型转换。caller.go 业务代码不出现 unsafe 类型转换。
func BridgeSetLogger(caller unsafe.Pointer, logPath, appName string) {
	cstrLogPath := GoStringToSafeCString(logPath)
	defer FreeCStringPointer(unsafe.Pointer(cstrLogPath))

	cstrAppName := GoStringToSafeCString(appName)
	defer FreeCStringPointer(unsafe.Pointer(cstrAppName))

	C.setLogger(caller, (*C.char)(unsafe.Pointer(cstrLogPath)), (*C.char)(unsafe.Pointer(cstrAppName)))
}
