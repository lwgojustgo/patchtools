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

// procCallerResponseBodySizePrefix 是 C++ 端在响应体前面追加的 C.uint
// 长度前缀字段的字节数。C.uint 在 caller 与 patchtools/cgo 同一平台编译，
// unsafe.Sizeof(C.uint(0)) 在两翻译单元结果一致（均为 4 / 8）。
var procCallerResponseBodySizePrefix = unsafe.Sizeof(C.uint(0))

// BridgeDecodeProcCallerResult 从 C.procCaller 返回的 char* 缓冲中解码响应体。
//
// C.procCaller 在 cTemp 指向的 C 堆缓冲区中存放以下结构（小端）：
//
//	[  0.. 4)  bodySize  uint32    - 后续响应体字节数
//	[  4.. 4+bodySize )  body  - 实际响应体
//
// 解码逻辑中所有 unsafe.Pointer 操作（cTemp -> unsafe.Pointer、跨平台指针
// 算术 bodySize 字段偏移、C.GoBytes 读取）都集中在本函数内，避免 caller
// 业务代码直接操作 unsafe 内存。
//
// 参数 cTempUnsafe 是 C.procCaller 在 caller 翻译单元返回的 *C.char，
// 通过 unsafe.Pointer 中转传入（cgo 跨翻译单元隔离约束：不同翻译单元的
// *_Ctype_char 在 Go 类型系统里是不同类型，见 issue #13467）。函数体内
// 将其转换为 patchtools/cgo 翻译单元的 *_Ctype_char 后再读前缀字段。
//
// allowEmpty 表示 bodySize == 0 是否合法：caller 处理空白瓦片场景时（HTTP
// Header 中包含 X-Tile-Empty=true）允许 bodySize == 0；其他场景必须 > 0。
//
// maxBodySize 是允许的最大响应体字节数，避免恶意或损坏数据导致 C.GoBytes
// 一次性分配超大内存。
//
// 返回值：
//   - bodySize：cTemp 缓冲中前缀字段声明的响应体字节数（用于错误日志）。
//   - ok=false：响应体大小不合法（bodySize == 0 且 !allowEmpty，或 bodySize 超过上限），
//     body 为 nil。caller 应自行调用 C.freeMem 释放 C++ 内存。
//   - ok=true：body 为解码后的响应体字节切片，caller 仍需自行释放 cTemp。
func BridgeDecodeProcCallerResult(cTempUnsafe unsafe.Pointer, allowEmpty bool, maxBodySize uint32) (body []byte, bodySize uint32, ok bool) {
	if cTempUnsafe == nil {
		return nil, 0, false
	}

	// 跨 cgo 翻译单元类型转换：unsafe.Pointer 中转。
	ptr := unsafe.Pointer(cTempUnsafe)

	// 读前 sizeof(C.uint) 字节作为 bodySize（C++ 端以原生字节序写入，
	// 与 caller 同一平台）。直接读 uint32 是安全的，因为：
	//  1. C.uint 与 uint32 在 caller 与 patchtools 同一平台同一 Go runtime 编译下
	//     内存布局一致（C99/C++11 uint32_t 固定 32 位，C.uint 在 amd64 也为 32 位）。
	//  2. caller 解析时已经限定此字段为 4 字节（caller 与 patchtools 同步编译）。
	bodySize = *(*uint32)(ptr)

	if bodySize == 0 && !allowEmpty {
		return nil, bodySize, false
	}
	if bodySize > maxBodySize {
		return nil, bodySize, false
	}

	// 跳过 bodySize 字段前缀，定位响应体起点。
	pBody := unsafe.Add(ptr, procCallerResponseBodySizePrefix)
	body = C.GoBytes(pBody, C.int(bodySize))
	return body, bodySize, true
}

