// Package fakegraph is an in-memory stand-in for the parts of Microsoft Graph
// that automatic app registration uses. Tests across packages share it; it is
// never linked into the program itself.
package fakegraph

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
)

// Token is the bearer token the fake accepts.
const Token = "fake-bootstrap-token"

// GraphAppID is the application ID of the fake Microsoft Graph service principal.
const GraphAppID = "00000003-0000-0000-c000-000000000000"

// Request is one request the fake received.
type Request struct {
	Method, Path string
	Body         map[string]any
}

// Server is the fake. Set the failure knobs before the call under test.
type Server struct {
	Srv *httptest.Server

	mu        sync.Mutex
	n         int
	Apps      []map[string]any
	SPs       map[string]string // appId -> service principal object ID
	GraphSP   string
	Scopes    map[string]string
	Roles     map[string]string
	Grants    []map[string]any
	Assigned  []map[string]string // principalId, resourceId, appRoleId
	Requests  []Request
	Secrets   []string // every client secret handed out
	Passwords map[string][]map[string]any

	// SPNotFound makes the next n service principal creations answer 404.
	SPNotFound int
	// GrantNotFound makes the next n consent writes answer 404.
	GrantNotFound int
	// Forbidden makes calls answer 403: keys are "METHOD /path-prefix".
	Forbidden map[string]bool
	// PageSize splits application listings into pages when > 0.
	PageSize int
	// TagQueryEmpty makes the tags/any filter answer nothing, like a replica
	// that has not seen a recent write.
	TagQueryEmpty bool
	// ListView, when set, rewrites every app in a listing (a stale replica);
	// reading one app by ID is never affected.
	ListView func(app map[string]any) map[string]any
	// ExistsStatus is the status of "already exists" answers (400 when 0; 409 is the other real one).
	ExistsStatus int
	// GrantListMiss and RoleListMiss make the next n consent / role assignment
	// listings answer nothing although the object exists.
	GrantListMiss, RoleListMiss int
	// OnRequest, when set, is called for every request while the fake's lock is
	// held (it must not call back into the Server); tests use it to make
	// something happen in the middle of a flow.
	OnRequest func(method, path string)
}

// New starts a fake Graph with the recommended permissions defined.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		SPs: map[string]string{}, Scopes: map[string]string{}, Roles: map[string]string{},
		Forbidden: map[string]bool{}, Passwords: map[string][]map[string]any{},
	}
	s.GraphSP = s.newID("sp")
	s.SPs[GraphAppID] = s.GraphSP
	for _, n := range append(append([]string{}, catalog.RecommendedDelegated...), "Directory.Read.All", "Mail.Read") {
		s.Scopes[n] = s.newID("scope")
	}
	for _, n := range append(append([]string{}, catalog.RecommendedApplication...), "Directory.Read.All", "Mail.Read") {
		s.Roles[n] = s.newID("role")
	}
	s.Srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Srv.Close)
	return s
}

// Base is the endpoint to give to appsetup.Setup.Base.
func (s *Server) Base() string { return s.Srv.URL + "/v1.0" }

func (s *Server) newID(kind string) string {
	s.n++
	return fmt.Sprintf("%s-%08d-0000-0000-0000-000000000000", kind, s.n)
}

// AddApp registers a hand-made app and returns its object ID.
func (s *Server) AddApp(app map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addApp(app)
}

func (s *Server) addApp(app map[string]any) string {
	id := s.newID("obj")
	app["id"] = id
	if app["appId"] == nil {
		s.n++
		app["appId"] = fmt.Sprintf("%08d-0000-4000-8000-000000000000", s.n)
	}
	s.Apps = append(s.Apps, app)
	return id
}

// App returns a copy of the app with the object ID.
func (s *Server) App(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.Apps {
		if a["id"] == id {
			return copyMap(a)
		}
	}
	return nil
}

