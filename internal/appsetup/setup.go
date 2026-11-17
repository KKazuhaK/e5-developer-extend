package appsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
)

// Setup creates or updates one app registration. Fill in the exported fields
// and call Plan, then Apply. Zero values mean the real service and defaults.
type Setup struct {
	Kind Kind
	// Token is a Graph access token with the permissions from the design doc
	// (Application.ReadWrite.All and so on). It is only sent to Graph.
	Token string
	// TenantID is the tenant the token belongs to; it goes into the admin consent link.
	TenantID string
	// Base is the Graph endpoint (DefaultBase when empty); tests point it at a fake.
	Base string
	HTTP *http.Client
	// Permissions overrides the recommended list; tests only.
	Permissions []string

	// Version and Hostname go into the app's notes and the client secret's name.
	Version  string
	Hostname string
	Now      func() time.Time

	// StoreSecret receives the client secret of KindApp. It is the only place
	// the secret ever goes; if it fails the secret is revoked again.
	StoreSecret func(secret string) error

	// Progress reports what is going on, e.g. "waiting for Microsoft to replicate".
	Progress func(msg string)
	// Backoff and Sleep replace the replication waits; tests only.
	Backoff []time.Duration
	Sleep   func(ctx context.Context, d time.Duration) error

	graph *graphInfo
}

type graphSP struct {
	ID     string `json:"id"`
	Scopes []struct {
		ID        string `json:"id"`
		Value     string `json:"value"`
		IsEnabled *bool  `json:"isEnabled"`
	} `json:"oauth2PermissionScopes"`
	Roles []struct {
		ID        string   `json:"id"`
		Value     string   `json:"value"`
		IsEnabled *bool    `json:"isEnabled"`
		Members   []string `json:"allowedMemberTypes"`
	} `json:"appRoles"`
}

type graphInfo struct {
	spID   string
	scopes map[string]string // permission name -> ID
	roles  map[string]string
}

func (s *Setup) client() *client {
	h := s.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	base := s.Base
	if base == "" {
		base = DefaultBase
	}
	return &client{base: base, http: h, token: s.Token}
}

func (s *Setup) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Setup) permissionNames() []string {
	if s.Permissions != nil {
		return s.Permissions
	}
	if s.Kind == KindApp {
		return catalog.RecommendedApplication
	}
	return catalog.RecommendedDelegated
}

func (s *Setup) permType() string {
	if s.Kind == KindApp {
		return "Role"
	}
	return "Scope"
}

// ---- Graph objects ----

type resourceAccess struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type requiredAccess struct {
	ResourceAppID  string           `json:"resourceAppId"`
	ResourceAccess []resourceAccess `json:"resourceAccess"`
}

type application struct {
	ID                     string           `json:"id"`
	AppID                  string           `json:"appId"`
	DisplayName            string           `json:"displayName"`
	Tags                   []string         `json:"tags"`
	Notes                  string           `json:"notes"`
	SignInAudience         string           `json:"signInAudience"`
	IsFallbackPublicClient *bool            `json:"isFallbackPublicClient"`
	PublicClient           *publicClient    `json:"publicClient"`
	RequiredResourceAccess []requiredAccess `json:"requiredResourceAccess"`
}

type publicClient struct {
	RedirectURIs []string `json:"redirectUris"`
}

func (a *application) ref() AppRef {
	return AppRef{ObjectID: a.ID, ClientID: a.AppID, DisplayName: a.DisplayName}
}

const appSelect = "id,appId,displayName,tags,notes,signInAudience,isFallbackPublicClient,publicClient,requiredResourceAccess"

// ---- reading ----

