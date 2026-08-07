package core

import (
	"os"
	"path/filepath"
	"testing"
)

// envfile_test.go — os primeiros testes que o carregamento de configuração do
// plugin já teve. A ausência deles é parte da história: o produto vendido como
// memória soberana passou 12,8 dias sem gravar nada porque a chave nunca chegava
// ao processo, e não havia um único teste cobrindo esse caminho.

func writeEnvFile(t *testing.T, contents string) string {
	t.Helper()
	stateDir := t.TempDir()
	envPath := filepath.Join(stateDir, envFileName)
	if writeErr := os.WriteFile(envPath, []byte(contents), 0o600); writeErr != nil {
		t.Fatalf("writing env fixture: %v", writeErr)
	}
	return stateDir
}

// TestLoadEnvFileWithoutExport is THE regression: the exact file shape that
// caused the 2026-07-18 blackout. Sourcing it from a POSIX shell leaves the
// variables shell-local and the child process sees nothing; reading the file
// directly must work regardless.
func TestLoadEnvFileWithoutExport(t *testing.T) {
	stateDir := writeEnvFile(t, "ANHUR_API_KEY=chave-sem-export\nANHUR_URL=https://exemplo\n")
	t.Setenv("ANHUR_API_KEY", "")
	os.Unsetenv("ANHUR_API_KEY")
	os.Unsetenv("ANHUR_URL")

	injected, loadErr := loadEnvFileInto(filepath.Join(stateDir, envFileName))
	if loadErr != nil {
		t.Fatalf("loadEnvFileInto: %v", loadErr)
	}
	if injected != 2 {
		t.Errorf("injected = %d, want 2", injected)
	}
	if got := os.Getenv("ANHUR_API_KEY"); got != "chave-sem-export" {
		t.Errorf("ANHUR_API_KEY = %q — o arquivo SEM export tem de funcionar; foi ele que causou o blackout de 12,8 dias", got)
	}
}

// TestLoadEnvFileWithExport proves the `export ` prefix is tolerated, so a file
// that DOES work under shell sourcing keeps working here.
func TestLoadEnvFileWithExport(t *testing.T) {
	stateDir := writeEnvFile(t, "export ANHUR_API_KEY=chave-com-export\n")
	os.Unsetenv("ANHUR_API_KEY")

	if _, loadErr := loadEnvFileInto(filepath.Join(stateDir, envFileName)); loadErr != nil {
		t.Fatalf("loadEnvFileInto: %v", loadErr)
	}
	if got := os.Getenv("ANHUR_API_KEY"); got != "chave-com-export" {
		t.Errorf("ANHUR_API_KEY = %q, want chave-com-export", got)
	}
}

// TestEnvironmentWinsOverFile pins the precedence rule: an explicit environment
// variable is never overwritten, so per-invocation overrides (CI, tests) keep
// working.
func TestEnvironmentWinsOverFile(t *testing.T) {
	stateDir := writeEnvFile(t, "ANHUR_API_KEY=do-arquivo\n")
	t.Setenv("ANHUR_API_KEY", "do-ambiente")

	if _, loadErr := loadEnvFileInto(filepath.Join(stateDir, envFileName)); loadErr != nil {
		t.Fatalf("loadEnvFileInto: %v", loadErr)
	}
	if got := os.Getenv("ANHUR_API_KEY"); got != "do-ambiente" {
		t.Errorf("ANHUR_API_KEY = %q — o ambiente do processo tem de vencer o arquivo", got)
	}
}

// TestMalformedLinesDoNotAbortTheLoad: a junk line must not cost us the key on
// the next line. Dropping the key because of a stray line would trade one
// silent-loss bug for another.
func TestMalformedLinesDoNotAbortTheLoad(t *testing.T) {
	stateDir := writeEnvFile(t, "# comentario\nlinha solta sem igual\n\nANHUR_API_KEY=\"entre aspas\"\n")
	os.Unsetenv("ANHUR_API_KEY")

	if _, loadErr := loadEnvFileInto(filepath.Join(stateDir, envFileName)); loadErr != nil {
		t.Fatalf("loadEnvFileInto: %v", loadErr)
	}
	if got := os.Getenv("ANHUR_API_KEY"); got != "entre aspas" {
		t.Errorf("ANHUR_API_KEY = %q — linha malformada nao pode derrubar a chave seguinte, e aspas devem sair", got)
	}
}