// RemoveScope deletes a delegated permission from Microsoft Graph's definition.
func (s *Server) RemoveScope(name string) { s.mu.Lock(); delete(s.Scopes, name); s.mu.Unlock() }

// RemoveRole deletes an application permission from Microsoft Graph's definition.
func (s *Server) RemoveRole(name string) { s.mu.Lock(); delete(s.Roles, name); s.mu.Unlock() }

// PasswordNames returns the key IDs and names of the app's client secrets.
func (s *Server) PasswordNames(objectID string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, c := range s.Passwords[objectID] {
		out[str(c["keyId"])] = str(c["displayName"])
	}
	return out
}

// Writes lists the requests that changed something ("POST /v1.0/applications").
func (s *Server) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.Requests {
		if r.Method != http.MethodGet {
			out = append(out, r.Method+" "+r.Path)
		}
	}
	return out
}

// ResetRequests forgets the recorded requests.
func (s *Server) ResetRequests() { s.mu.Lock(); s.Requests = nil; s.mu.Unlock() }

// Count returns how many recorded requests start with "METHOD /path".
func (s *Server) Count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.Requests {
		if strings.HasPrefix(r.Method+" "+r.Path, prefix) {
			n++
		}
	}
	return n
}

// Do runs f with the lock held, for tests that inspect state.
func (s *Server) Do(f func()) { s.mu.Lock(); defer s.mu.Unlock(); f() }

// GrantScope returns the scope string of the first AllPrincipals grant, or "".
func (s *Server) GrantScope() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Grants) == 0 {
		return ""
	}
	return s.Grants[0]["scope"].(string)
}

func copyMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

