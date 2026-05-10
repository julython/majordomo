package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/julython/majordomo/internal/repomap/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeSymbol(id, file, name string, start, end uint32) *graph.Symbol {
	return &graph.Symbol{
		ID:        graph.SymbolID(id),
		Name:      name,
		Kind:      graph.KindFunction,
		File:      file,
		Language:  graph.LangGo,
		StartByte: start,
		EndByte:   end,
		StartLine: 1,
		EndLine:   1,
	}
}

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

	// "package main\n\n" = 14 bytes, then hello function = 32 bytes (14..46)
	path := "main.go"
	original := "package main\n\nfunc hello() {\n\tprintln(\"hi\")\n}\n"
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	sym := makeSymbol("main.go::hello", path, "hello", 14, 46)
	err = e.ReplaceSymbol(path, sym, "func hello() {\n\tprintln(\"hello, world!\")\n}\n")
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "package main\n\nfunc hello() {\n\tprintln(\"hello, world!\")\n}\n", string(read))
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

	sym := makeSymbol("api.go::HandleRequest", path, "HandleRequest", 18, 86)
	err = e.ReplaceSymbol(path, sym, `func HandleRequest(w http.ResponseWriter, r *http.Request) {
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

	sym := makeSymbol("main.go::hello", path, "hello", 14, 46)
	err = e.InsertAfter(path, sym, "func world() {\n\tprintln(\"world\")\n}")
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

	// hello: bytes 14..49, world: bytes 49..84
	helloSym := makeSymbol("main.go::hello", path, "hello", 14, 49)
	err = e.DeleteSymbol(path, helloSym)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(read), "func hello")
	assert.Contains(t, string(read), "func world")
}

func TestExecutor_ReplaceSymbol_TamperedFile(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	path := "main.go"
	original := "func foo() {}\n"
	err := e.WriteFile(path, []byte(original))
	require.NoError(t, err)

	sym := makeSymbol("main.go::foo", path, "foo", 0, 200)
	err = e.ReplaceSymbol(path, sym, "func bar() {}\n")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds file size")
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

	// Delete world first (it comes after hello, so positions won't shift)
	worldSym := makeSymbol("main.go::world", path, "world", 49, 84)
	err = e.DeleteSymbol(path, worldSym)
	require.NoError(t, err)

	// Now modify hello
	helloSym := makeSymbol("main.go::hello", path, "hello", 14, 49)
	err = e.ReplaceSymbol(path, helloSym, `func hello() {
	println("modified hello")
}
`)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	content := string(read)
	assert.Contains(t, content, "modified hello")
	assert.NotContains(t, content, "func world")
}

func TestExecutor_Integration_AddAfter(t *testing.T) {
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

	startSym := makeSymbol("server.go::Start", path, "Start", 13, 52)
	err = e.InsertAfter(path, startSym, `func Stop() {
	println("stopping")
}
`)
	require.NoError(t, err)

	read, err := e.ReadFile(path)
	require.NoError(t, err)
	content := string(read)
	assert.Contains(t, content, "func Start")
	assert.Contains(t, content, "func Stop")
}

func TestExecutor_TrimTrailingWhitespace(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"hello\n", "hello"},
		{"hello\n\n", "hello"},
		{"  hello\n", "  hello"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, TrimLeadingWhitespace(tt.input))
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
