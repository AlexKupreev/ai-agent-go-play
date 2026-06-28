package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// NewRunCode returns a tool that runs a short Lua script for computation and
// data shaping, and returns its result.
//
// Phase 1 sandboxing: the script is compute-only — no filesystem, network, OS,
// or I/O libraries are exposed, and execution is bounded by `timeout`. It has
// NO host functions (no brokered effects); it only transforms its inputs. When
// the capability broker lands (Phase 2), this tier graduates to running
// agent-authored code with granted capabilities.
func NewRunCode(timeout time.Duration) Tool {
	return Tool{
		Name: "run_code",
		Description: "Execute a short Lua script for computation and data shaping, returning its result. " +
			"Sandboxed: no filesystem, network, OS, or I/O access — only pure computation with the " +
			"string, table, and math libraries. End the script with `return <value>` (string, number, " +
			"boolean, or table); tables are returned as JSON. Prefer this over shelling out for " +
			"calculations, parsing, and transforming data.",
		Parameters: map[string]any{
			"code": map[string]any{
				"type":        "string",
				"description": "Lua source to execute; end with `return <value>`",
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			code, ok := args["code"].(string)
			if !ok {
				return "", fmt.Errorf("code must be a string")
			}
			out, err := runLua(ctx, code, timeout)
			if err != nil {
				// Feed errors back to the model as content (like shell) so it can adapt.
				return fmt.Sprintf("error: %v", err), nil
			}
			return out, nil
		},
	}
}

// runLua executes code in a fresh, restricted Lua state and returns its result.
func runLua(ctx context.Context, code string, timeout time.Duration) (string, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()
	openSafeLibs(L)
	hardenGlobals(L)

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

	// Prefer an explicit `return <value>`; fall back to a global named `result`.
	var v lua.LValue
	if L.GetTop() >= 1 {
		v = L.Get(1)
	} else {
		v = L.GetGlobal("result")
	}
	return luaValueToString(v)
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

// hardenGlobals removes code-loading, I/O, and OS escape hatches. The os/io/
// debug/package libraries are never opened; clearing them is belt-and-braces.
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
