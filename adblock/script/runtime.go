// Package script 提供 JavaScript 脚本执行能力。
//
// 用于执行 Surge / QuantumultX 规则中的 script-response-body 脚本，
// 这些脚本解析 API 返回的 JSON 并精确删除广告字段。
//
// 脚本环境提供以下全局对象（兼容 Surge/QX 脚本 API）：
//   - $request: { url, headers, body }
//   - $response: { status, headers, body }
//   - $done(response): 完成回调
//   - console.log(): 日志输出
//   - JSON: 标准 JSON 对象
package script

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/grafana/sobek"
)

// Runtime JS 脚本运行时。每次执行创建独立的 VM 实例，保证线程安全。
type Runtime struct {
	mu      sync.Mutex
	scripts map[string]*sobek.Program // 预编译的脚本
}

// NewRuntime 创建脚本运行时。
func NewRuntime() *Runtime {
	return &Runtime{
		scripts: make(map[string]*sobek.Program),
	}
}

// LoadScript 加载并预编译脚本内容。
func (rt *Runtime) LoadScript(name, content string) error {
	prog, err := sobek.Compile(name, content, true)
	if err != nil {
		return fmt.Errorf("compile script %q: %w", name, err)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.scripts[name] = prog
	return nil
}

// Request 表示 HTTP 请求，传递给脚本的 $request 对象。
type Request struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Response 表示 HTTP 响应，传递给脚本的 $response 对象。
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// ExecResult 脚本执行结果。
type ExecResult struct {
	Body    string
	Headers map[string]string
	Status  int
}

// ExecResponseScript 执行 http-response 类型脚本。
// 接收原始请求和上游响应，返回修改后的结果。
func (rt *Runtime) ExecResponseScript(scriptName string, req Request, resp Response) (*ExecResult, error) {
	rt.mu.Lock()
	prog, ok := rt.scripts[scriptName]
	rt.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("script %q not loaded", scriptName)
	}

	vm := sobek.New()

	// 注入 $request 对象
	vm.Set("$request", map[string]interface{}{
		"url":     req.URL,
		"headers": req.Headers,
		"body":    req.Body,
	})

	// 注入 $response 对象
	vm.Set("$response", map[string]interface{}{
		"status":     resp.Status,
		"statusCode": resp.Status,
		"headers":    resp.Headers,
		"body":       resp.Body,
	})

	// $done() 回调——脚本通过 $done({body: ...}) 返回修改后的结果
	result := &ExecResult{
		Body:   resp.Body,
		Status: resp.Status,
	}
	doneCalled := false

	vm.Set("$done", func(call sobek.FunctionCall) sobek.Value {
		doneCalled = true
		if len(call.Arguments) == 0 {
			return sobek.Undefined()
		}

		arg := call.Arguments[0].Export()
		if m, ok := arg.(map[string]interface{}); ok {
			if body, ok := m["body"]; ok {
				switch v := body.(type) {
				case string:
					result.Body = v
				default:
					// 如果是对象/数组，序列化为 JSON
					if b, err := json.Marshal(v); err == nil {
						result.Body = string(b)
					}
				}
			}
			if status, ok := m["status"]; ok {
				if s, ok := status.(int64); ok {
					result.Status = int(s)
				}
			}
			if headers, ok := m["headers"]; ok {
				if h, ok := headers.(map[string]interface{}); ok {
					result.Headers = make(map[string]string)
					for k, v := range h {
						result.Headers[k] = fmt.Sprint(v)
					}
				}
			}
		}
		return sobek.Undefined()
	})

	// console.log 支持
	console := vm.NewObject()
	console.Set("log", func(call sobek.FunctionCall) sobek.Value {
		args := make([]interface{}, len(call.Arguments))
		for i, a := range call.Arguments {
			args[i] = a.Export()
		}
		log.Printf("[script:%s] %v", scriptName, args)
		return sobek.Undefined()
	})
	vm.Set("console", console)

	// 超时控制：脚本最多执行 5 秒
	timer := time.AfterFunc(5*time.Second, func() {
		vm.Interrupt("script execution timeout")
	})
	defer timer.Stop()

	// 执行脚本
	_, err := vm.RunProgram(prog)
	if err != nil {
		return nil, fmt.Errorf("exec script %q: %w", scriptName, err)
	}

	// 如果脚本没调用 $done()，尝试从 $response.body 获取修改
	if !doneCalled {
		respObj := vm.Get("$response")
		if respObj != nil {
			if obj := respObj.ToObject(vm); obj != nil {
				if body := obj.Get("body"); body != nil {
					result.Body = body.String()
				}
			}
		}
	}

	return result, nil
}

// ExecRequestScript 执行 http-request 类型脚本。
func (rt *Runtime) ExecRequestScript(scriptName string, req Request) (*Request, error) {
	rt.mu.Lock()
	prog, ok := rt.scripts[scriptName]
	rt.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("script %q not loaded", scriptName)
	}

	vm := sobek.New()

	vm.Set("$request", map[string]interface{}{
		"url":     req.URL,
		"headers": req.Headers,
		"body":    req.Body,
	})

	result := &req
	vm.Set("$done", func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) == 0 {
			return sobek.Undefined()
		}
		arg := call.Arguments[0].Export()
		if m, ok := arg.(map[string]interface{}); ok {
			if body, ok := m["body"].(string); ok {
				result.Body = body
			}
			if url, ok := m["url"].(string); ok {
				result.URL = url
			}
		}
		return sobek.Undefined()
	})

	timer := time.AfterFunc(5*time.Second, func() {
		vm.Interrupt("script execution timeout")
	})
	defer timer.Stop()

	_, err := vm.RunProgram(prog)
	if err != nil {
		return nil, fmt.Errorf("exec script %q: %w", scriptName, err)
	}

	return result, nil
}
