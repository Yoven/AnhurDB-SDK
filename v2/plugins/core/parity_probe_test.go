package core

// parity_probe_test.go — the GO ARM of the cross-language parity harness.
//
// The harness itself (scenarios, fixtures, runner, comparator) lives in
// ../tests/parity/. This file only OBSERVES: it runs each shared scenario against the
// real product functions in this package and writes a language-neutral observation
// JSON. The comparator decides pass/fail — nothing here asserts, so a divergence is
// reported once, by one component, in one place.
//
// Junior Tip [why this file sits in core/ instead of tests/parity/, 2026-07-30]: the
// three rules under test — loadConfig, flushQueue, splitIntoChunks — are UNEXPORTED.
// Go has no way to reach a package's unexported identifiers from another directory, so
// the alternative would be to re-implement them next to the harness and compare a copy
// against a copy: a parity check that proves nothing about the shipped binary, which is
// the single worst outcome for a tool whose whole job is to catch drift. It is a _test.go
// file, so it is never linked into anhur-claude-memory or anhur-hermes-memory.
//
// Junior Tip [it SKIPS unless the harness drives it]: `go test ./...` during normal
// development must not depend on a scenario directory existing. The two ANHUR_PARITY_*
// variables are the harness's handshake; without them this test skips with a message
// that names the runner. A skip here can never be mistaken for a green parity run,
// because the RUNNER — not this test — decides the verdict, and it fails loudly when
// the observation file it asked for does not appear.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// parityBodyMarker is embedded in every queued fixture body so an observation can name
// WHICH item was attempted, persisted, or quarantined without depending on either
// implementation's on-disk format (Go: a .txt chunk whose session lives in the header
// text; Python: a .json envelope whose session lives in a field).
const parityBodyMarker = "BODY-"

// parityScenarioFile is the shared fixture shape. Only the fields the probe needs are
// decoded; `expect`, `why` and `on_failure` belong to the comparator.
type parityScenarioFile struct {
	Name  string          `json:"name"`
	Rule  string          `json:"rule"`
	Given json.RawMessage `json:"given"`
}

// parityConfigGiven describes an env-file + process-environment situation.
type parityConfigGiven struct {
	// EnvFileLocation: "state_dir" (<stateDir>/env, the production layout),
	// "explicit" (elsewhere, pointed at by ANHUR_ENV_FILE) or "none" (no file at all).
	EnvFileLocation string            `json:"env_file_location"`
	EnvFileLines    []string          `json:"env_file_lines"`
	ProcessEnv      map[string]string `json:"process_env"`
}

// parityQueueItem is one unit already sitting in the on-disk queue. Session "" means the
// unit carries NO provable owner — the quarantine case.
type parityQueueItem struct {
	ID      string `json:"id"`
	Session string `json:"session"`
	Body    string `json:"body"`
}

// parityQueueGiven describes a queue and how the backend behaves during the drain.
type parityQueueGiven struct {
	Items []parityQueueItem `json:"items"`
	// Transport: "ok" (everything succeeds), "down" (every write fails) or
	// "reject:<id>" (that one item is permanently rejected, the rest succeed).
	Transport  string `json:"transport"`
	DrainTwice bool   `json:"drain_twice"`
}

// parityChunkGiven points at a shared fixture file and the chunk size limit.
type parityChunkGiven struct {
	Fixture  string `json:"fixture"`
	MaxChars int    `json:"max_chars"`
}

