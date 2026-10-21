// Package main implements the CLIProxyAPI (CPA) native plugin "anyrouter".
//
// It ports the two AnyRouter helpers that used to live outside CPA:
//
//   - the daily cookie check-in script (POST /api/user/sign_in per account)
//   - the balance viewer wrapper, including the 加速乐 / JSL interstitial that
//     has to be executed before any AnyRouter API call answers with JSON
//
// Accounts are stored as ordinary CPA auth files of type "anyrouter", so the
// host owns the inventory (list/enable/disable/delete) and the plugin only
// needs to read it, check in, and read balances.
//
// Declared capabilities:
//
//	management_api  - panel + JSON routes for check-in, balance and accounts
//	auth_provider   - claims auth files of type "anyrouter"
//	quota_provider  - exposes the AnyRouter balance to the CPA credential page
//
// This file carries the cgo glue. It is implicitly cgo-only, so
// CGO_ENABLED=0 builds (and unit tests) stay possible: cabi.go simply is not
// compiled then, and hostrpc.go keeps its unavailable-but-safe default.
package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

// cgo cannot call a function pointer held in a struct field, so every host RPC
// goes through these trampolines.
static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	if host != nil {
		C.store_host_api(host)
		hostCallFunc = callHostCgo
		hostReady.Store(true)
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", 400))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	methodName := C.GoString(method)
	raw, errHandle := guardRPC(methodName, func() ([]byte, error) {
		return handleMethod(methodName, requestBytes)
	})
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error(), 500))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	stopBackground()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

// callHostCgo performs one host RPC over the C ABI. The response buffer is
// allocated by the host and must be released with the host free function.
func callHostCgo(method string, payload any) (json.RawMessage, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var body []byte
	if payload != nil {
		raw, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return nil, errMarshal
		}
		body = raw
	}

	var requestPtr *C.uint8_t
	if len(body) > 0 {
		cPayload := C.CBytes(body)
		if cPayload == nil {
			return nil, errHostUnavailable
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}

	var buf C.cliproxy_buffer
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(body)), &buf)
	var rawResponse []byte
	if buf.ptr != nil && buf.len > 0 {
		rawResponse = C.GoBytes(buf.ptr, C.int(buf.len))
	}
	if buf.ptr != nil {
		C.free_host_buffer(buf.ptr, buf.len)
	}
	if len(rawResponse) == 0 {
		return nil, errHostUnavailable
	}

	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if !env.OK {
		if env.Error != nil {
			return nil, &hostRPCError{Code: env.Error.Code, Message: env.Error.Message}
		}
		return nil, errHostUnavailable
	}
	if callCode != 0 {
		return nil, errHostUnavailable
	}
	return append(json.RawMessage(nil), env.Result...), nil
}
