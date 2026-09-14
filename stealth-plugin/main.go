package main

/*
#include <stdint.h>
#include <stdlib.h>

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
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginName    = "stealth"
	pluginVersion = "1.0.0"
)

var hostAPI *C.cliproxy_host_api

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPI = host
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
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func writeResponse(buf *C.cliproxy_buffer, payload []byte) {
	if buf == nil {
		return
	}
	if len(payload) == 0 {
		buf.ptr = nil
		buf.len = 0
		return
	}
	cBytes := C.CBytes(payload)
	buf.ptr = cBytes
	buf.len = C.size_t(len(payload))
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(stealthRegistration())
	case pluginabi.MethodRequestInterceptBefore, pluginabi.MethodRequestInterceptAfter:
		return handleRequestIntercept(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	RequestInterceptor bool `json:"request_interceptor"`
}

func stealthRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:        pluginName,
			Version:     pluginVersion,
			Author:      "codex-webui",
			GitHubRepository: "https://github.com/stealth-plugin",
		},
		Capabilities: registrationCapability{
			RequestInterceptor: true,
		},
	}
}

type rpcRequestInterceptRequest struct {
	pluginapi.RequestInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// Rules for neutralizing known blocklisted phrases across upstream providers.
// 微调一两个词或字母，既彻底打破上游 WAF / 安全特征指纹的精确字符串匹配，又 100% 保留大模型理解语义。
var sanitizationRules = []struct {
	name    string
	blocked []byte
	safe    []byte
}{
	// --- Claude Code & Claude Agent SDK ---
	{
		name:    "Claude Agent SDK Opener (Google Antigravity 429)",
		blocked: []byte("You are a Claude agent, built on Anthropic's Claude Agent SDK."),
		safe:    []byte("You are a Claude agent, built on Anthropix's Claude Agent SDK."),
	},
	{
		name:    "Claude Code CLI Identity (Tencent CodeBuddy 400)",
		blocked: []byte("You are Claude Code, Anthropic's official CLI for Claude."),
		safe:    []byte("You are Claude Code, Anthropic's official CLI tool for Claude."),
	},
	{
		name:    "Claude Code SDK Runtime Identity",
		blocked: []byte("You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."),
		safe:    []byte("You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."),
	},
	{
		name:    "Claude Code Git Branch Injection",
		blocked: []byte("Main branch (you will usually use this for PRs)"),
		safe:    []byte("Default branch (you will usually use this for PRs)"),
	},

	// --- DeepSeek Harness (DSH) ---
	{
		name:    "DeepSeek Harness Fixed Opener",
		blocked: []byte("You are an AI agent powered by DeepSeek Harness."),
		safe:    []byte("You are an AI agent powered by DeepSeek Engine."),
	},
	{
		name:    "DeepSeek Harness Persona Prefix",
		blocked: []byte("You are a coding agent powered by the "),
		safe:    []byte("You are a coding assistant powered by the "),
	},

	// --- OpenAI Codex ---
	{
		name:    "OpenAI Codex Primary Agent Identity",
		blocked: []byte("You are Codex, an OpenAI general-purpose agentic assistant that helps the user complete tasks across coding, browsing, apps, documents, research, and other digital workflows."),
		safe:    []byte("You are Codex, an AI general-purpose agentic assistant that helps the user complete tasks across coding, browsing, apps, documents, research, and other digital workflows."),
	},
	{
		name:    "OpenAI Codex Root Team Lead",
		blocked: []byte("You are `/root`, the primary agent in a team of agents collaborating to fulfill the user's goals."),
		safe:    []byte("You are `root`, the primary agent in a team of agents collaborating to fulfill the user's goals."),
	},
	{
		name:    "OpenAI Codex Action Reviewer Context",
		blocked: []byte("The following is the Codex agent history whose request action you are assessing."),
		safe:    []byte("The following is the coding agent history whose request action you are assessing."),
	},

	// --- Cursor & GitHub Copilot ---
	{
		name:    "Cursor Programming Assistant Identity",
		blocked: []byte("You are an expert AI programming assistant, including writing code, debugging, and explaining concepts."),
		safe:    []byte("You are an expert programming assistant, including writing code, debugging, and explaining concepts."),
	},
	{
		name:    "Cursor Editor Identity",
		blocked: []byte("You are Cursor, an AI-powered code editor."),
		safe:    []byte("You are an AI-powered code editor."),
	},
	{
		name:    "GitHub Copilot Pair Programmer Identity",
		blocked: []byte("You are GitHub Copilot, an AI pair programmer."),
		safe:    []byte("You are an AI pair programmer."),
	},
}

func sanitizeBody(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	changed := false
	out := body
	for _, rule := range sanitizationRules {
		if bytes.Contains(out, rule.blocked) {
			out = bytes.ReplaceAll(out, rule.blocked, rule.safe)
			changed = true
		}
	}
	return out, changed
}

func handleRequestIntercept(raw []byte) ([]byte, error) {
	fmt.Printf("[STEALTH_DEBUG] handleRequestIntercept called! len=%d\n", len(raw))
	if len(raw) < 500 {
		fmt.Printf("[STEALTH_DEBUG] raw: %s\n", string(raw))
	}
	if len(raw) == 0 {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}
	var req rpcRequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode request intercept: %w", err)
	}

	cleaned, modified := sanitizeBody(req.Body)
	if modified {
		fmt.Printf("[STEALTH_INTERCEPT] Matched and cleaned blocked prompt! len before=%d, after=%d\n", len(req.Body), len(cleaned))
		return okEnvelope(pluginapi.RequestInterceptResponse{
			Body: cleaned,
		})
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{})
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{
		OK:    false,
		Error: &envelopeError{Code: code, Message: message},
	})
	return raw
}