var (
	tagFilter    = regexp.MustCompile(`tags/any\(t:t eq '([^']+)'\)`)
	appIDFilter  = regexp.MustCompile(`appId eq '([^']+)'`)
	clientFilter = regexp.MustCompile(`clientId eq '([^']+)' and resourceId eq '([^']+)'`)
)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1.0")
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	s.Requests = append(s.Requests, Request{r.Method, r.URL.Path, body})
	if s.OnRequest != nil {
		s.OnRequest(r.Method, r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer "+Token {
		apiErr(w, 401, "InvalidAuthenticationToken", "bad token")
		return
	}
	for key := range s.Forbidden {
		if strings.HasPrefix(r.Method+" "+path, key) {
			apiErr(w, 403, "Authorization_RequestDenied", "Insufficient privileges to complete the operation.")
			return
		}
	}
	filter := r.URL.Query().Get("$filter")
	parts := strings.Split(strings.Trim(path, "/"), "/")

	switch {
	case path == "/servicePrincipals" && r.Method == http.MethodGet:
		s.listSPs(w, filter)
	case path == "/servicePrincipals" && r.Method == http.MethodPost:
		s.createSP(w, body)
	case len(parts) == 3 && parts[0] == "servicePrincipals" && parts[2] == "appRoleAssignments" && r.Method == http.MethodGet:
		var out []map[string]any
		if s.RoleListMiss > 0 {
			s.RoleListMiss--
			writeJSON(w, 200, map[string]any{"value": out})
			return
		}
		for _, a := range s.Assigned {
			if a["principalId"] == parts[1] {
				out = append(out, map[string]any{"appRoleId": a["appRoleId"], "resourceId": a["resourceId"], "principalId": a["principalId"]})
			}
		}
		writeJSON(w, 200, map[string]any{"value": out})
	case len(parts) == 3 && parts[0] == "servicePrincipals" && parts[2] == "appRoleAssignedTo" && r.Method == http.MethodPost:
		if s.GrantNotFound > 0 {
			s.GrantNotFound--
			apiErr(w, 404, "Request_ResourceNotFound", "Resource does not exist")
			return
		}
		a := map[string]string{"principalId": str(body["principalId"]), "resourceId": parts[1], "appRoleId": str(body["appRoleId"])}
		for _, have := range s.Assigned {
			if have["principalId"] == a["principalId"] && have["resourceId"] == a["resourceId"] && have["appRoleId"] == a["appRoleId"] {
				apiErr(w, s.existsStatus(), "Request_BadRequest", "Permission being assigned already exists on the object.")
				return
			}
		}
		s.Assigned = append(s.Assigned, a)
		writeJSON(w, 201, a)
	case path == "/applications" && r.Method == http.MethodGet:
		s.listApps(w, r, filter)
	case path == "/applications" && r.Method == http.MethodPost:
		app := copyMap(body)
		s.addApp(app)
		writeJSON(w, 201, app)
	case len(parts) == 2 && parts[0] == "applications" && r.Method == http.MethodGet:
		for _, a := range s.Apps {
			if a["id"] == parts[1] {
				writeJSON(w, 200, s.withPasswords(a))
				return
			}
		}
		apiErr(w, 404, "Request_ResourceNotFound", "Resource does not exist")
	case len(parts) == 2 && parts[0] == "applications" && r.Method == http.MethodPatch:
		for _, a := range s.Apps {
			if a["id"] == parts[1] {
				for k, v := range body {
					a[k] = v
				}
				w.WriteHeader(204)
				return
			}
		}
		apiErr(w, 404, "Request_ResourceNotFound", "Resource does not exist")
	case len(parts) == 3 && parts[0] == "applications" && parts[2] == "addPassword" && r.Method == http.MethodPost:
		pc, _ := body["passwordCredential"].(map[string]any)
		secret := "SECRET-" + s.newID("pw")
		s.Secrets = append(s.Secrets, secret)
		cred := map[string]any{"keyId": s.newID("key"), "displayName": pc["displayName"], "endDateTime": pc["endDateTime"], "secretText": secret}
		s.Passwords[parts[1]] = append(s.Passwords[parts[1]], cred)
		writeJSON(w, 200, cred)
	case len(parts) == 3 && parts[0] == "applications" && parts[2] == "removePassword" && r.Method == http.MethodPost:
		list := s.Passwords[parts[1]]
		for i, c := range list {
			if c["keyId"] == body["keyId"] {
				s.Passwords[parts[1]] = append(list[:i:i], list[i+1:]...)
				break
			}
		}
		w.WriteHeader(204)
	case path == "/oauth2PermissionGrants" && r.Method == http.MethodGet:
		m := clientFilter.FindStringSubmatch(filter)
		var out []map[string]any
		if s.GrantListMiss > 0 {
			s.GrantListMiss--
			writeJSON(w, 200, map[string]any{"value": out})
			return
		}
		for _, g := range s.Grants {
			if m != nil && g["clientId"] == m[1] && g["resourceId"] == m[2] && g["consentType"] == "AllPrincipals" {
				out = append(out, g)
			}
		}
		writeJSON(w, 200, map[string]any{"value": out})
	case path == "/oauth2PermissionGrants" && r.Method == http.MethodPost:
		if s.GrantNotFound > 0 {
			s.GrantNotFound--
			apiErr(w, 404, "Request_ResourceNotFound", "Resource 'x' does not exist or one of its queried reference-property objects are not present.")
			return
		}
		for _, have := range s.Grants {
			if have["clientId"] == body["clientId"] && have["resourceId"] == body["resourceId"] && have["consentType"] == body["consentType"] {
				apiErr(w, s.existsStatus(), "Request_BadRequest", "Permission entry already exists.")
				return
			}
		}
		g := copyMap(body)
		g["id"] = s.newID("grant")
		s.Grants = append(s.Grants, g)
		writeJSON(w, 201, g)
	case len(parts) == 2 && parts[0] == "oauth2PermissionGrants" && r.Method == http.MethodPatch:
		for _, g := range s.Grants {
			if g["id"] == parts[1] {
				for k, v := range body {
					g[k] = v
				}
				w.WriteHeader(204)
				return
			}
		}
		apiErr(w, 404, "Request_ResourceNotFound", "Resource does not exist")
	default:
		// Fail loudly: a call the fake does not know is a bug in the code under test.
		apiErr(w, 501, "NotImplemented", "fakegraph: unhandled "+r.Method+" "+path)
	}
}

