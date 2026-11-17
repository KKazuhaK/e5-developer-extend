package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

func TestChannelServicesNeverReturnSecrets(t *testing.T) {
	r := newRig(t)
	set := NewSettingsService(r.b)

	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		mu.Lock()
		hits = append(hits, req.URL.Path+" "+string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	const token = "CHANNELTOKEN555"

	bad, err := set.SaveChannel(ChannelInput{ID: "team", Type: config.ChannelSlack, MinLevel: "warning", Language: "zh",
		Secrets: map[string]string{"url": srv.URL + "/hook/" + token}}, true)
	if err != nil || len(bad) > 0 {
		t.Fatalf("save: %v %v", bad, err)
	}
	// A bad one is reported field by field and nothing is saved.
	bad, err = set.SaveChannel(ChannelInput{ID: "x", Type: config.ChannelTelegram}, true)
	if err != nil || len(bad) < 1 {
		t.Fatalf("bad telegram: %v %v", bad, err)
	}
	bad, _ = set.SaveChannel(ChannelInput{ID: "y", Type: config.ChannelSlack, Secrets: map[string]string{"url": "http://hooks.example.com/" + token}}, true)
	if len(bad) != 1 || strings.Contains(bad[0].Message, token) {
		t.Fatalf("http address: %v", bad)
	}

	chs, err := set.ListChannels()
	if err != nil || len(chs) != 1 {
		t.Fatalf("list: %v %v", chs, err)
	}
	r.keep(chs)

	res, err := set.TestChannel("")
	if err != nil || len(res) != 1 || !res[0].OK {
		t.Fatalf("test: %v %v", res, err)
	}
	r.keep(res)
	mu.Lock()
	if len(hits) != 1 || !strings.HasPrefix(hits[0], "/hook/"+token+" ") || !strings.Contains(hits[0], "测试消息") {
		t.Errorf("hits: %v", hits)
	}
	mu.Unlock()

	p, err := set.PreviewMessage(config.ChannelTelegram, "en", "", "", "failure_spike")
	if err != nil || !strings.Contains(p.Body, "High failure rate") {
		t.Fatalf("preview: %v %+v", err, p)
	}
	r.keep(p)
	if _, err := set.PreviewMessage(config.ChannelSlack, "en", "{{.Nope}}", "", "test"); err == nil {
		t.Error("broken template previewed")
	}
	presets := set.EmailPresets()
	if len(presets) != 4 {
		t.Errorf("presets: %v", presets)
	}
	r.keep(presets)

	s, err := set.Get()
	if err != nil || s.Notify.Channels != 1 || s.Notify.Cooldown != "6h" {
		t.Fatalf("settings: %v %+v", err, s.Notify)
	}
	r.keep(s)

	if err := set.RemoveChannel("team"); err != nil {
		t.Fatal(err)
	}
	if chs, _ := set.ListChannels(); len(chs) != 0 {
		t.Errorf("after remove: %v", chs)
	}
	for _, j := range r.returned {
		if strings.Contains(j, token) || strings.Contains(j, "127.0.0.1") {
			t.Errorf("a service handed out a secret: %s", j)
		}
	}
	var back []ChannelView
	_ = json.Unmarshal([]byte(r.returned[0]), &back)
	if len(back) != 1 || !back[0].SecretFields[0].Set {
		t.Errorf("view: %+v", back)
	}
}
