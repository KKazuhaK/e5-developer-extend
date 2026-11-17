package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// MicrosoftGraphCLIClientID is the public client "Microsoft Graph Command Line
// Tools", which exists in every tenant. Automatic app registration signs in
// through it once to create the app registration.
const MicrosoftGraphCLIClientID = "14d82eec-204b-4c2f-b7e8-296a70dab67e"

// graphResource prefixes bare Graph permission names in Bootstrap scopes.
const graphResource = "https://graph.microsoft.com/"

// Bootstrap signs in once with a public client (device code or browser) and
// returns the access token. The token cache lives in memory only: nothing is
// written to the vault or the configuration, and the token is gone when the
// caller drops it. method is config.AuthDeviceCode or config.AuthBrowser;
// tenant is "organizations" or a tenant ID or domain; scopes are Graph
// permission names such as "Application.ReadWrite.All". httpClient may be nil.
func Bootstrap(ctx context.Context, method, tenant, clientID string, scopes []string, ui LoginUI, httpClient *http.Client) (Token, error) {
	return BootstrapWithOptions(ctx, method, tenant, clientID, scopes, ui, Options{HTTPClient: httpClient})
}

// BootstrapWithOptions is Bootstrap with explicit Options; tests point
// AuthorityHost at a fake identity service.
func BootstrapWithOptions(ctx context.Context, method, tenant, clientID string, scopes []string, ui LoginUI, opts Options) (Token, error) {
	if opts.HTTPClient == nil {
		opts.HTTPClient = NewHTTPClient("")
	}
	if opts.AuthorityHost == "" {
		opts.AuthorityHost = "https://login.microsoftonline.com"
	}
	if tenant == "" {
		tenant = "organizations"
	}
	if len(scopes) == 0 {
		return Token{}, fmt.Errorf("auth: bootstrap needs at least one scope")
	}
	full := make([]string, len(scopes))
	for i, s := range scopes {
		if !strings.Contains(s, "/") {
			s = graphResource + s
		}
		full[i] = s
	}
	// No WithCache: msal keeps its cache in memory, and it dies with this client.
	c, err := public.New(clientID,
		public.WithAuthority(opts.authority(tenant)),
		public.WithHTTPClient(opts.HTTPClient),
		public.WithInstanceDiscovery(!opts.DisableInstanceDiscovery),
	)
	if err != nil {
		return Token{}, fmt.Errorf("create sign-in client: %w", err)
	}
	var res public.AuthResult
	switch method {
	case config.AuthDeviceCode:
		dc, derr := c.AcquireTokenByDeviceCode(ctx, full)
		if derr != nil {
			return Token{}, Classify(derr)
		}
		ui.DeviceCode(dc.Result.Message)
		res, err = dc.AuthenticationResult(ctx)
	case config.AuthBrowser:
		res, err = c.AcquireTokenInteractive(ctx, full,
			public.WithRedirectURI("http://localhost"),
			public.WithOpenURL(ui.OpenBrowser),
		)
	default:
		return Token{}, fmt.Errorf("auth: bootstrap supports %s and %s, not %q", config.AuthDeviceCode, config.AuthBrowser, method)
	}
	if err != nil {
		return Token{}, Classify(err)
	}
	tok := Token{
		AccessToken: res.AccessToken,
		ExpiresOn:   res.ExpiresOn,
		Scopes:      parseScopes(res.AccessToken),
		Account:     res.Account.PreferredUsername,
	}
	tok.TenantID = res.IDToken.TenantID
	if cl, ok := parseClaims(res.AccessToken); ok && tok.TenantID == "" {
		tok.TenantID = cl.TID
	}
	if tok.TenantID == "" {
		tok.TenantID = res.Account.Realm
	}
	return tok, nil
}
