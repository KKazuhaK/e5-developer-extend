package notify

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

var (
	tagRe    = regexp.MustCompile(`<(/?)([a-zA-Z]+)[^<>]*>`)
	entityRe = regexp.MustCompile(`&(amp|lt|gt|quot|#\d+);`)
)

// wellFormedHTML checks balanced tags and that every & starts a valid entity.
func wellFormedHTML(s string) error {
	var stack []string
	for _, m := range tagRe.FindAllStringSubmatch(s, -1) {
		if m[1] == "" {
			stack = append(stack, m[2])
			continue
		}
		if len(stack) == 0 || stack[len(stack)-1] != m[2] {
			return fmt.Errorf("unbalanced </%s>", m[2])
		}
		stack = stack[:len(stack)-1]
	}
	if len(stack) > 0 {
		return fmt.Errorf("unclosed %v", stack)
	}
	rest := tagRe.ReplaceAllString(s, "")
	if strings.ContainsAny(rest, "<>") {
		return fmt.Errorf("stray angle bracket")
	}
	if n := strings.Count(rest, "&"); n != len(entityRe.FindAllString(rest, -1)) {
		return fmt.Errorf("broken entity")
	}
	return nil
}

func bigDigest(n int) Event {
	d := SampleEvent(Digest, fixedNow)
	d.Items = nil
	for i := 0; i < n; i++ {
		it := SampleEvent(Stale, fixedNow)
		it.Account = fmt.Sprintf("acct-%02d <&>", i)
		it.Message = strings.Repeat("a <b> & c ", 12)
		d.Items = append(d.Items, it)
	}
	return d
}

func TestLongDigestIsSplitIntoWellFormedMessages(t *testing.T) {
	for _, typ := range []string{config.ChannelTelegram, config.ChannelWeCom, config.ChannelDingTalk, config.ChannelDiscord, config.ChannelSlack, config.ChannelBark, config.ChannelNtfy, config.ChannelServerChan, config.ChannelFeishu} {
		ev := bigDigest(40)
		lang := "en"
		render := func(e Event) (Rendered, error) {
			return Render(typ, lang, "", NewData(e, lang, "dev-box", nil))
		}
		parts, err := fitEvent(ev, limits[typ], render)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) < 2 && typ != config.ChannelSlack && typ != config.ChannelDingTalk && typ != config.ChannelServerChan && typ != config.ChannelFeishu {
			t.Errorf("%s: not split (%d)", typ, len(parts))
		}
		items := 0
		for _, p := range parts {
			r, _ := render(p)
			if !limits[typ].fits(r.Body) {
				t.Errorf("%s: a message is too long: %d", typ, len(r.Body))
			}
			if typ == config.ChannelTelegram {
				if err := wellFormedHTML(r.Body); err != nil {
					t.Errorf("telegram: %v\n%s", err, r.Body)
				}
			}
			if p.Event == Digest {
				items += len(p.Items)
			} else {
				items++
			}
		}
		if items != 40 {
			t.Errorf("%s: %d of 40 items survived", typ, items)
		}
	}
}

func TestLongDigestReachesTheServiceInSeveralRequests(t *testing.T) {
	f := newFake(t, 200, `{"ok":true}`)
	tg := Target{Channel: config.Channel{ID: "tg", Type: config.ChannelTelegram, ChatID: "1", APIBase: f.URL}, Secrets: map[string]string{"bot_token": "123:T"}}
	if err := testDeliverer(f).Deliver(context.Background(), tg, bigDigest(40)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) < 2 {
		t.Fatalf("one request for 40 items")
	}
	for _, r := range f.reqs {
		text := decode(t, r.Body)["text"].(string)
		if utf8.RuneCountInString(text) > 4096 || wellFormedHTML(text) != nil {
			t.Errorf("bad message (%d chars)", utf8.RuneCountInString(text))
		}
	}
}

