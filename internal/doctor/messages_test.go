package doctor

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var placeholderRE = regexp.MustCompile(`\{(\w+)\}`)

// checkCodes verifies the codes of one result (rig.run calls it for every
// check of every test): the detail and the fix each come as codes, every code
// is known and has all its parameters, and the English text is what the codes
// render to.
func checkCodes(t *testing.T, c Check) {
	t.Helper()
	msgs := func(code string, params map[string]string, parts []Msg) []Msg {
		if code != "" {
			return []Msg{{Code: code, Params: params}}
		}
		return parts
	}
	verify := func(what string, ms []Msg, text string, allowHint bool) {
		t.Helper()
		var rendered []string
		for _, m := range ms {
			if allowHint && slices.Contains(HintCodes, m.Code) {
				if _, ok := m.Params["account"]; !ok {
					t.Errorf("%s: hint %q lacks the account", c.ID, m.Code)
				}
				return // the caller's own wording
			}
			tpl, known := english[m.Code]
			if !known {
				t.Errorf("%s: %s code %q is not in the table", c.ID, what, m.Code)
				continue
			}
			for _, p := range placeholderRE.FindAllStringSubmatch(tpl, -1) {
				name := p[1]
				switch name {
				case "dur", "age":
					name = "seconds"
				case "when":
					name = "offset"
				}
				if _, ok := m.Params[name]; !ok {
					t.Errorf("%s: %s code %q lacks the parameter %q", c.ID, what, m.Code, name)
				}
			}
			rendered = append(rendered, m.English())
		}
		if got := strings.Join(rendered, "; "); got != text {
			t.Errorf("%s: %s %q does not match its codes, which render %q", c.ID, what, text, got)
		}
	}
	if c.Detail == "" {
		t.Errorf("%s: no detail", c.ID)
	}
	ds := msgs(c.Code, c.Params, c.Parts)
	if len(ds) == 0 {
		t.Errorf("%s: detail %q has no code", c.ID, c.Detail)
	}
	verify("detail", ds, c.Detail, false)
	if c.Fix != "" {
		fs := msgs(c.FixCode, c.FixParams, c.FixParts)
		if len(fs) == 0 {
			t.Errorf("%s: fix %q has no code", c.ID, c.Fix)
		}
		verify("fix", fs, c.Fix, true)
	} else if c.FixCode != "" || len(c.FixParts) > 0 {
		t.Errorf("%s: a fix code without a fix", c.ID)
	}
}

func TestCodeTable(t *testing.T) {
	for code, text := range english {
		if text == "" {
			t.Errorf("%s has no text", code)
		}
		if !strings.Contains(code, ".") {
			t.Errorf("code %q has no dot", code)
		}
	}
	for _, h := range HintCodes {
		if _, dup := english[h]; dup {
			t.Errorf("%s is both a hint and a message", h)
		}
	}
}

func TestRender(t *testing.T) {
	got := Render("clock.ahead", map[string]string{"seconds": "5"})
	if got != "this machine is 5s ahead of Graph's clock" {
		t.Errorf("got %q", got)
	}
	if got := Render("sub.manual", map[string]string{"date": "2026-12-01", "offset": "-2"}); got != "the date you entered (2026-12-01) is 2 day(s) ago" {
		t.Errorf("got %q", got)
	}
	if Render("nope", nil) != "" {
		t.Error("an unknown code renders as nothing")
	}
}

// codesFile is the list the desktop app's tests read to prove it can show
// every code. Regenerate it with UPDATE_DOCTOR_CODES=1 go test ./internal/doctor.
const codesFile = "../../gui/frontend/src/lib/doctor-codes.json"

func TestCodeListMatchesFrontend(t *testing.T) {
	want, err := json.MarshalIndent(Codes(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_DOCTOR_CODES") != "" {
		if err := os.WriteFile(codesFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(codesFile)
	if err != nil {
		t.Fatalf("%v; run: UPDATE_DOCTOR_CODES=1 go test ./internal/doctor", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is out of date; run: UPDATE_DOCTOR_CODES=1 go test ./internal/doctor", codesFile)
	}
}
