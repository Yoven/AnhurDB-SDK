package client

// deleted_surface_test.go — one domain: proving that the surface 3.0.0 DELETED
// is really gone, by compiling a caller that uses it and requiring the build to
// FAIL.
//
// Junior Tip [why absence is not a proof, 2026-09-14]: a grep that finds no
// `_ = opts` proves only that the string is gone. The defect was never the
// string — it was that `memory.Recent(ctx, 10, WithLimit(5))` COMPILED and did
// nothing. The only assertion that speaks to that is a compiler refusing the
// exact line. This test builds testdata/deleted_surface_callers.go.txt in a
// throwaway module that `replace`s this SDK to the working tree, and fails if
// the build succeeds.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// sdkModuleRoot returns the directory holding this SDK's go.mod, derived from
// this test file's own location so the test is independent of the working
// directory the runner chose.
func sdkModuleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	moduleRoot := filepath.Dir(filepath.Dir(thisFile)) // client/ -> golang/
	if _, statErr := os.Stat(filepath.Join(moduleRoot, "go.mod")); statErr != nil {
		t.Fatalf("go.mod not found at %s: %v", moduleRoot, statErr)
	}
	return moduleRoot
}

// TestDeletedSurfaceDoesNotCompile is the compile-fail proof for §1 (the 16
// methods that lost `opts ...ReadOption`), §3 (the phantom struct fields) and
// §9 (the three dead model types).
func TestDeletedSurfaceDoesNotCompile(t *testing.T) {
	if _, lookErr := exec.LookPath("go"); lookErr != nil {
		t.Skip("go toolchain not on PATH; cannot run the compile-fail proof")
	}
	moduleRoot := sdkModuleRoot(t)

	// The probe module lives OUTSIDE the SDK tree on purpose: a package inside
	// it would be picked up by `go build ./...` and break the ordinary build.
	probeDir := t.TempDir()
	goModContents := "module anhurdb.parity/probe\n\ngo 1.24\n\n" +
		"require github.com/Yoven/AnhurDB-SDK/v2/golang/v2 v2.0.0\n\n" +
		"replace github.com/Yoven/AnhurDB-SDK/v2/golang/v2 => " + moduleRoot + "\n"
	if writeErr := os.WriteFile(filepath.Join(probeDir, "go.mod"), []byte(goModContents), 0o644); writeErr != nil {
		t.Fatalf("writing probe go.mod: %v", writeErr)
	}

	corpusPath := filepath.Join(moduleRoot, "client", "testdata", "deleted_surface_callers.go.txt")
	corpus, readErr := os.ReadFile(corpusPath)
	if readErr != nil {
		t.Fatalf("reading %s: %v", corpusPath, readErr)
	}
	if writeErr := os.WriteFile(filepath.Join(probeDir, "probe.go"), corpus, 0o644); writeErr != nil {
		t.Fatalf("writing probe.go: %v", writeErr)
	}

	// -gcflags=-e removes the compiler's 10-error cap. Without it the build
	// stops at "too many errors" and the later symbols in the corpus would be
	// unproven — a green test that checked only the first ten deletions.
	buildCommand := exec.Command("go", "build", "-gcflags=-e", "./...")
	buildCommand.Dir = probeDir
	buildCommand.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	buildOutput, buildErr := buildCommand.CombinedOutput()

	if buildErr == nil {
		t.Fatalf("the deleted 3.0.0 surface still compiles — every line in %s "+
			"is supposed to be a compile error:\n%s", corpusPath, buildOutput)
	}

	// Junior Tip [a build that fails for the WRONG reason proves nothing, and a
	// substring check is not enough]: a typo in the probe module or an
	// unreachable replace path fails the build too. The first version of this
	// test only looked for symbol NAMES in the output — and a mutation that
	// gave Recent its discarded `opts ...ReadOption` back SURVIVED, because the
	// word "Recent" was still in the output from the RecentMemories line next to
	// it. So the assertion is per-LINE: every statement in the corpus must have
	// produced a compile error at its own line number.
	outputText := string(buildOutput)
	reportedLines := map[int]bool{}
	for _, outputLine := range strings.Split(outputText, "\n") {
		trimmed := strings.TrimPrefix(strings.TrimSpace(outputLine), "./probe.go:")
		if trimmed == strings.TrimSpace(outputLine) {
			continue
		}
		lineText, _, _ := strings.Cut(trimmed, ":")
		lineNumber, convErr := strconv.Atoi(lineText)
		if convErr == nil {
			reportedLines[lineNumber] = true
		}
	}

	for lineNumber, statement := range corpusStatements(string(corpus)) {
		if !reportedLines[lineNumber] {
			t.Fatalf("probe.go:%d still COMPILES: %s\n"+
				"that surface was supposed to be deleted in 3.0.0.\nbuild output:\n%s",
				lineNumber, statement, outputText)
		}
	}
}

// corpusStatements maps each statement line of the corpus body to its text.
// Comments, blank lines, imports and the two func headers are skipped: only the
// lines that exercise deleted surface must fail to compile.
func corpusStatements(corpus string) map[int]string {
	statements := map[int]string{}
	insideBody := false
	for index, rawLine := range strings.Split(corpus, "\n") {
		lineNumber := index + 1
		trimmed := strings.TrimSpace(rawLine)
		if strings.HasPrefix(trimmed, "func mustNotCompile") {
			insideBody = true
			continue
		}
		if insideBody && trimmed == "}" {
			break
		}
		if !insideBody || trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		statements[lineNumber] = trimmed
	}
	return statements
}
