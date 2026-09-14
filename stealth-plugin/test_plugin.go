//go:build ignore

// Standalone harness that dlopen()s a built stealth.dylib and feeds it a
// canned request, so the interceptor can be exercised without starting CPA.
//
// The build tag keeps it out of `go build ./...`: it declares its own main(),
// which would otherwise collide with the plugin's main() in main.go.
//
// Run it explicitly:
//   go run -tags ignore test_plugin.go
package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*init_fn)(void*, void*);
typedef int (*call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);

int run_test(const char* lib_path, const char* method, const char* req_json, char** out_buf) {
	void* handle = dlopen(lib_path, RTLD_NOW);
	if (!handle) {
		printf("dlopen failed: %s\n", dlerror());
		return 1;
	}
	init_fn init = (init_fn)dlsym(handle, "cliproxy_plugin_init");
	call_fn call = (call_fn)dlsym(handle, "cliproxyPluginCall");
	if (!init || !call) {
		printf("dlsym failed\n");
		return 2;
	}

	// Fake struct of size at least 64 bytes
	uint8_t fake_host[128] = {0};
	uint8_t fake_plugin[128] = {0};
	init(fake_host, fake_plugin);

	cliproxy_buffer resp = {0};
	int rc = call((char*)method, (uint8_t*)req_json, strlen(req_json), &resp);
	if (rc != 0) {
		printf("call failed with rc=%d\n", rc);
		return 3;
	}

	if (resp.ptr && resp.len > 0) {
		*out_buf = malloc(resp.len + 1);
		memcpy(*out_buf, resp.ptr, resp.len);
		(*out_buf)[resp.len] = '\0';
	}
	dlclose(handle);
	return 0;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func main() {
	libPath := C.CString("/Volumes/MyPassport/Projects/codex-webui-demo/stealth-plugin/stealth.dylib")
	defer C.free(unsafe.Pointer(libPath))

	method := C.CString("plugin.register")
	defer C.free(unsafe.Pointer(method))

	reqJSON := C.CString("{}")
	defer C.free(unsafe.Pointer(reqJSON))

	var outBuf *C.char
	rc := C.run_test(libPath, method, reqJSON, &outBuf)
	if rc == 0 && outBuf != nil {
		fmt.Println("Register response:")
		fmt.Println(C.GoString(outBuf))
		C.free(unsafe.Pointer(outBuf))
	} else {
		fmt.Println("Test failed rc =", rc)
	}

	// Test request.intercept_before
	method2 := C.CString("request.intercept_before")
	defer C.free(unsafe.Pointer(method2))
	reqJSON2 := C.CString(`{"Body":"eyJzeXN0ZW0iOiJZb3UgYXJlIGEgQ2xhdWRlIGFnZW50LCBidWlsdCBvbiBBbnRocm9waWMncyBDbGF1ZGUgQWdlbnQgU0RLLiJ9"}`)
	defer C.free(unsafe.Pointer(reqJSON2))

	var outBuf2 *C.char
	rc2 := C.run_test(libPath, method2, reqJSON2, &outBuf2)
	if rc2 == 0 && outBuf2 != nil {
		fmt.Println("Intercept response:")
		fmt.Println(C.GoString(outBuf2))
		C.free(unsafe.Pointer(outBuf2))
	} else {
		fmt.Println("Intercept test failed rc =", rc2)
	}
}