// TestParityProbe runs every scenario in ANHUR_PARITY_SCENARIOS and writes the
// observations to ANHUR_PARITY_OUT. Driven by ../tests/parity/run_parity.sh.
func TestParityProbe(t *testing.T) {
	scenarioDirectory := os.Getenv("ANHUR_PARITY_SCENARIOS")
	outputPath := os.Getenv("ANHUR_PARITY_OUT")
	if scenarioDirectory == "" || outputPath == "" {
		t.Skip("parity probe idle: set ANHUR_PARITY_SCENARIOS and ANHUR_PARITY_OUT, " +
			"or just run ../tests/parity/run_parity.sh")
	}

	scenarioFiles, readDirErr := os.ReadDir(scenarioDirectory)
	if readDirErr != nil {
		t.Fatalf("cannot read scenario directory %s: %v", scenarioDirectory, readDirErr)
	}
	fixtureDirectory := filepath.Join(filepath.Dir(scenarioDirectory), "fixtures")

	observations := map[string]interface{}{}
	scenarioCount := 0
	for _, scenarioFile := range scenarioFiles {
		if scenarioFile.IsDir() || !strings.HasSuffix(scenarioFile.Name(), ".json") {
			continue
		}
		scenarioPath := filepath.Join(scenarioDirectory, scenarioFile.Name())
		rawScenario, readErr := os.ReadFile(scenarioPath)
		if readErr != nil {
			t.Fatalf("cannot read scenario %s: %v", scenarioPath, readErr)
		}
		var scenario parityScenarioFile
		if unmarshalErr := json.Unmarshal(rawScenario, &scenario); unmarshalErr != nil {
			t.Fatalf("scenario %s is not valid JSON: %v", scenarioPath, unmarshalErr)
		}
		if scenario.Name == "" {
			t.Fatalf("scenario %s has no name", scenarioPath)
		}
		scenarioCount++

		// One temp root per scenario: the state dir, the env file and the queue all
		// live under it, and it doubles as HOME so a scenario can never read (or
		// write) the operator's real ~/.anhur-*-memory.
		scenarioRoot := t.TempDir()
		switch scenario.Rule {
		case "config":
			var given parityConfigGiven
			decodeParityGiven(t, scenario, &given)
			observations[scenario.Name] = observeParityConfig(t, scenarioRoot, given)
		case "queue":
			var given parityQueueGiven
			decodeParityGiven(t, scenario, &given)
			observations[scenario.Name] = observeParityQueue(t, scenarioRoot, given)
		case "chunk":
			var given parityChunkGiven
			decodeParityGiven(t, scenario, &given)
			observations[scenario.Name] = observeParityChunk(t, fixtureDirectory, given)
		default:
			t.Fatalf("scenario %s has unknown rule %q (want config|queue|chunk)", scenario.Name, scenario.Rule)
		}
	}
	if scenarioCount == 0 {
		t.Fatalf("no scenarios found in %s — the harness would have nothing to compare", scenarioDirectory)
	}

	document := map[string]interface{}{
		"implementation": "go",
		"source":         "AnhurDB-SDK/v2/plugins/core (shared by anhur-claude-memory and anhur-hermes-memory)",
		"scenarios":      observations,
	}
	encoded, marshalErr := json.MarshalIndent(document, "", "  ")
	if marshalErr != nil {
		t.Fatalf("cannot encode observations: %v", marshalErr)
	}
	if writeErr := os.WriteFile(outputPath, append(encoded, '\n'), 0o644); writeErr != nil {
		t.Fatalf("cannot write observations to %s: %v", outputPath, writeErr)
	}
	t.Logf("parity probe: wrote %d scenario observations to %s", scenarioCount, outputPath)
}

// decodeParityGiven unmarshals the scenario's `given` block, failing loudly on a shape
// mismatch — a silently-empty `given` would produce a green run that tested nothing.
func decodeParityGiven(t *testing.T, scenario parityScenarioFile, target interface{}) {
	t.Helper()
	if len(scenario.Given) == 0 {
		t.Fatalf("scenario %s has no `given` block", scenario.Name)
	}
	decoder := json.NewDecoder(strings.NewReader(string(scenario.Given)))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(target); decodeErr != nil {
		t.Fatalf("scenario %s has an unusable `given` block: %v", scenario.Name, decodeErr)
	}
}

// ── rule 1: configuration loading ────────────────────────────────────────────