func (s *Server) existsStatus() int {
	if s.ExistsStatus != 0 {
		return s.ExistsStatus
	}
	return 400
}

// withPasswords returns a copy of the app with its passwordCredentials as Graph
// shows them on a read: key ID and name, never the secret.
func (s *Server) withPasswords(a map[string]any) map[string]any {
	out := copyMap(a)
	creds := []map[string]any{}
	for _, c := range s.Passwords[str(a["id"])] {
		creds = append(creds, map[string]any{"keyId": c["keyId"], "displayName": c["displayName"], "endDateTime": c["endDateTime"]})
	}
	out["passwordCredentials"] = creds
	return out
}

func str(v any) string { s, _ := v.(string); return s }

func (s *Server) listSPs(w http.ResponseWriter, filter string) {
	m := appIDFilter.FindStringSubmatch(filter)
	if m == nil {
		writeJSON(w, 200, map[string]any{"value": []any{}})
		return
	}
	id, ok := s.SPs[m[1]]
	if !ok {
		writeJSON(w, 200, map[string]any{"value": []any{}})
		return
	}
	sp := map[string]any{"id": id, "appId": m[1]}
	if m[1] == GraphAppID {
		var scopes, roles []map[string]any
		for n, v := range s.Scopes {
			scopes = append(scopes, map[string]any{"id": v, "value": n, "isEnabled": true})
		}
		for n, v := range s.Roles {
			roles = append(roles, map[string]any{"id": v, "value": n, "isEnabled": true, "allowedMemberTypes": []string{"Application"}})
		}
		sp["oauth2PermissionScopes"], sp["appRoles"] = scopes, roles
	}
	writeJSON(w, 200, map[string]any{"value": []any{sp}})
}

func (s *Server) createSP(w http.ResponseWriter, body map[string]any) {
	if s.SPNotFound > 0 {
		s.SPNotFound--
		// What a real tenant answers while a new app is still replicating.
		apiErr(w, 400, "Request_BadRequest", "The appId '"+str(body["appId"])+"' of the service principal does not reference a valid application object.")
		return
	}
	appID := str(body["appId"])
	if _, ok := s.SPs[appID]; ok {
		apiErr(w, 400, "Request_BadRequest", "A service principal with the same appId already exists")
		return
	}
	id := s.newID("sp")
	s.SPs[appID] = id
	writeJSON(w, 201, map[string]any{"id": id, "appId": appID})
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request, filter string) {
	var all []map[string]any
	for _, a := range s.Apps {
		if m := tagFilter.FindStringSubmatch(filter); m != nil {
			if s.TagQueryEmpty {
				continue
			}
			tags, _ := a["tags"].([]any)
			found := false
			for _, t := range tags {
				if t == m[1] {
					found = true
				}
			}
			if tsl, ok := a["tags"].([]string); ok {
				for _, t := range tsl {
					if t == m[1] {
						found = true
					}
				}
			}
			if !found {
				continue
			}
		}
		v := s.withPasswords(a)
		if s.ListView != nil {
			v = s.ListView(v)
		}
		all = append(all, v)
	}
	skip, _ := strconv.Atoi(r.URL.Query().Get("$skip"))
	out := map[string]any{}
	if s.PageSize > 0 && skip < len(all) {
		end := skip + s.PageSize
		if end < len(all) {
			out["@odata.nextLink"] = s.Srv.URL + "/v1.0/applications?$skip=" + strconv.Itoa(end) + "&" + "$filter=" + strings.ReplaceAll(r.URL.Query().Get("$filter"), " ", "%20")
		} else {
			end = len(all)
		}
		all = all[skip:end]
	}
	out["value"] = all
	writeJSON(w, 200, out)
}
