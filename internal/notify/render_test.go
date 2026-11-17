package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

var tNow = time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)

func sampleData(kind, lang string) Data {
	return NewData(SampleEvent(kind, tNow), lang, "dev-box", time.UTC)
}

func TestEveryBuiltinTemplateRenders(t *testing.T) {
	for _, typ := range BuiltinTypes {
		for _, lang := range []string{"zh", "en"} {
			for _, kind := range SampleKinds() {
				r, err := Render(typ, lang, "", sampleData(kind, lang))
				if err != nil {
					t.Fatalf("%s/%s/%s: %v", typ, lang, kind, err)
				}
				if r.Title == "" || r.Body == "" {
					t.Errorf("%s/%s/%s: empty title or body: %+v", typ, lang, kind, r)
				}
				if typ == config.ChannelEmail && !strings.Contains(r.HTML, "<html>") {
					t.Errorf("%s/%s/%s: no html part", typ, lang, kind)
				}
				if !strings.Contains(r.Body, "dev-box") {
					t.Errorf("%s/%s/%s: host missing:\n%s", typ, lang, kind, r.Body)
				}
				if strings.Contains(r.Body+r.Title+r.HTML, "<no value>") || strings.Contains(r.Body, "{{") {
					t.Errorf("%s/%s/%s: unrendered:\n%s", typ, lang, kind, r.Body)
				}
			}
		}
	}
}

func TestBuiltinTemplatesLocalized(t *testing.T) {
	zh, _ := Render("telegram", "zh", "", sampleData(FailureSpike, "zh"))
	en, _ := Render("telegram", "en", "", sampleData(FailureSpike, "en"))
	for _, want := range []string{"调用失败率过高", "失败率 75%", "近 3 轮共失败 9 次", "建议"} {
		if !strings.Contains(zh.Body, want) {
			t.Errorf("zh body lacks %q:\n%s", want, zh.Body)
		}
	}
	for _, want := range []string{"High failure rate", "Failure rate 75%", "9 failures over the last 3 rounds", "Next step"} {
		if !strings.Contains(en.Body, want) {
			t.Errorf("en body lacks %q:\n%s", want, en.Body)
		}
	}
	rec, _ := Render("slack", "en", "", sampleData(Recovered, "en"))
	if !strings.Contains(rec.Body, "Needs attention") { // names what ended
		t.Errorf("recovery does not name the origin:\n%s", rec.Body)
	}
}