// observeParityConfig materialises the env file, isolates the environment, and reports
// what loadConfig resolved.
//
// Junior Tip [the Go arm wears the HERMES identity, 2026-07-30]: Config.StateDirName and
// DefaultContainer are plugin IDENTITY, not rule — the claude build says "claude-ltm",
// the hermes build says "hermes-ltm", and the Python provider ships the hermes defaults.
// Probing with the hermes identity keeps this harness comparing the RULES (precedence,
// parsing, defaults) instead of flagging an intended identity difference every run.
func observeParityConfig(t *testing.T, scenarioRoot string, given parityConfigGiven) map[string]interface{} {
	t.Helper()
	defaultStateDirectory := filepath.Join(scenarioRoot, ".anhur-hermes-memory")

	processEnvironment := map[string]string{}
	for variableName, variableValue := range given.ProcessEnv {
		processEnvironment[variableName] = variableValue
	}

	var envFilePath string
	switch given.EnvFileLocation {
	case "state_dir":
		envFilePath = filepath.Join(defaultStateDirectory, envFileName)
	case "explicit":
		envFilePath = filepath.Join(scenarioRoot, "elsewhere", "anhur.env")
		processEnvironment["ANHUR_ENV_FILE"] = envFilePath
	case "none":
		envFilePath = ""
	default:
		t.Fatalf("unknown env_file_location %q (want state_dir|explicit|none)", given.EnvFileLocation)
	}
	if envFilePath != "" {
		if mkdirErr := os.MkdirAll(filepath.Dir(envFilePath), 0o700); mkdirErr != nil {
			t.Fatalf("cannot create env file directory: %v", mkdirErr)
		}
		fileBody := strings.Join(given.EnvFileLines, "\n") + "\n"
		if writeErr := os.WriteFile(envFilePath, []byte(fileBody), 0o600); writeErr != nil {
			t.Fatalf("cannot write env file: %v", writeErr)
		}
	}

	pluginIdentity := Config{
		StateDirName:     ".anhur-hermes-memory",
		DefaultContainer: "hermes-ltm",
		BinaryName:       "anhur-parity-probe",
	}

	var observation map[string]interface{}
	withIsolatedAnhurEnvironment(t, scenarioRoot, processEnvironment, func() {
		loaded := loadConfig(pluginIdentity)
		observation = map[string]interface{}{
			"api_key_source": loaded.keySource,
			// Junior Tip [a FINGERPRINT, never the value]: this observation file is
			// printed, diffed and pasted into reports. A harness that can print an API
			// key WILL print one the first time somebody points it at a real env file.
			// sha256[:12] proves "the right key was resolved" and reveals nothing.
			"api_key_fingerprint": parityFingerprint(loaded.apiKey),
			"url":                 loaded.url,
			"container":           loaded.container,
			"state_dir":           parityPortablePath(loaded.stateDir, scenarioRoot),
			"vars_loaded":         loaded.envFileVars,
			"env_file_error":      loaded.envFileErr != nil,
		}
	})
	return observation
}

