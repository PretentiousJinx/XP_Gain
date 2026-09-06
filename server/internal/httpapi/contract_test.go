package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/service"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
)

// contractDir holds real server responses, committed and parsed by the Flutter
// client's own tests.
//
// Both sides previously had their own hand-written idea of the JSON, so a
// renamed field would leave the Go suite green, the Dart suite green, and the
// app broken. These files make the server the single source of truth: it emits
// them, the client parses them, and CI fails if the server's output drifts
// without the fixtures being regenerated.
const contractDir = "../../../contract"

func TestGenerateContractFixtures(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "contract.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Freeze the clock so regenerating produces a byte-identical file when
	// nothing changed; a moving timestamp would make every run look like drift.
	frozen := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	db.SetClock(func() time.Time { return frozen })

	srv := httptest.NewServer(New(service.New(db), okAuth()).Routes())
	defer srv.Close()

	do := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer good-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	if err := os.MkdirAll(contractDir, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(name string, raw []byte) {
		t.Helper()
		var pretty any
		if err := json.Unmarshal(raw, &pretty); err != nil {
			t.Fatalf("%s: response is not JSON: %v", name, err)
		}
		normaliseVolatile(pretty)
		out, err := json.MarshalIndent(pretty, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, '\n')
		if err := os.WriteFile(filepath.Join(contractDir, name), out, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 1. An unprovisioned account.
	status, body := do(http.MethodGet, "/v1/me", "")
	if status != http.StatusNotFound {
		t.Fatalf("GET /v1/me on a fresh account = %d, want 404", status)
	}
	write("error_profile_not_found.json", body)

	// 2. Provisioning.
	status, body = do(http.MethodPut, "/v1/me",
		`{"timezone":"UTC","goal_kcal":2200,"goal_protein_g":160,"goal_carbs_g":220,"goal_fat_g":70}`)
	if status != http.StatusCreated {
		t.Fatalf("PUT /v1/me = %d, want 201", status)
	}
	write("profile_created.json", body)

	// 3. An accepted manual entry.
	status, body = do(http.MethodPost, "/v1/intake/manual",
		`{"client_entry_id":"c1","kcal":650,"protein_g":45,"carbs_g":60,"fat_g":20}`)
	if status != http.StatusCreated {
		t.Fatalf("POST manual = %d, want 201", status)
	}
	write("intake_manual.json", body)

	// 4. An accepted photo.
	status, body = do(http.MethodPost, "/v1/intake/photo",
		`{"client_entry_id":"c2","photo_uri":"file://meal.jpg","vision":{"is_valid_food":true,
		  "kcal":500,"protein_g":30,"carbs_g":40,"fat_g":15,"confidence":0.94,"model":"vision-1"}}`)
	if status != http.StatusCreated {
		t.Fatalf("POST photo = %d, want 201", status)
	}
	write("intake_photo.json", body)

	// 5. Path B, the rejection the client must render verbatim.
	status, body = do(http.MethodPost, "/v1/intake/photo",
		`{"client_entry_id":"c3","photo_uri":"file://desk.jpg","vision":{"is_valid_food":false,
		  "validation_reasoning":"This appears to be a photo of a desk, not a meal.",
		  "model":"vision-1"}}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("rejected photo = %d, want 422", status)
	}
	write("error_photo_rejected.json", body)

	// 6. The account view after logging.
	status, body = do(http.MethodGet, "/v1/me", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/me = %d, want 200", status)
	}
	write("profile_active.json", body)

	// 7. Rejected goals.
	status, body = do(http.MethodPut, "/v1/me",
		`{"timezone":"UTC","goal_kcal":1,"goal_protein_g":1,"goal_carbs_g":1,"goal_fat_g":1}`)
	if status != http.StatusBadRequest {
		t.Fatalf("bad goals = %d, want 400", status)
	}
	write("error_invalid_payload.json", body)

	// 8. Unauthenticated.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/me", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", resp.StatusCode)
	}
	write("error_unauthorized.json", raw)
}

// volatileFields hold a fresh random value on every run.
//
// Left alone, the fixtures would differ on every regeneration and the CI drift
// check would fire constantly -- a false alarm people quickly learn to ignore,
// which is worse than having no check. Replacing them with fixed placeholders
// makes the files byte-stable, so a diff means the *shape* changed. The client
// only asserts these are non-empty, never their value.
var volatileFields = map[string]string{
	"entry_id":                "ent_fixture",
	"rejection_id":            "rej_fixture",
	"supersedes_rejection_id": "rej_fixture",
	"resolved_by_entry_id":    "ent_fixture",
}

func normaliseVolatile(v any) {
	switch node := v.(type) {
	case map[string]any:
		for key, child := range node {
			if placeholder, ok := volatileFields[key]; ok {
				if _, isString := child.(string); isString {
					node[key] = placeholder
					continue
				}
			}
			normaliseVolatile(child)
		}
	case []any:
		for _, child := range node {
			normaliseVolatile(child)
		}
	}
}

// TestContractFixturesAreValidJSON is a cheap guard for anyone hand-editing the
// generated files instead of regenerating them.
func TestContractFixturesAreValidJSON(t *testing.T) {
	entries, err := os.ReadDir(contractDir)
	if err != nil {
		t.Skip("contract fixtures not generated yet")
	}
	seen := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(contractDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var any1 any
		if err := json.Unmarshal(raw, &any1); err != nil {
			t.Errorf("%s is not valid JSON: %v", e.Name(), err)
		}
		seen++
	}
	if seen == 0 {
		t.Error("no contract fixtures found")
	}
}
