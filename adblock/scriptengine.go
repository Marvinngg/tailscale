package adblock

import (
	"tailscale.com/adblock/script"
)

// ScriptEngine 是 script.Runtime 的包装，供 Engine 和 Addon 使用。
type ScriptEngine struct {
	rt *script.Runtime
}

// NewScriptEngine 创建脚本引擎。
func NewScriptEngine() *ScriptEngine {
	return &ScriptEngine{
		rt: script.NewRuntime(),
	}
}

// LoadScript 加载并预编译脚本。
func (se *ScriptEngine) LoadScript(name, content string) error {
	return se.rt.LoadScript(name, content)
}

// ModifyResponse 用脚本修改 HTTP 响应。
func (se *ScriptEngine) ModifyResponse(scriptName string, reqURL string, reqHeaders map[string]string, respStatus int, respHeaders map[string]string, respBody string) (string, error) {
	result, err := se.rt.ExecResponseScript(scriptName, script.Request{
		URL:     reqURL,
		Headers: reqHeaders,
	}, script.Response{
		Status:  respStatus,
		Headers: respHeaders,
		Body:    respBody,
	})
	if err != nil {
		return respBody, err
	}
	return result.Body, nil
}