// withIsolatedAnhurEnvironment runs `body` with HOME pointed at the scenario root and
// exactly the requested ANHUR_* variables set, then restores the process environment.
//
// Junior Tip [why the cleanup is this thorough, 2026-07-30]: loadConfig calls
// os.Setenv for every variable it reads out of the env file, so WITHOUT this reset
// scenario 2 would inherit scenario 1's key and the harness would report agreement it
// never observed. Overriding HOME matters just as much: the default state dir is
// $HOME/.anhur-hermes-memory, and a probe that read the operator's REAL env file would
// both be non-deterministic and pull a live API key into a test process.
func withIsolatedAnhurEnvironment(t *testing.T, homeDirectory string, processEnvironment map[string]string, body func()) {
	t.Helper()
	savedVariables := map[string]string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "ANHUR_") {
			continue
		}
		variableName, variableValue, _ := strings.Cut(entry, "=")
		savedVariables[variableName] = variableValue
	}
	savedHome, homeWasSet := os.LookupEnv("HOME")

	clearAnhurVariables := func() {
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "ANHUR_") {
				continue
			}
			variableName, _, _ := strings.Cut(entry, "=")
			_ = os.Unsetenv(variableName)
		}
	}
	defer func() {
		clearAnhurVariables()
		for variableName, variableValue := range savedVariables {
			_ = os.Setenv(variableName, variableValue)
		}
		if homeWasSet {
			_ = os.Setenv("HOME", savedHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
	}()

	clearAnhurVariables()
	if setErr := os.Setenv("HOME", homeDirectory); setErr != nil {
		t.Fatalf("cannot isolate HOME: %v", setErr)
	}
	for variableName, variableValue := range processEnvironment {
		if setErr := os.Setenv(variableName, variableValue); setErr != nil {
			t.Fatalf("cannot set %s: %v", variableName, setErr)
		}
	}
	body()
}

// ── rule 2: the on-disk queue ────────────────────────────────────────────────

// parityFakeAnhurDB is a minimal session-first AnhurDB whose failure mode is scripted by
// the scenario. It records every ingest attempt in arrival order, which is what makes
// "drained in chronological order" and "each item under its OWN session" observable.
type parityFakeAnhurDB struct {
	mutex sync.Mutex
	// transport mirrors parityQueueGiven.Transport.
	transport string
	// attempts holds "<id>@<session_id>" for every ingest request received.
	attempts []string
	// persisted holds the same string for the requests answered with 200.
	persisted []string
}

func (fake *parityFakeAnhurDB) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		sessionID, _ := payload["session_id"].(string)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"session_id": sessionID})
	})
	mux.HandleFunc("/api/v1/ingest", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		sessionID, _ := payload["session_id"].(string)
		content, _ := payload["content"].(string)
		attemptLabel := parityItemID(content) + "@" + sessionID

		rejected := fake.transport == "down"
		if strings.HasPrefix(fake.transport, "reject:") {
			rejectedID := strings.TrimPrefix(fake.transport, "reject:")
			rejected = parityItemID(content) == rejectedID
		}

		fake.mutex.Lock()
		fake.attempts = append(fake.attempts, attemptLabel)
		if !rejected {
			fake.persisted = append(fake.persisted, attemptLabel)
		}
		fake.mutex.Unlock()

		if rejected {
			responseWriter.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "parity scenario: write rejected"})
			return
		}
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{
			"session_id": sessionID,
			"id":         1,
		})
	})
	return mux
}

func (fake *parityFakeAnhurDB) snapshot() (attempts []string, persisted []string) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]string{}, fake.attempts...), append([]string{}, fake.persisted...)
}

// observeParityQueue seeds the queue, drains it against the scripted backend, and reports
// what was attempted, what landed, and what is left on disk.
func observeParityQueue(t *testing.T, scenarioRoot string, given parityQueueGiven) map[string]interface{} {
	t.Helper()
	cfg := config{stateDir: scenarioRoot}
	if mkdirErr := os.MkdirAll(cfg.queueDir(), 0o755); mkdirErr != nil {
		t.Fatalf("cannot create queue dir: %v", mkdirErr)
	}

	expectedBodies := map[string]string{}
	for itemIndex, item := range given.Items {
		content := parityQueueContent(item)
		expectedBodies[item.ID] = content
		// The stamp prefix is what both implementations sort by, so the file names
		// decide the drain order — exactly as in production.
		fileName := fmt.Sprintf("20260730T0000%02d.000000000-4711-%06d.txt", itemIndex, itemIndex)
		if writeErr := os.WriteFile(filepath.Join(cfg.queueDir(), fileName), []byte(content), 0o600); writeErr != nil {
			t.Fatalf("cannot seed queue item %s: %v", item.ID, writeErr)
		}
	}

	fake := &parityFakeAnhurDB{transport: given.Transport}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	memory := client.NewMemory("parity-probe-not-a-real-key",
		client.WithURL(server.URL),
		client.WithUserID("parity-container"),
		client.WithTimeout(5*time.Second),
	)

	flushQueue(context.Background(), cfg, memory)
	firstAttempts, firstPersisted := fake.snapshot()

	secondDrainAttempts := []string{}
	finalPersisted := firstPersisted
	if given.DrainTwice {
		flushQueue(context.Background(), cfg, memory)
		allAttempts, allPersisted := fake.snapshot()
		secondDrainAttempts = allAttempts[len(firstAttempts):]
		finalPersisted = allPersisted
	}

	pendingIDs, pendingIntact := parityScanQueue(t, cfg, expectedBodies)
	quarantinedIDs, quarantineIntact := parityScanQuarantine(t, cfg, expectedBodies)

	accountedFor := map[string]bool{}
	for _, label := range finalPersisted {
		accountedFor[strings.SplitN(label, "@", 2)[0]] = true
	}
	for _, itemID := range append(append([]string{}, pendingIDs...), quarantinedIDs...) {
		accountedFor[itemID] = true
	}

	return map[string]interface{}{
		"attempted":              firstAttempts,
		"persisted":              firstPersisted,
		"pending_after":          pendingIDs,
		"quarantined_after":      quarantinedIDs,
		"second_drain_attempted": secondDrainAttempts,
		// THE no-silent-loss invariant: every item is either in AnhurDB or still on
		// disk. An item in neither place was dropped.
		"every_item_accounted_for": len(accountedFor) == len(given.Items),
		// Nothing left on disk may have been rewritten, truncated or re-headered.
		"remaining_bodies_intact": pendingIntact && quarantineIntact,
	}
}