func TestTelegramEscapesHTML(t *testing.T) {
	ev := SampleEvent(NeedsAttention, tNow)
	ev.Message = "x <b>&</b> y"
	r, err := Render("telegram", "en", "", NewData(ev, "en", "h", time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Body, "<b>&</b>") || !strings.Contains(r.Body, "&lt;b&gt;&amp;&lt;/b&gt;") {
		t.Errorf("message not escaped:\n%s", r.Body)
	}
	mail, _ := Render("email", "en", "", NewData(ev, "en", "h", time.UTC))
	if strings.Contains(mail.HTML, "<b>&</b>") || !strings.Contains(mail.HTML, "&lt;b&gt;&amp;&lt;/b&gt;") {
		t.Errorf("email html not escaped:\n%s", mail.HTML)
	}
}

func TestDigestRendersEveryItem(t *testing.T) {
	r, err := Render("wecom", "zh", "", sampleData(Digest, "zh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Body, "共 2 条") || !strings.Contains(r.Body, "长时间没有成功调用") || !strings.Contains(r.Body, "密钥即将过期") {
		t.Errorf("digest:\n%s", r.Body)
	}
}

func TestTenantIsHiddenByBuiltinTemplates(t *testing.T) {
	ev := SampleEvent(NeedsAttention, tNow)
	ev.Tenant = "11111111-2222-3333-4444-555555555555"
	for _, typ := range BuiltinTypes {
		r, _ := Render(typ, "en", "", NewData(ev, "en", "h", time.UTC))
		if strings.Contains(r.Body+r.Title+r.HTML, ev.Tenant) {
			t.Errorf("%s shows the tenant", typ)
		}
	}
	r, err := Render("slack", "en", `tenant={{.Tenant}}`, NewData(ev, "en", "h", time.UTC))
	if err != nil || r.Body != "tenant="+ev.Tenant {
		t.Errorf("custom template cannot show the tenant: %v %q", err, r.Body)
	}
}

func TestCustomTemplates(t *testing.T) {
	d := sampleData(NeedsAttention, "en")
	// Whole text is the body; the title comes from the built-in template.
	r, err := Render("slack", "en", `ALERT {{.Account}}: {{.Message}}`, d)
	if err != nil || !strings.HasPrefix(r.Body, "ALERT demo: needs attention") || r.Title == "" {
		t.Fatalf("%v %+v", err, r)
	}
	// Blocks.
	r, err = Render("email", "en", `{{define "title"}}T {{.Account}}{{end}}{{define "body"}}B{{end}}{{define "html"}}<i>{{.Message}}</i>{{end}}`, d)
	if err != nil || r.Title != "T demo" || r.Body != "B" || !strings.HasPrefix(r.HTML, "<i>") {
		t.Fatalf("%v %+v", err, r)
	}
	// An email template without an html block still gets an escaped HTML part.
	r, _ = Render("email", "en", `<b>{{.Message}}</b>`, d)
	if !strings.Contains(r.HTML, "&lt;b&gt;") {
		t.Errorf("html part: %s", r.HTML)
	}
	// Webhook templates have no built-in.
	r, err = Render("webhook", "en", `{"text":{{json .Message}}}`, d)
	if err != nil || !strings.HasPrefix(r.Body, `{"text":"needs attention`) {
		t.Fatalf("%v %q", err, r.Body)
	}
}

func TestCheckTemplate(t *testing.T) {
	good := []struct{ typ, src string }{
		{"telegram", `<b>{{.EventLabel | html}}</b> {{.Message | html}}`},
		{"slack", `{{.Level}} {{.Account}} {{if .Digest}}{{range .Digest}}{{.Message}}{{end}}{{end}}`},
		{"email", `{{define "body"}}{{.Message}}{{end}}{{define "html"}}<p>{{.Message}}</p>{{end}}`},
	}
	for _, g := range good {
		if err := CheckTemplate(g.typ, g.src, false); err != nil {
			t.Errorf("%s: %v", g.src, err)
		}
	}
	if err := CheckTemplate("webhook", `{"text":{{json .Message}},"level":"{{.Level}}"}`, true); err != nil {
		t.Errorf("json template: %v", err)
	}
	bad := []struct{ typ, src, want string }{
		{"slack", `{{.Message`, "template"},
		{"slack", `{{.Nope}}`, "Nope"},
		{"slack", `{{if}}x{{end}}`, "template"},
		{"slack", `{{range .Message}}{{end}}`, "template"},
		{"email", `{{define "html"}}<p>{{.Nope}}</p>{{end}}{{define "body"}}x{{end}}`, "Nope"},
		{"slack", `{{if eq .Event "test"}}hello{{end}}`, "empty"}, // empty for other events
	}
	for _, b := range bad {
		err := CheckTemplate(b.typ, b.src, false)
		if err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%q: got %v, want %q", b.src, err, b.want)
		}
	}
	if err := CheckTemplate("webhook", `text={{.Message}}`, true); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("non-JSON webhook body accepted: %v", err)
	}
	if CheckTemplate("slack", "  ", false) != nil {
		t.Error("blank template must mean the built-in one")
	}
}

func TestLanguageResolution(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	if Language("", "") != "zh" || Language("en", "zh") != "en" || Language("", "en") != "en" {
		t.Error("language order")
	}
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	if Language("", "") != "en" {
		t.Error("fallback")
	}
}