func (s *Setup) resolveGraph(ctx context.Context, c *client) (*graphInfo, error) {
	if s.graph != nil {
		return s.graph, nil
	}
	var res []graphSP
	err := c.list(ctx, "read the Microsoft Graph service principal", "/servicePrincipals"+
		filterQuery("appId eq '"+GraphAppID+"'", "$select", "id,appId,oauth2PermissionScopes,appRoles"),
		func(raw json.RawMessage) error {
			var one graphSP
			if err := json.Unmarshal(raw, &one); err != nil {
				return err
			}
			res = append(res, one)
			return nil
		})
	if err != nil {
		return nil, s.mapErr(err, "read the Microsoft Graph service principal", rolesApp)
	}
	if len(res) == 0 || res[0].ID == "" {
		return nil, errors.New("the Microsoft Graph service principal was not found in this tenant")
	}
	g := &graphInfo{spID: res[0].ID, scopes: map[string]string{}, roles: map[string]string{}}
	for _, sc := range res[0].Scopes {
		if sc.IsEnabled == nil || *sc.IsEnabled {
			g.scopes[sc.Value] = sc.ID
		}
	}
	for _, r := range res[0].Roles {
		if (r.IsEnabled == nil || *r.IsEnabled) && (len(r.Members) == 0 || slices.Contains(r.Members, "Application")) {
			g.roles[r.Value] = r.ID
		}
	}
	// Every wanted permission must exist; never skip one quietly.
	known := g.scopes
	if s.Kind == KindApp {
		known = g.roles
	}
	var unknown []string
	for _, n := range s.permissionNames() {
		if _, ok := known[n]; !ok {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return nil, &UnknownPermissionError{Kind: s.Kind, Names: unknown}
	}
	s.graph = g
	return g, nil
}

func (s *Setup) permIDs(g *graphInfo) map[string]string {
	m := g.scopes
	if s.Kind == KindApp {
		m = g.roles
	}
	out := map[string]string{}
	for _, n := range s.permissionNames() {
		out[n] = m[n]
	}
	return out
}

func (s *Setup) findTagged(ctx context.Context, c *client) (*application, error) {
	var found []application
	err := c.list(ctx, "look for an existing app registration", "/applications"+
		filterQuery("tags/any(t:t eq '"+s.Kind.KindTag()+"')", "$select", appSelect, "$count", "true"),
		func(raw json.RawMessage) error {
			var a application
			if err := json.Unmarshal(raw, &a); err != nil {
				return err
			}
			found = append(found, a)
			return nil
		})
	if err != nil {
		return nil, s.mapErr(err, "look for an existing app registration", rolesApp)
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

func (s *Setup) findCandidates(ctx context.Context, c *client) ([]application, error) {
	var out []application
	err := c.list(ctx, "look for hand-made app registrations", "/applications?$select="+appSelect+"&$top=100",
		func(raw json.RawMessage) error {
			var a application
			if err := json.Unmarshal(raw, &a); err != nil {
				return err
			}
			for _, t := range a.Tags {
				if strings.HasPrefix(strings.ToLower(t), "e5extend:") {
					return nil // already one of ours, for either kind
				}
			}
			for _, n := range legacyNames {
				if strings.EqualFold(a.DisplayName, n) {
					out = append(out, a)
					break
				}
			}
			return nil
		})
	if err != nil {
		return nil, s.mapErr(err, "look for hand-made app registrations", rolesApp)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

func (s *Setup) servicePrincipalID(ctx context.Context, c *client, appID string) (string, error) {
	var id string
	err := c.list(ctx, "look for the service principal", "/servicePrincipals"+
		filterQuery("appId eq '"+appID+"'", "$select", "id,appId"),
		func(raw json.RawMessage) error {
			var sp struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &sp); err != nil {
				return err
			}
			if id == "" {
				id = sp.ID
			}
			return nil
		})
	return id, s.mapErr(err, "look for the service principal", rolesApp)
}

type grant struct {
	ID    string `json:"id"`
	Scope string `json:"scope"`
}

func (s *Setup) findGrant(ctx context.Context, c *client, spID, graphID string) (*grant, error) {
	var g *grant
	err := c.list(ctx, "look for the administrator consent", "/oauth2PermissionGrants"+
		filterQuery(fmt.Sprintf("clientId eq '%s' and resourceId eq '%s' and consentType eq 'AllPrincipals'", spID, graphID)),
		func(raw json.RawMessage) error {
			var one grant
			if err := json.Unmarshal(raw, &one); err != nil {
				return err
			}
			if g == nil {
				g = &one
			}
			return nil
		})
	return g, err
}

// assignedRoles returns the Graph app role IDs already assigned to the service principal.
func (s *Setup) assignedRoles(ctx context.Context, c *client, spID, graphID string) (map[string]bool, error) {
	have := map[string]bool{}
	err := c.list(ctx, "look for the granted application permissions", "/servicePrincipals/"+spID+"/appRoleAssignments",
		func(raw json.RawMessage) error {
			var a struct {
				AppRoleID  string `json:"appRoleId"`
				ResourceID string `json:"resourceId"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return err
			}
			if a.ResourceID == graphID {
				have[a.AppRoleID] = true
			}
			return nil
		})
	return have, err
}

// ---- plan ----

// missingPermissions returns the wanted names the app does not list yet.
func (s *Setup) missingPermissions(a *application, ids map[string]string) []string {
	have := map[string]bool{}
	if a != nil {
		for _, r := range a.RequiredResourceAccess {
			if !strings.EqualFold(r.ResourceAppID, GraphAppID) {
				continue
			}
			for _, ra := range r.ResourceAccess {
				if strings.EqualFold(ra.Type, s.permType()) {
					have[strings.ToLower(ra.ID)] = true
				}
			}
		}
	}
	var missing []string
	for _, n := range s.permissionNames() {
		if !have[strings.ToLower(ids[n])] {
			missing = append(missing, n)
		}
	}
	return missing
}

// Plan works out what Apply would do, without changing anything.
func (s *Setup) Plan(ctx context.Context) (*Plan, error) {
	c := s.client()
	g, err := s.resolveGraph(ctx, c)
	if err != nil {
		return nil, err
	}
	ids := s.permIDs(g)
	p := &Plan{
		Kind: s.Kind, TenantID: s.TenantID, DisplayName: s.Kind.DisplayName(), Tags: s.Kind.Tags(),
		Permissions: slices.Clone(s.permissionNames()),
	}
	existing, err := s.findTagged(ctx, c)
	if err != nil {
		return nil, err
	}
	var app *application
	if existing != nil {
		r := existing.ref()
		p.Existing = &r
		app = existing
		p.DisplayName = existing.DisplayName
	} else {
		cands, err := s.findCandidates(ctx, c)
		if err != nil {
			return nil, err
		}
		for i := range cands {
			p.Candidates = append(p.Candidates, cands[i].ref())
		}
	}
	p.Missing = s.missingPermissions(app, ids)
	add := func(code, text string) { p.Actions = append(p.Actions, Action{Code: code, Text: text}) }

	switch {
	case existing == nil && len(p.Candidates) > 0:
		add("adopt_or_create", fmt.Sprintf("adopt one of the existing app registrations (%d found) or create %q", len(p.Candidates), p.DisplayName))
	case existing == nil:
		add("create_app", fmt.Sprintf("create the app registration %q", p.DisplayName))
	default:
		if len(p.Missing) > 0 || s.needsAppSettings(existing) {
			add("update_app", fmt.Sprintf("update the app registration %q (only adds what is missing)", existing.DisplayName))
		}
	}

	if existing == nil && len(p.Candidates) > 0 {
		// What is left depends on which app the user picks.
		if s.Kind == KindApp {
			add("create_secret", "create a client secret named for this computer and store it in the vault (12 months)")
		}
		return p, nil
	}
	// Service principal, consent, secret: inspect what exists for a known app.
	spID, hasSP := "", false
	if app != nil {
		if spID, err = s.servicePrincipalID(ctx, c, app.AppID); err != nil {
			return nil, err
		}
		hasSP = spID != ""
	}
	if !hasSP {
		add("create_service_principal", "create the service principal (the app's identity in this tenant)")
	}
	if s.Kind == KindApp {
		need := true
		if hasSP {
			have, err := s.assignedRoles(ctx, c, spID, g.spID)
			if err != nil {
				return nil, s.mapErr(err, "look for the granted application permissions", rolesGrant)
			}
			need = false
			for _, n := range s.permissionNames() {
				if !have[ids[n]] {
					need = true
				}
			}
		}
		if need {
			add("assign_roles", "grant the application permissions (administrator consent)")
		}
		add("create_secret", "create a client secret named for this computer and store it in the vault (12 months)")
	} else {
		need := true
		if hasSP {
			gr, err := s.findGrant(ctx, c, spID, g.spID)
			if err != nil {
				return nil, s.mapErr(err, "look for the administrator consent", rolesGrant)
			}
			if gr != nil {
				need = len(missingFrom(strings.Fields(gr.Scope), s.permissionNames())) > 0
			}
		}
		if need {
			add("grant_consent", "grant administrator consent for the delegated permissions")
		}
	}
	return p, nil
}

// needsAppSettings reports whether the delegated app lacks the redirect URI or the public client flag.
func (s *Setup) needsAppSettings(a *application) bool {
	if s.Kind != KindDelegated {
		return false
	}
	return !hasRedirect(a) || a.IsFallbackPublicClient == nil || !*a.IsFallbackPublicClient
}

func hasRedirect(a *application) bool {
	return a.PublicClient != nil && slices.Contains(a.PublicClient.RedirectURIs, Redirect)
}

func missingFrom(have, want []string) []string {
	set := map[string]bool{}
	for _, h := range have {
		set[strings.ToLower(h)] = true
	}
	var out []string
	for _, w := range want {
		if !set[strings.ToLower(w)] {
			out = append(out, w)
		}
	}
	return out
}

// ---- apply ----

func (s *Setup) notes() string {
	v := s.Version
	if v == "" {
		v = "dev"
	}
	return fmt.Sprintf("Created by E5 Developer Extend %s on %s. Read-only Microsoft Graph access to keep the tenant active.",
		v, s.now().Format("2006-01-02"))
}

// Apply creates or updates the app registration so that it matches the plan,
// grants consent and, for KindApp, creates a client secret. It is safe to run
// again. A failure to grant consent does not fail Apply: the Result carries the
// administrator consent link instead.
func (s *Setup) Apply(ctx context.Context, choice Choice) (*Result, error) {
	if s.Kind == KindApp && s.StoreSecret == nil {
		return nil, errors.New("appsetup: StoreSecret is required for the application identity")
	}
	c := s.client()
	g, err := s.resolveGraph(ctx, c)
	if err != nil {
		return nil, err
	}
	ids := s.permIDs(g)
	res := &Result{Kind: s.Kind, TenantID: s.TenantID}

	// 1. The application.
	app, err := s.ensureApp(ctx, c, ids, choice, res)
	if err != nil {
		return nil, err
	}
	res.ClientID, res.ObjectID, res.DisplayName = app.AppID, app.ID, app.DisplayName

	// 2. The service principal.
	spID, err := s.ensureServicePrincipal(ctx, c, app, res)
	if err != nil {
		return nil, err
	}

	// 3. Consent. A failure here keeps the app and hands back the consent link.
	var consentErr error
	if s.Kind == KindApp {
		consentErr = s.ensureRoles(ctx, c, g, ids, spID, res)
	} else {
		consentErr = s.ensureGrant(ctx, c, g, spID, res)
	}
	if consentErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		res.ConsentGranted = false
		res.ConsentError = consentErr.Error()
		res.AdminConsentURL = AdminConsentURL(s.TenantID, app.AppID)
	} else {
		res.ConsentGranted = true
	}

	// 4. The client secret.
	if s.Kind == KindApp {
		if err := s.createSecret(ctx, c, app, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (s *Setup) ensureApp(ctx context.Context, c *client, ids map[string]string, choice Choice, res *Result) (*application, error) {
	app, err := s.findTagged(ctx, c)
	if err != nil {
		return nil, err
	}
	adopting := false
	if app == nil {
		cands, err := s.findCandidates(ctx, c)
		if err != nil {
			return nil, err
		}
		switch {
		case choice.Adopt != "":
			for i := range cands {
				if strings.EqualFold(cands[i].ID, choice.Adopt) || strings.EqualFold(cands[i].AppID, choice.Adopt) {
					app, adopting = &cands[i], true
				}
			}
			if app == nil {
				return nil, fmt.Errorf("no existing app registration %q to adopt (it must be named like one of: %s)",
					choice.Adopt, strings.Join(legacyNames, ", "))
			}
		case len(cands) > 0 && !choice.CreateNew:
			return nil, ErrChoiceRequired
		}
	}

	if app == nil { // create
		s.progress("creating the app registration %q...", s.Kind.DisplayName())
		body := map[string]any{
			"displayName":            s.Kind.DisplayName(),
			"signInAudience":         "AzureADMyOrg",
			"tags":                   s.Kind.Tags(),
			"notes":                  s.notes(),
			"requiredResourceAccess": mergeAccess(nil, s.permissionNames(), ids, s.permType()),
		}
		if s.Kind == KindDelegated {
			body["publicClient"] = map[string]any{"redirectUris": []string{Redirect}}
			body["isFallbackPublicClient"] = true
		}
		var created application
		if err := c.do(ctx, "create the app registration", http.MethodPost, "/applications", body, &created); err != nil {
			return nil, s.mapErr(err, "create the app registration", rolesApp)
		}
		if created.ID == "" || created.AppID == "" {
			return nil, errors.New("create the app registration: Graph returned no ID")
		}
		res.CreatedApp = true
		res.AddedPermissions = slices.Clone(s.permissionNames())
		return &created, nil
	}

	// reuse or adopt: add what is missing, never remove.
	patch := map[string]any{}
	if adopting {
		res.AdoptedApp = true
		if choice.Rename && app.DisplayName != s.Kind.DisplayName() {
			patch["displayName"] = s.Kind.DisplayName()
			res.Renamed = true
		}
		if app.Notes == "" {
			patch["notes"] = s.notes()
		}
	}
	if tags := unionStrings(app.Tags, s.Kind.Tags()); len(tags) != len(app.Tags) {
		patch["tags"] = tags
	}
	if s.Kind == KindDelegated {
		if !hasRedirect(app) {
			var uris []string
			if app.PublicClient != nil {
				uris = slices.Clone(app.PublicClient.RedirectURIs)
			}
			patch["publicClient"] = map[string]any{"redirectUris": append(uris, Redirect)}
		}
		if app.IsFallbackPublicClient == nil || !*app.IsFallbackPublicClient {
			patch["isFallbackPublicClient"] = true
		}
	}
	missing := s.missingPermissions(app, ids)
	if len(missing) > 0 {
		patch["requiredResourceAccess"] = mergeAccess(app.RequiredResourceAccess, s.permissionNames(), ids, s.permType())
		res.AddedPermissions = missing
	}
	if len(patch) > 0 {
		s.progress("updating the app registration %q...", app.DisplayName)
		if err := c.do(ctx, "update the app registration", http.MethodPatch, "/applications/"+app.ID, patch, nil); err != nil {
			return nil, s.mapErr(err, "update the app registration", rolesApp)
		}
		res.UpdatedApp = true
		if name, ok := patch["displayName"].(string); ok {
			app.DisplayName = name
		}
	}
	return app, nil
}

// mergeAccess adds the wanted permissions to the Graph entry of existing,
// keeping everything already there, and returns the whole requiredResourceAccess.
func mergeAccess(existing []requiredAccess, names []string, ids map[string]string, typ string) []requiredAccess {
	out := make([]requiredAccess, len(existing))
	copy(out, existing)
	idx := -1
	for i := range out {
		if strings.EqualFold(out[i].ResourceAppID, GraphAppID) {
			idx = i
		}
	}
	if idx < 0 {
		out = append(out, requiredAccess{ResourceAppID: GraphAppID, ResourceAccess: []resourceAccess{}})
		idx = len(out) - 1
	}
	list := slices.Clone(out[idx].ResourceAccess)
	if list == nil {
		list = []resourceAccess{}
	}
	for _, n := range names {
		id := ids[n]
		if !slices.ContainsFunc(list, func(r resourceAccess) bool {
			return strings.EqualFold(r.ID, id) && strings.EqualFold(r.Type, typ)
		}) {
			list = append(list, resourceAccess{ID: id, Type: typ})
		}
	}
	out[idx].ResourceAccess = list
	return out
}

func unionStrings(have, add []string) []string {
	out := slices.Clone(have)
	for _, a := range add {
		if !slices.ContainsFunc(out, func(h string) bool { return strings.EqualFold(h, a) }) {
			out = append(out, a)
		}
	}
	return out
}

func (s *Setup) ensureServicePrincipal(ctx context.Context, c *client, app *application, res *Result) (string, error) {
	id, err := s.servicePrincipalID(ctx, c, app.AppID)
	if err != nil || id != "" {
		return id, err
	}
	s.progress("creating the service principal...")
	err = s.retryReplication(ctx, "service principal", func() error {
		var sp struct {
			ID string `json:"id"`
		}
		err := c.do(ctx, "create the service principal", http.MethodPost, "/servicePrincipals",
			map[string]any{"appId": app.AppID}, &sp)
		if err == nil {
			id = sp.ID
			res.CreatedServicePrincipal = true
			return nil
		}
		var ae *APIError
		if errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Message), "already exist") {
			// Another run created it a moment ago.
			id, err = s.servicePrincipalID(ctx, c, app.AppID)
			if err == nil && id != "" {
				return nil
			}
		}
		return err
	})
	if err != nil {
		return "", s.mapErr(err, "create the service principal", rolesApp)
	}
	return id, nil
}

func (s *Setup) ensureGrant(ctx context.Context, c *client, g *graphInfo, spID string, res *Result) error {
	want := s.permissionNames()
	return s.retryReplication(ctx, "administrator consent", func() error {
		cur, err := s.findGrant(ctx, c, spID, g.spID)
		if err != nil {
			return s.mapErr(err, "grant administrator consent", rolesGrant)
		}
		if cur == nil {
			s.progress("granting administrator consent...")
			err := c.do(ctx, "grant administrator consent", http.MethodPost, "/oauth2PermissionGrants", map[string]any{
				"clientId": spID, "consentType": "AllPrincipals", "resourceId": g.spID, "scope": strings.Join(want, " "),
			}, nil)
			if err == nil {
				res.ConsentAdded = slices.Clone(want)
			}
			return s.mapErr(err, "grant administrator consent", rolesGrant)
		}
		have := strings.Fields(cur.Scope)
		add := missingFrom(have, want)
		if len(add) == 0 {
			return nil
		}
		s.progress("adding permissions to the administrator consent...")
		err = c.do(ctx, "grant administrator consent", http.MethodPatch, "/oauth2PermissionGrants/"+cur.ID,
			map[string]any{"scope": strings.Join(append(have, add...), " ")}, nil)
		if err == nil {
			res.ConsentAdded = add
		}
		return s.mapErr(err, "grant administrator consent", rolesGrant)
	})
}

func (s *Setup) ensureRoles(ctx context.Context, c *client, g *graphInfo, ids map[string]string, spID string, res *Result) error {
	return s.retryReplication(ctx, "application permissions", func() error {
		have, err := s.assignedRoles(ctx, c, spID, g.spID)
		if err != nil {
			return s.mapErr(err, "grant the application permissions", rolesGrant)
		}
		for _, n := range s.permissionNames() {
			if have[ids[n]] {
				continue
			}
			s.progress("granting %s...", n)
			err := c.do(ctx, "grant the application permissions", http.MethodPost,
				"/servicePrincipals/"+g.spID+"/appRoleAssignedTo",
				map[string]any{"principalId": spID, "resourceId": g.spID, "appRoleId": ids[n]}, nil)
			if err != nil {
				return s.mapErr(err, "grant the application permissions", rolesGrant)
			}
			have[ids[n]] = true
			res.ConsentAdded = append(res.ConsentAdded, n)
		}
		return nil
	})
}

func (s *Setup) createSecret(ctx context.Context, c *client, app *application, res *Result) error {
	host := s.Hostname
	if host == "" {
		host = "unknown-host"
	}
	now := s.now()
	name := fmt.Sprintf("e5extend %s %s", host, now.Format("2006-01-02"))
	end := now.AddDate(0, 12, 0)
	s.progress("creating a client secret %q...", name)
	var out struct {
		KeyID      string `json:"keyId"`
		SecretText string `json:"secretText"`
	}
	err := s.retryReplication(ctx, "client secret", func() error {
		return c.do(ctx, "create the client secret", http.MethodPost, "/applications/"+app.ID+"/addPassword",
			map[string]any{"passwordCredential": map[string]any{
				"displayName": name, "endDateTime": end.UTC().Format(time.RFC3339),
			}}, &out)
	})
	if err != nil {
		return s.mapErr(err, "create the client secret", rolesApp)
	}
	if out.SecretText == "" {
		return errors.New("create the client secret: Graph returned no secret")
	}
	secret := out.SecretText
	out.SecretText = ""
	if err := s.StoreSecret(secret); err != nil {
		// Do not leave a secret nobody holds.
		_ = c.do(ctx, "revoke the client secret", http.MethodPost, "/applications/"+app.ID+"/removePassword",
			map[string]any{"keyId": out.KeyID}, nil)
		return fmt.Errorf("store the client secret: %w", err)
	}
	res.SecretName = name
	res.SecretExpires = end.Format("2006-01-02")
	return nil
}

// mapErr turns a 403 into a RoleError naming the roles that would allow it.
func (s *Setup) mapErr(err error, doing string, roles []string) error {
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
		return &RoleError{Doing: doing, Roles: roles}
	}
	return err
}