// TestMissingEnvFileIsNotFatal: a fresh install has no file yet; the loader must
// report the error and let the caller decide, never panic.
func TestMissingEnvFileIsNotFatal(t *testing.T) {
	injected, loadErr := loadEnvFileInto(filepath.Join(t.TempDir(), "nao-existe"))
	if loadErr == nil {
		t.Error("arquivo ausente deve devolver erro para o chamador registrar a causa")
	}
	if injected != 0 {
		t.Errorf("injected = %d, want 0", injected)
	}
}

// TestAPIKeySourceClassification pins the diagnostic that was missing: the same
// invocation could work or skip depending on the launching shell, and nothing
// recorded which. Without this, the next diagnosis is guesswork again.
func TestAPIKeySourceClassification(t *testing.T) {
	if got := apiKeySource(true, "k"); got != KeySourceEnvironment {
		t.Errorf("chave herdada do ambiente = %q, want %q", got, KeySourceEnvironment)
	}
	if got := apiKeySource(false, "k"); got != KeySourceFile {
		t.Errorf("chave vinda do arquivo = %q, want %q", got, KeySourceFile)
	}
	if got := apiKeySource(false, ""); got != KeySourceMissing {
		t.Errorf("sem chave = %q, want %q", got, KeySourceMissing)
	}
}

// TestExplicitEnvFilePathOverride proves ANHUR_ENV_FILE redirects the loader —
// needed for testing and for installs that keep config elsewhere.
func TestExplicitEnvFilePathOverride(t *testing.T) {
	customDir := t.TempDir()
	customPath := filepath.Join(customDir, "custom.env")
	if writeErr := os.WriteFile(customPath, []byte("ANHUR_API_KEY=x\n"), 0o600); writeErr != nil {
		t.Fatalf("writing fixture: %v", writeErr)
	}
	t.Setenv("ANHUR_ENV_FILE", customPath)

	if got := resolveEnvFilePath("/qualquer/state/dir"); got != customPath {
		t.Errorf("resolveEnvFilePath = %q, want %q", got, customPath)
	}
}

// TestEmptyEnvVarDoesNotMaskTheFileKey pins the blackout shape found by the
// cross-language parity harness on 2026-07-31: a variable that EXISTS but is empty
// (a stray `export ANHUR_API_KEY=` in a shell rc, an empty `env:` in a manifest)
// used to suppress a perfectly valid key sitting in the env file — silently, with
// the log blaming "missing". Python never had this bug; Go disagreed with itself,
// since envOr() already treated empty as absent for every other variable.
func TestEmptyEnvVarDoesNotMaskTheFileKey(t *testing.T) {
	stateDir := t.TempDir()
	envFilePath := filepath.Join(stateDir, "env")
	if writeErr := os.WriteFile(envFilePath, []byte("ANHUR_API_KEY=key-from-file\n"), 0o600); writeErr != nil {
		t.Fatalf("writing env file: %v", writeErr)
	}
	t.Setenv("ANHUR_API_KEY", "") // definida — e vazia

	if _, loadErr := loadEnvFileInto(envFilePath); loadErr != nil {
		t.Fatalf("loadEnvFileInto: %v", loadErr)
	}
	if got := os.Getenv("ANHUR_API_KEY"); got != "key-from-file" {
		t.Fatalf("ANHUR_API_KEY = %q, want the file value — an empty variable "+
			"silenced the memory while a valid key sat on disk", got)
	}
	// E o log tem de apontar para o ARQUIVO, não para o ambiente: numa investigação
	// esta linha é a única pista, e mandar procurar no shell custa horas.
	if source := apiKeySource(false, os.Getenv("ANHUR_API_KEY")); source != KeySourceFile {
		t.Errorf("key source = %q, want %q", source, KeySourceFile)
	}
}

// TestNonEmptyEnvVarStillWins guards the other direction: the override that CI,
// tests and `ANHUR_URL=... plugin recall` depend on must keep working.
func TestNonEmptyEnvVarStillWins(t *testing.T) {
	stateDir := t.TempDir()
	envFilePath := filepath.Join(stateDir, "env")
	if writeErr := os.WriteFile(envFilePath, []byte("ANHUR_API_KEY=key-from-file\n"), 0o600); writeErr != nil {
		t.Fatalf("writing env file: %v", writeErr)
	}
	t.Setenv("ANHUR_API_KEY", "key-from-environment")

	if _, loadErr := loadEnvFileInto(envFilePath); loadErr != nil {
		t.Fatalf("loadEnvFileInto: %v", loadErr)
	}
	if got := os.Getenv("ANHUR_API_KEY"); got != "key-from-environment" {
		t.Errorf("ANHUR_API_KEY = %q, want the environment value (override broke)", got)
	}
}