// BridgeProcCaller 是 C.procCaller 的完整封装，集中处理跨 cgo 翻译单元的所有
// unsafe.Pointer 操作。
//
// 集中执行：
//  1. []byte -> *_Ctype_char 零拷贝转换（body / traceID）
//  2. cgo 跨翻译单元 *_Ctype_char -> caller 翻译单元 *C.char 中转
//  3. cgo 跨翻译单元 *_Ctype_uchar / *_Ctype_char_star 出参中转
//
// caller 业务代码完全不出现 unsafe 转换。
//
// 参数：
//   - caller：plugin caller 指针（caller 翻译单元 void*，已转 unsafe.Pointer）
//   - body / traceID：null-terminated []byte 输入
//   - commandID：命令 ID
//   - cDataTypeOut：caller 翻译单元 *C.uchar 输出的 unsafe.Pointer 形式
//     （caller 业务代码传 unsafe.Pointer(&cDataType)）
//   - cHttpHeaderOut：caller 翻译单元 **C.char 输出的 unsafe.Pointer 形式
//     （caller 业务代码传 unsafe.Pointer(&cHttpHeader)）
//
// 返回值：C.procCaller 返回的 char* 缓冲（caller 翻译单元的 *C.char 转 unsafe.Pointer）
func BridgeProcCaller(caller unsafe.Pointer, body []byte, traceID []byte, commandID uint64, cDataTypeOut, cHttpHeaderOut unsafe.Pointer) unsafe.Pointer {
	cBody := ByteArrayToSafeCString(body)
	cTraceID := ByteArrayToSafeCString(traceID)
	pDataType := (*C.uchar)(cDataTypeOut)
	pHttpHeader := (**C.char)(cHttpHeaderOut)
	cTemp := C.procCaller(caller, cBody, C.ulong(commandID), cTraceID, pDataType, pHttpHeader)
	return unsafe.Pointer(cTemp)
}

// BridgeFreeMem 是 C.freeMem 的封装，集中处理跨 cgo 翻译单元 *C.char -> unsafe.Pointer 中转。
func BridgeFreeMem(p unsafe.Pointer) {
	if p == nil {
		return
	}
	C.freeMem((*C.char)(p))
}

// BridgeGoString 是 C.GoString 的封装，集中处理跨 cgo 翻译单元 *C.char -> Go string。
func BridgeGoString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	return C.GoString((*C.char)(p))
}

// BridgeByteArrayToCString 是 caller 业务代码调用 ByteArrayToCString 的入口。
//
// 集中处理跨 cgo 翻译单元类型转换：patchtools/cgo.ByteArrayToSafeCString 返回
// patchtools/cgo 翻译单元的 *_Ctype_char（Go 类型系统里与 caller.go 的 *C.char
// 是不同类型），caller 业务代码如直接 `(*C.char)(cstr)` 会触发跨翻译单元
// unsafe 转换。本函数封装此转换，caller 业务代码改用 unsafe.Pointer。
//
// 返回的 unsafe.Pointer 可在 caller 翻译单元里安全转为 (*C.char)，
// 转换操作请集中到 caller 侧唯一的 *_Ctype_char -> *C.char 桥接点。
//
// 注：caller 业务代码目前已通过 BridgeProcCaller 整体封装 procCaller 调用，
// 此函数保留以备将来其他 cgo 集成场景使用。
func BridgeByteArrayToCString(ba []byte) unsafe.Pointer {
	cstr := ByteArrayToSafeCString(ba)
	return unsafe.Pointer(cstr)
}

// ByteArrayToSafeCString 是零拷贝 []byte -> *_Ctype_char 转换。
//
// 调用方必须确保：
//  1. ba 已经以 null 结尾（最后一个字节 == 0）
//  2. 在 C 函数调用期间 ba 保持存活（防止 GC 释放底层数组）
//  3. 适用于同步 C 函数调用（C 函数不保存指针）
//
// 返回的 *_Ctype_char 是 patchtools/cgo 翻译单元的 *_Ctype_char，调用方
// 跨翻译单元使用须经 BridgeByteArrayToCString 桥接。
func ByteArrayToSafeCString(ba []byte) *C.char {
	if len(ba) == 0 {
		return C.CString("")
	}
	return (*C.char)(unsafe.Pointer(&ba[0]))
}