// parityQueueContent renders one queued unit the way the Go plugin writes it: the owning
// session is embedded in the chunk header, which is the ONLY place flushQueue can prove
// it from. An item with no session gets no header — the quarantine case.
func parityQueueContent(item parityQueueItem) string {
	body := parityBodyMarker + item.ID + " " + item.Body
	if item.Session == "" {
		return body
	}
	return "Claude Code session " + item.Session +
		" — conversation excerpt (2026-07-30T00:00:00Z):\n" + body
}

// parityItemID recovers the item id embedded in a body ("BODY-<id> ...").
func parityItemID(content string) string {
	markerIndex := strings.Index(content, parityBodyMarker)
	if markerIndex < 0 {
		return "unknown"
	}
	rest := content[markerIndex+len(parityBodyMarker):]
	if end := strings.IndexAny(rest, " \n\t"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// parityScanQueue lists the item ids still waiting for delivery, in queue order, and
// whether every one of them still holds its original bytes.
//
// Junior Tip [a sonda tem de olhar onde a REGRA vive, 2026-07-31]: até a fila com
// estado, "ainda pendente" era um arquivo em queue/*.txt. Depois dela, é uma linha com
// state='pending'. Uma sonda deixada apontando para o diretório antigo reportaria
// pending_after=[] com os itens intactos no banco — ou seja, acusaria PERDA DE DADO
// onde não houve nenhuma. Um harness de paridade que grita falso é pior que nenhum:
// ensina a ignorar o alarme. Quando o banco não abre, a fila cai para os arquivos e a
// sonda tem de enxergar os dois lugares — por isso a soma abaixo, não a escolha.
func parityScanQueue(t *testing.T, cfg config, expectedBodies map[string]string) ([]string, bool) {
	t.Helper()
	itemIDs, bodiesIntact := parityScanStoreState(t, cfg, expectedBodies, false)
	fileIDs, filesIntact := parityScanQueueDirectory(t, cfg.queueDir(), expectedBodies)
	return append(itemIDs, fileIDs...), bodiesIntact && filesIntact
}

// parityScanQuarantine does the same for chunks parked awaiting a human.
func parityScanQuarantine(t *testing.T, cfg config, expectedBodies map[string]string) ([]string, bool) {
	t.Helper()
	itemIDs, bodiesIntact := parityScanStoreState(t, cfg, expectedBodies, true)
	fileIDs, filesIntact := parityScanQueueDirectory(t, cfg.quarantineDir(), expectedBodies)
	return append(itemIDs, fileIDs...), bodiesIntact && filesIntact
}

// parityScanStoreState reads one state out of the state queue WITHOUT mutating it.
func parityScanStoreState(t *testing.T, cfg config, expectedBodies map[string]string,
	quarantined bool) ([]string, bool) {
	t.Helper()
	store, storeErr := queueStoreFor(cfg)
	if storeErr != nil {
		return []string{}, true // banco indisponível: só existem arquivos
	}
	var storedItems []queueItem
	var listErr error
	if quarantined {
		storedItems, listErr = store.ListQuarantined()
	} else {
		storedItems, listErr = store.ListPending()
	}
	if listErr != nil {
		t.Fatalf("cannot read the state queue: %v", listErr)
	}
	itemIDs := []string{}
	bodiesIntact := true
	for _, storedItem := range storedItems {
		itemID := parityItemID(storedItem.Content)
		itemIDs = append(itemIDs, itemID)
		if expectedBodies[itemID] != storedItem.Content {
			bodiesIntact = false
		}
	}
	return itemIDs, bodiesIntact
}

// parityScanQueueDirectory lists the item ids still present in a directory (sorted by
// file name = queue order) and whether every one of them still holds its original bytes.
func parityScanQueueDirectory(t *testing.T, directory string, expectedBodies map[string]string) ([]string, bool) {
	t.Helper()
	itemIDs := []string{}
	bodiesIntact := true
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		return itemIDs, bodiesIntact // absent directory == nothing there
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		content, readFileErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readFileErr != nil {
			t.Fatalf("cannot read %s: %v", entry.Name(), readFileErr)
		}
		itemID := parityItemID(string(content))
		itemIDs = append(itemIDs, itemID)
		if expectedBodies[itemID] != string(content) {
			bodiesIntact = false
		}
	}
	return itemIDs, bodiesIntact
}

// ── rule 3: chunking ─────────────────────────────────────────────────────────

// observeParityChunk splits a shared fixture and reports the properties that decide
// whether a long turn survives the trip: nothing dropped, nothing over the limit.
func observeParityChunk(t *testing.T, fixtureDirectory string, given parityChunkGiven) map[string]interface{} {
	t.Helper()
	fixturePath := filepath.Join(fixtureDirectory, given.Fixture)
	fixtureBytes, readErr := os.ReadFile(fixturePath)
	if readErr != nil {
		t.Fatalf("cannot read fixture %s: %v", fixturePath, readErr)
	}
	inputText := string(fixtureBytes)

	chunks := splitIntoChunks(inputText, given.MaxChars)
	largestChunk := 0
	allWithinLimit := true
	for _, chunk := range chunks {
		// len() on a Go string is BYTES; the Python arm measures code points. The
		// scenario files say which of these fields may be compared across languages.
		if len(chunk) > largestChunk {
			largestChunk = len(chunk)
		}
		if len(chunk) > given.MaxChars {
			allWithinLimit = false
		}
	}
	rejoined := strings.Join(chunks, "")

	return map[string]interface{}{
		"chunk_count":         len(chunks),
		"max_chunk_size":      largestChunk,
		"all_within_limit":    allWithinLimit,
		"rejoin_equals_input": rejoined == inputText,
		"rejoined_sha256":     parityDigest(rejoined),
		"input_sha256":        parityDigest(inputText),
	}
}

// ── shared helpers ───────────────────────────────────────────────────────────

// parityFingerprint returns sha256(value)[:12] — enough to prove WHICH secret was
// resolved, never enough to use it. Empty in, empty out.
func parityFingerprint(secretValue string) string {
	if secretValue == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(secretValue))
	return hex.EncodeToString(digest[:])[:12]
}

// parityDigest is the full sha256 of a payload, used to prove chunking lost nothing.
func parityDigest(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// parityPortablePath rewrites an absolute path into something comparable across
// languages and machines: paths under the scenario root (which is also HOME during the
// probe) become "~/...", everything else stays absolute and will fail the comparison —
// loudly, which is the point.
func parityPortablePath(absolutePath string, scenarioRoot string) string {
	if absolutePath == scenarioRoot {
		return "~"
	}
	if strings.HasPrefix(absolutePath, scenarioRoot+string(os.PathSeparator)) {
		return "~/" + absolutePath[len(scenarioRoot)+1:]
	}
	return absolutePath
}
