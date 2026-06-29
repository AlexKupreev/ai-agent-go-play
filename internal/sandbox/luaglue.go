// Package sandbox runs agent-authored guest scripts. The default tier is
// LuaGlue: an in-process gopher-lua interpreter with a restricted environment.
//
// Capability gating is structural: the host installs ONLY the host functions the
// grant allows. A script cannot name a function it was not granted — there is no
// ambient authority and no way to reach the broker except through an injected fn.
package sandbox

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"ai-agent-go-play/internal/capability"

	lua "github.com/yuin/gopher-lua"
)

// Parse compiles code without running it, returning a syntax error if any. Used
// by author_tool to validate authored source before approval/registration.
func Parse(code string) error {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()
	_, err := L.LoadString(code)
	return err
}

// LuaGlue runs Lua scripts, brokering effects through a capability.Broker.
// The broker may be nil for compute-only use (empty grants need no broker).
type LuaGlue struct {
	broker *capability.Broker
}

func NewLuaGlue(broker *capability.Broker) *LuaGlue {
	return &LuaGlue{broker: broker}
}

// Run executes code with the given input and grant, returning the script's
// result. The script reads its arguments from the global `input` and returns its
// answer via `return <value>` (or a global `result`).
func (g *LuaGlue) Run(ctx context.Context, code string, input map[string]any, grant *capability.GrantContext, timeout time.Duration) (string, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()
	openSafeLibs(L)
	hardenGlobals(L)

	L.SetGlobal("input", goToLua(L, input))
	g.installHostFuncs(ctx, L, grant)

	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	L.SetContext(c)

	fn, err := L.LoadString(code)
	if err != nil {
		return "", fmt.Errorf("parse error: %w", err)
	}
	L.Push(fn)
	if err := L.PCall(0, lua.MultRet, nil); err != nil {
		return "", fmt.Errorf("runtime error: %w", err)
	}

	var v lua.LValue
	if L.GetTop() >= 1 {
		v = L.Get(1)
	} else {
		v = L.GetGlobal("result")
	}
	return luaValueToString(v)
}

// installHostFuncs injects a host function for each granted capability — and
// nothing more. Ungranted effects are simply absent from the script's globals.
func (g *LuaGlue) installHostFuncs(ctx context.Context, L *lua.LState, grant *capability.GrantContext) {
	if g.broker == nil || grant == nil {
		return
	}
	b := g.broker

	if grant.Has(capability.HTTPGet) {
		L.SetGlobal("http_get", L.NewFunction(func(L *lua.LState) int {
			out, err := b.HTTPGet(ctx, grant, L.CheckString(1))
			if err != nil {
				L.RaiseError("%v", err)
			}
			L.Push(lua.LString(out))
			return 1
		}))
	}
	if grant.Has(capability.ReadFile) {
		L.SetGlobal("read_file", L.NewFunction(func(L *lua.LState) int {
			out, err := b.ReadFile(grant, L.CheckString(1))
			if err != nil {
				L.RaiseError("%v", err)
			}
			L.Push(lua.LString(out))
			return 1
		}))
	}
	if grant.Has(capability.WriteFile) {
		L.SetGlobal("write_file", L.NewFunction(func(L *lua.LState) int {
			if err := b.WriteFile(grant, L.CheckString(1), L.CheckString(2)); err != nil {
				L.RaiseError("%v", err)
			}
			return 0
		}))
	}
	if grant.Has(capability.CallTool) {
		L.SetGlobal("call_tool", L.NewFunction(func(L *lua.LState) int {
			name := L.CheckString(1)
			var args map[string]any
			if tbl, ok := L.Get(2).(*lua.LTable); ok {
				if m, isMap := tableToGo(tbl).(map[string]any); isMap {
					args = m
				}
			}
			out, err := b.CallTool(ctx, grant, name, args)
			if err != nil {
				L.RaiseError("%v", err)
			}
			L.Push(lua.LString(out))
			return 1
		}))
	}
	if grant.Has(capability.Clock) {
		L.SetGlobal("now", L.NewFunction(func(L *lua.LState) int {
			t, err := b.Now(grant)
			if err != nil {
				L.RaiseError("%v", err)
			}
			L.Push(lua.LNumber(t.Unix()))
			return 1
		}))
	}
	if grant.Has(capability.Random) {
		L.SetGlobal("random", L.NewFunction(func(L *lua.LState) int {
			buf, err := b.RandomBytes(grant, L.CheckInt(1))
			if err != nil {
				L.RaiseError("%v", err)
			}
			L.Push(lua.LString(hex.EncodeToString(buf)))
			return 1
		}))
	}
}

// openSafeLibs loads only the compute libraries (base, table, string, math).
func openSafeLibs(L *lua.LState) {
	libs := []struct {
		name string
		fn   lua.LGFunction
	}{
		{"", lua.OpenBase},
		{"table", lua.OpenTable},
		{"string", lua.OpenString},
		{"math", lua.OpenMath},
	}
	for _, lib := range libs {
		L.Push(L.NewFunction(lib.fn))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
}

// hardenGlobals removes code-loading, I/O, and OS escape hatches. os/io/debug/
// package are never opened; clearing them is belt-and-braces.
func hardenGlobals(L *lua.LState) {
	for _, name := range []string{
		"dofile", "loadfile", "load", "loadstring", "print",
		"collectgarbage", "module", "require",
		"os", "io", "debug", "package",
	} {
		L.SetGlobal(name, lua.LNil)
	}
}

func luaValueToString(v lua.LValue) (string, error) {
	if s, ok := v.(lua.LString); ok {
		return string(s), nil // return strings raw, not JSON-quoted
	}
	if v == nil || v.Type() == lua.LTNil {
		return "nil", nil
	}
	b, err := json.Marshal(luaToGo(v))
	if err != nil {
		return "", fmt.Errorf("could not serialize result: %w", err)
	}
	return string(b), nil
}

func luaToGo(v lua.LValue) any {
	switch lv := v.(type) {
	case lua.LBool:
		return bool(lv)
	case lua.LNumber:
		return float64(lv)
	case lua.LString:
		return string(lv)
	case *lua.LTable:
		return tableToGo(lv)
	default:
		if v == nil || v.Type() == lua.LTNil {
			return nil
		}
		return v.String()
	}
}

// tableToGo converts a Lua table to a Go slice (pure sequence) or map.
func tableToGo(t *lua.LTable) any {
	hasNonIntKey := false
	t.ForEach(func(k, _ lua.LValue) {
		if k.Type() != lua.LTNumber {
			hasNonIntKey = true
		}
	})

	if n := t.Len(); n > 0 && !hasNonIntKey {
		arr := make([]any, 0, n)
		for i := 1; i <= n; i++ {
			arr = append(arr, luaToGo(t.RawGetInt(i)))
		}
		return arr
	}

	m := make(map[string]any)
	t.ForEach(func(k, v lua.LValue) {
		m[k.String()] = luaToGo(v)
	})
	return m
}

// goToLua converts a Go value (decoded JSON shapes) to a Lua value.
func goToLua(L *lua.LState, v any) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case float64:
		return lua.LNumber(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case string:
		return lua.LString(val)
	case []any:
		t := L.NewTable()
		for i, e := range val {
			t.RawSetInt(i+1, goToLua(L, e))
		}
		return t
	case map[string]any:
		t := L.NewTable()
		for k, e := range val {
			t.RawSetString(k, goToLua(L, e))
		}
		return t
	default:
		return lua.LString(fmt.Sprint(val))
	}
}
