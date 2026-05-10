package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutor_ReadWriteFile(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "test.go"
	content := []byte("package main\n\nfunc hello() {\n\tprintln(\"hi\")\n}\n")

	err := e.WriteFile(path, content)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, read)
}

func TestExecutor_ReplaceSymbol(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := "package main\n\nfunc hello() {\n\tprintln(\"hi\")\n}\n"
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	oldBody, err := e.ReplaceSymbol(path, "hello", "func hello() {\n\tprintln(\"hello, world!\")\n}")
	require.NoError(t, err)
	assert.Equal(t, "func hello() {\n\tprintln(\"hi\")\n}", oldBody)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(read), "println(\"hello, world!\")")
	assert.NotContains(t, string(read), "println(\"hi\")")
}

func TestExecutor_ReplaceSymbol_FuncSignature(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "api.go"
	original := `package api

func HandleRequest(w http.ResponseWriter, r *http.Request) {
	fmt.Println("handled")
}
`
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	_, err = e.ReplaceSymbol(path, "HandleRequest", `func HandleRequest(w http.ResponseWriter, r *http.Request) {
	fmt.Println("handled with new logic")
}
`)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(read), "handled with new logic")
}

func TestExecutor_InsertAfter(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := "package main\n\nfunc hello() {\n\tprintln(\"hi\")\n}\n"
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	err = e.InsertAfter(path, "hello", "func world() {\n\tprintln(\"world\")\n}")
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(read), "func hello()")
	assert.Contains(t, string(read), "func world()")
}

func TestExecutor_DeleteSymbol(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := `package main

func hello() {
	println("hello")
}

func world() {
	println("world")
}
`
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	err = e.DeleteSymbol(path, "hello")
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(read), "func hello")
	assert.Contains(t, string(read), "func world")
}

func TestExecutor_SymbolContent(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := "package main\n\nfunc hello() {\n\tprintln(\"hi\")\n}\n"
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	body, err := e.SymbolContent(path, "hello")
	require.NoError(t, err)
	assert.Contains(t, body, "println(\"hi\")")
}

func TestExecutor_ReplaceSymbol_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	_, err := e.ReplaceSymbol("nonexistent.go", "foo", "bar")
	assert.Error(t, err)
}

func TestExecutor_ReplaceSymbol_SymbolNotFound(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := "package main\n\nfunc hello() {}\n"
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	_, err = e.ReplaceSymbol(path, "nonexistent", "bar")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "symbol")
}

func TestExecutor_Integration_ModifyThenDelete(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := `package main

func hello() {
	println("hello")
}

func world() {
	println("world")
}
`
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	// Delete world first (positions won't shift for hello)
	err = e.DeleteSymbol(path, "world")
	require.NoError(t, err)

	// Now modify hello (fresh index after delete)
	oldBody, err := e.ReplaceSymbol(path, "hello", `func hello() {
	println("modified hello")
}
`)
	require.NoError(t, err)
	assert.Contains(t, oldBody, "println(\"hello\")")

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	content := string(read)
	assert.Contains(t, content, "modified hello")
	assert.NotContains(t, content, "func world")
}

func TestExecutor_Integration_ModifyThenInsert(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "server.go"
	original := `package main

func Start(addr string) {
	println("starting", addr)
}
`
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	// Modify Start
	_, err = e.ReplaceSymbol(path, "Start", `func Start(addr string) {
	println("starting on", addr)
}
`)
	require.NoError(t, err)

	// Insert after Start (fresh index after modify)
	err = e.InsertAfter(path, "Start", `func Stop() {
	println("stopping")
}
`)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	content := string(read)
	assert.Contains(t, content, "starting on")
	assert.Contains(t, content, "func Stop")
}

func TestExecutor_TrimNewlines(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello\n", "hello"},
		{"hello\n\n", "hello"},
		{"hello", "hello"},
		{"  hello\n", "  hello"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, TrimNewlines(tt.input))
	}
}

func TestExecutor_RunCommand(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	script := filepath.Join(dir, "test.sh")
	err := os.WriteFile(script, []byte("#!/bin/bash\necho hello\n"), 0o755)
	require.NoError(t, err)

	output, err := e.RunCommand(dir, "bash", script)
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(output))
}

