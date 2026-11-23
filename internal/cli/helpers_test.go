package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camlock"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

// gold is the recipe most CLI tests write.
var gold = map[string]any{"name": "GOLD", "film_simulation": "classic_chrome", "dynamic_range": "400", "clarity": 2,
	"white_balance": map[string]any{"mode": "daylight", "shift_r": 3, "shift_b": -5}}

// result is one run of the command line.
type result struct {
	code     int
	out, err string
}

// json decodes stdout as one JSON object.
func (r result) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(r.out), &v); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\nstdout:\n%s\nstderr:\n%s", err, r.out, r.err)
	}
	return v
}

// TestMain points the camera lock at a file of this test process, so `go test ./internal/cli` never
// waits for (or blocks) a real x100vi command. isolate gives each test its own lock file as well.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "x100vi-cli-test")
	if err != nil {
		panic(err)
	}
	os.Setenv(camlock.EnvVar, filepath.Join(dir, "camera.lock"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// isolate points the state folder (automatic backups) and the camera lock at a temp folder and returns
// the state folder.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	t.Setenv("X100VI_HOME", state)
	t.Setenv(camlock.EnvVar, filepath.Join(dir, "camera.lock"))
	return state
}

// cameraFactory hands the CLI a software camera.
func cameraFactory(f *fakecam.FakeX100VI) Factory {
	return func() (ptp.Transport, error) { return f, nil }
}

// failing hands the CLI a factory that fails to connect with err.
func failing(err error) Factory {
	return func() (ptp.Transport, error) { return nil, err }
}

// boom is a factory for commands that must not connect.
func boom(t *testing.T) Factory {
	return func() (ptp.Transport, error) {
		t.Error("must not connect")
		return nil, errors.New("must not connect")
	}
}

// runWith runs the CLI with a factory.
func runWith(t *testing.T, factory Factory, argv ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main(argv, factory, &out, &errOut)
	return result{code, out.String(), errOut.String()}
}

// cliRunner runs the CLI against one fake camera, like the Python `run` fixture.
type cliRunner struct {
	t    *testing.T
	fake *fakecam.FakeX100VI
}

func newRunner(t *testing.T) *cliRunner {
	isolate(t)
	return &cliRunner{t, fakecam.New()}
}

func (c *cliRunner) run(argv ...string) result {
	c.t.Helper()
	return runWith(c.t, cameraFactory(c.fake), argv...)
}

// on runs the CLI against another camera.
func (c *cliRunner) on(f *fakecam.FakeX100VI, argv ...string) result {
	c.t.Helper()
	return runWith(c.t, cameraFactory(f), argv...)
}

// writeRecipe saves a recipe as JSON in dir and returns the path.
func writeRecipe(t *testing.T, dir string, recipe map[string]any, name string) string {
	t.Helper()
	if name == "" {
		name = "recipe.json"
	}
	b, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// makeRAF writes an X100VI RAF header file.
func makeRAF(t *testing.T, path, model string) string {
	t.Helper()
	p, err := fakecam.MakeRAF(path, model)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// cells is what the fake camera developed into an image file.
func cells(t *testing.T, path string) ([]int, bool) {
	t.Helper()
	c, full, err := fakecam.RenderedCellsFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c, full
}

func mustContain(t *testing.T, text string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(text, f) {
			t.Errorf("%q not found in:\n%s", f, text)
		}
	}
}

func wantCode(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.out, r.err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// get walks nested JSON objects: get(j, "codex", "written").
func get(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func strs(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