func TestOneVeryLongMessageIsShortenedBeforeEscaping(t *testing.T) {
	ev := SampleEvent(NeedsAttention, fixedNow)
	ev.Message = strings.Repeat("<&>", 3000)
	f := newFake(t, 200, `{"ok":true}`)
	tg := Target{Channel: config.Channel{ID: "tg", Type: config.ChannelTelegram, ChatID: "1", APIBase: f.URL}, Secrets: map[string]string{"bot_token": "123:T"}}
	if err := testDeliverer(f).Deliver(context.Background(), tg, ev); err != nil {
		t.Fatal(err)
	}
	text := decode(t, f.only(t).Body)["text"].(string)
	if utf8.RuneCountInString(text) > 4096 || wellFormedHTML(text) != nil {
		t.Errorf("bad message (%d chars)", utf8.RuneCountInString(text))
	}
}

func TestUntrustedTextIsEscapedPerFormat(t *testing.T) {
	nasty := SampleEvent(NeedsAttention, fixedNow)
	nasty.Account = "<!channel>"
	nasty.Message = "<url|x> *bold* &amp; _it_ `c` [l](u) @all"
	d := NewData(nasty, "en", "<!here>", nil)
	d.Time = "<!everyone>"
	d.Hint = "<!here> *h*"
	check := func(typ string, bad ...string) Rendered {
		r, err := Render(typ, "en", "", d)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bad {
			if strings.Contains(r.Body, b) {
				t.Errorf("%s: %q survives:\n%s", typ, b, r.Body)
			}
		}
		return r
	}
	sl := check("slack", "<!channel>", "<url|x>", "<!here>", "<!everyone>", "&amp; ")
	mustContain(t, "slack", sl.Body, "&lt;!channel&gt;", "&lt;url|x&gt;", "&amp;amp;")
	tgr := check("telegram", "<!channel>", "<url|x>", "<!here>", "<!everyone>")
	mustContain(t, "telegram", tgr.Body, "&lt;!here&gt;", "&lt;!everyone&gt;")
	if err := wellFormedHTML(tgr.Body); err != nil {
		t.Errorf("telegram: %v\n%s", err, tgr.Body)
	}
	for _, typ := range []string{"wecom", "dingtalk"} {
		r := check(typ, "<!channel>", "<url|x>", "*bold*", "[l](u)", "`c`", "<!here>", "@all", `\`)
		if strings.Contains(r.Body, "<") || strings.Contains(r.Body, "[l]") || strings.Contains(r.Body, "**bold") {
			t.Errorf("%s: markup survives:\n%s", typ, r.Body)
		}
		mustContain(t, typ, r.Body, "\u2039!channel\u203a", "\uff0abold\uff0a", "\uff20")
	}
	for _, typ := range []string{"discord", "serverchan"} {
		r := check(typ, "<!channel>", "<url|x>", "*bold*", "[l](u)", "`c`", "<!here>")
		mustContain(t, typ, r.Body, `\*bold\*`, `\<url\|x\>`, `\&amp;`)
	}
}

func TestMdSafeKeepsUnderscoresInsideWords(t *testing.T) {
	if got := mdSafe("e5extend account resume my_acct"); got != "e5extend account resume my_acct" {
		t.Errorf("%q", got)
	}
	if got := mdSafe("a_b_c 2_x"); got != "a_b_c 2_x" {
		t.Errorf("%q", got)
	}
	for _, in := range []string{"_em_", "x_ y", "_x"} {
		if strings.Contains(mdSafe(in), "_") && in != "x_ y" {
			t.Errorf("%q kept an emphasis underscore: %q", in, mdSafe(in))
		}
	}
	if got := mdSafe("x_ y"); strings.Contains(got, "_") {
		t.Errorf("trailing underscore kept: %q", got)
	}
	d := NewData(SampleEvent(AutoPaused, fixedNow), "en", "h", nil)
	d.Account = "my_acct"
	d.Hint = Hint(AutoPaused, "my_acct", "en")
	r, err := Render("wecom", "en", "", d)
	if err != nil || !strings.Contains(r.Body, "account resume my_acct") {
		t.Errorf("hint not copy-pasteable: %v\n%s", err, r.Body)
	}
}

func TestUserinfoPasswordIsAlwaysRedacted(t *testing.T) {
	got := redactSecrets("boom: dial failed for pw", []string{"https://user:pw@hooks.example/a"})
	if strings.Contains(got, "pw") {
		t.Errorf("%q", got)
	}
}
