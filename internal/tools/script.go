package tools

// Authored Script tools share one contract so a tool behaves identically when
// smoke-tested and when later invoked:
//
//   - The tool body (`code`) is the inside of `function tool(input) ... end`.
//     It reads its arguments from `input` and ends with `return <value>`.
//   - At runtime the executor runs WrapScript(code): the body is defined as
//     `tool` and immediately called with the live `input`, returning its value.
//   - The smoke `test` runs WrapTest(code, test): the same `tool` function is in
//     scope, and the test calls it with sample inputs and `return true` on
//     success (or raises/asserts on failure).
//
// Keeping both forms derived from the same body means the test exercises exactly
// the code that will run in production.

// WrapScript produces the runtime form: define the body as tool(input) and call
// it with the live input.
func WrapScript(code string) string {
	return "local function tool(input)\n" + code + "\nend\nreturn tool(input)\n"
}

// WrapTest produces the smoke-test form: define the body as tool(input), then run
// the test, which must return a truthy value (raises/asserts on failure).
func WrapTest(code, test string) string {
	return "local function tool(input)\n" + code + "\nend\n" + test + "\n"
}
