package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

func TestBootstrapKeepsTheTokenInMemoryOnly(t *testing.T) {
	f := newFakeAAD(t)
	ui := &testUI{}
	// Anything written to disk would land in the working directory or temp dirs of this test.
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("E5EXTEND_CONFIG_DIR", filepath.Join(dir, "cfg"))
	t.Setenv("E5EXTEND_DATA_DIR", filepath.Join(dir, "data"))

	tok, err := BootstrapWithOptions(ctx5(t), config.AuthDeviceCode, fakeTenant, fakeClient,
		[]string{"Mail.Read"}, ui, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if ui.msg == "" {
		t.Error("device code was not shown")
	}
	if tok.AccessToken == "" || tok.Account != "me@contoso.test" || tok.TenantID != "tid1" {
		t.Fatalf("token = %+v", tok)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("bootstrap wrote to disk: %v", entries)
	}
}

func TestBootstrapRejectsOtherMethods(t *testing.T) {
	f := newFakeAAD(t)
	for _, m := range []string{config.AuthPassword, config.AuthClientSecret, ""} {
		if _, err := BootstrapWithOptions(ctx5(t), m, fakeTenant, fakeClient, []string{"X"}, &testUI{}, f.opts()); err == nil {
			t.Errorf("method %q accepted", m)
		}
	}
	if _, err := BootstrapWithOptions(ctx5(t), config.AuthDeviceCode, fakeTenant, fakeClient, nil, &testUI{}, f.opts()); err == nil {
		t.Error("no scopes accepted")
	}
}

func TestBootstrapClassifiesFailures(t *testing.T) {
	f := newFakeAAD(t)
	f.failWith("device_code",
		jsonError(400, `{"error":"invalid_grant","error_description":"AADSTS65001: The user or administrator has not consented","error_codes":[65001]}`))
	_, err := BootstrapWithOptions(ctx5(t), config.AuthDeviceCode, fakeTenant, fakeClient, []string{"Mail.Read"}, &testUI{}, f.opts())
	if err == nil || ErrorCode(err) != "AADSTS65001" {
		t.Fatalf("err = %v (code %q)", err, ErrorCode(err))
	}
}