func TestExecutor_IndexFreshness(t *testing.T) {
	// Verify that the executor re-indexes before each operation,
	// so modifications from previous turns are reflected.
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := `package main

func hello() {
	println("original")
}

func world() {
	println("world")
}
`
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	// First call: replace hello
	_, err = e.ReplaceSymbol(path, "hello", `func hello() {
	println("first")
}
`)
	require.NoError(t, err)

	// Second call: replace world (would fail with stale indices)
	_, err = e.ReplaceSymbol(path, "world", `func world() {
	println("second")
}
`)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	content := string(read)
	assert.Contains(t, content, "println(\"first\")")
	assert.Contains(t, content, "println(\"second\")")
}

func TestExecutor_Integration_DeleteThenModify(t *testing.T) {
	// Delete a symbol, then modify another in the same file.
	// This tests that the re-index picks up the size change.
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := `package main

func foo() {
	println("foo")
}

func bar() {
	println("bar")
}

func baz() {
	println("baz")
}
`
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	// Delete bar (middle function)
	err = e.DeleteSymbol(path, "bar")
	require.NoError(t, err)

	// Modify baz — fresh index should find correct byte range
	oldBody, err := e.ReplaceSymbol(path, "baz", `func baz() {
	println("modified baz")
}
`)
	require.NoError(t, err)
	assert.Contains(t, oldBody, "println(\"baz\")")

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	content := string(read)
	assert.Contains(t, content, "modified baz")
	assert.NotContains(t, content, "func bar")
	assert.Contains(t, content, "func foo") // foo should remain
}

func TestExecutor_SymbolNotFound(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := "package main\n\nfunc hello() {}\n"
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	_, err = e.SymbolContent(path, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExecutor_CircularReplace(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := "package main\n\nfunc hello() {\n\tprintln(\"1\")\n}\n"
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	// Replace with something longer
	_, err = e.ReplaceSymbol(path, "hello", "func hello() {\n\tprintln(\"2\")\n}")
	require.NoError(t, err)

	// Replace again with something even longer
	_, err = e.ReplaceSymbol(path, "hello", "func hello() {\n\tprintln(\"3\")\n}")
	require.NoError(t, err)

	read, _ := e.ReadFile(path)
	assert.Contains(t, string(read), "println(\"3\")")
	assert.NotContains(t, string(read), "println(\"2\")")
}

func TestExecutor_ReplaceAndInsertSameFile(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := `package main

func hello() {
	println("hello")
}
`
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	// Modify hello
	_, err = e.ReplaceSymbol(path, "hello", `func hello() {
	println("modified hello")
}
`)
	require.NoError(t, err)

	// Insert after hello
	err = e.InsertAfter(path, "hello", `func world() {
	println("world")
}
`)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	content := string(read)
	assert.Contains(t, content, "modified hello")
	assert.Contains(t, content, "func world")
}

// --- Integration tests using toolbridge patterns ---

func TestExecutor_ChainedOps(t *testing.T) {
	// Simulates LLM calling multiple tools in sequence:
	// 1. Replace a function
	// 2. Add a new function after it
	// 3. Delete another function
	// 4. Replace a remaining function
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := `package main

func alpha() {
	println("alpha")
}

func beta() {
	println("beta")
}

func gamma() {
	println("gamma")
}

func delta() {
	println("delta")
}
`
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	// Step 1: Replace alpha
	_, err = e.ReplaceSymbol(path, "alpha", `func alpha() {
	println("alpha-modified")
}
`)
	require.NoError(t, err)

	// Step 2: Insert after alpha
	err = e.InsertAfter(path, "alpha", `func epsilon() {
	println("epsilon")
}
`)
	require.NoError(t, err)

	// Step 3: Delete beta
	err = e.DeleteSymbol(path, "beta")
	require.NoError(t, err)

	// Step 4: Replace delta
	_, err = e.ReplaceSymbol(path, "delta", `func delta() {
	println("delta-modified")
}
`)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	content := string(read)

	assert.Contains(t, content, "alpha-modified")
	assert.Contains(t, content, "func epsilon")
	assert.NotContains(t, content, "func beta")
	assert.Contains(t, content, "delta-modified")
	assert.Contains(t, content, "func gamma")
	assert.Contains(t, content, `println("gamma")`)
}

func TestExecutor_ReplaceMultipleTimes(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	err := e.WriteFile(path, []byte("package main\n\nfunc hello() {\n\tprintln(\"0\")\n}\n"))
	require.NoError(t, err)

	for i := 1; i <= 5; i++ {
		_, err = e.ReplaceSymbol(path, "hello",
			fmt.Sprintf(`func hello() {
	println("%d")
}`, i))
		require.NoError(t, err)
	}

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(read), `println("5")`)
}
