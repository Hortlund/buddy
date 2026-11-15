package tasks

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed automation_browser.cjs
var browserPageGuard string

//go:embed automation_page_init.js
var browserPageInit string

type browserScope struct {
	mu             sync.RWMutex
	views          map[string]string
	docs           map[string]string
	sites          map[string]string
	active         *browserAction
	lateDenials    map[string]bool
	historyEpoch   string
	historyStart   int64
	networkHistory map[string]bool
	close          func()
}

type browserAction struct {
	ctx     context.Context
	call    automationCall
	epoch   string
	click   string
	pending sync.WaitGroup
	denials map[string]bool
}

const maxBrowserRequestBody = 16 << 10

func safeBrowserMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func startBrowserScope(ctx context.Context, dir, guard string, command []string) (*browserScope, []string, error) {
	if guard == "" {
		return nil, nil, fmt.Errorf("browser automation needs a job authorization endpoint")
	}
	for _, arg := range command[1:] {
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "--browser", "--config", "--cdp-endpoint", "--endpoint", "--extension", "--proxy-server", "--proxy-bypass", "--init-page", "--init-script", "--port", "--user-data-dir", "--allow-unrestricted-file-access":
			return nil, nil, fmt.Errorf("%s conflicts with Buddy's managed browser session", flag)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	scope := &browserScope{views: make(map[string]string), docs: make(map[string]string), sites: make(map[string]string)}
	serve := func(handler http.Handler) (string, func(), error) {
		return serveBrowserHTTP(ctx, handler, nil)
	}
	key := "/" + rand.Text()
	check, stopCheck, err := serve(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != key {
			http.NotFound(w, req)
			return
		}
		var destination struct {
			Click string `json:"click"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 2048)).Decode(&destination); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		control, err := readAutomationControlFile(dir)
		if err != nil || control.Paused || !scope.confirmClick(req.Context(), guard, control, destination.Click) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	resourceKey := rand.Text()
	proxy, pin, stopProxy, err := startBrowserNetwork(ctx, scope, dir, guard, resourceKey, slices.Contains(command[1:], "--ignore-https-errors"))
	if err != nil {
		cancel()
		stopCheck()
		return nil, nil, err
	}
	sessionDir, err := os.MkdirTemp(dir, "browser-")
	if err != nil {
		cancel()
		stopProxy()
		stopCheck()
		return nil, nil, err
	}
	scope.close = func() {
		cancel()
		stopProxy()
		stopCheck()
		_ = os.RemoveAll(sessionDir)
	}
	eventKey, secret := "__buddy_"+rand.Text(), rand.Text()
	locationMarker := "__buddy_location_" + rand.Text() + ":"
	initFile := filepath.Join(sessionDir, "init.js")
	initCode := fmt.Sprintf("(() => { const buddyEventKey = %q, buddySecret = %q, buddyLocationMarker = %q;\n%s\n})();",
		eventKey, secret, locationMarker, browserPageInit)
	guardFile := filepath.Join(sessionDir, "guard.cjs")
	code := fmt.Sprintf("const endpoint = %q, eventKey = %q, secret = %q, resourceKey = %q, locationMarker = %q;\n%s",
		check+key, eventKey, secret, resourceKey, locationMarker, browserPageGuard)
	for _, script := range []struct{ path, code string }{{initFile, initCode}, {guardFile, code}} {
		if err = os.WriteFile(script.path, []byte(script.code), 0600); err != nil {
			scope.close()
			return nil, nil, err
		}
	}
	// Playwright writes some results before the proxy can check their scope.
	// The tool sandbox cannot read this directory, and the proxy returns approved content inline.
	configFile := filepath.Join(sessionDir, "browser.json")
	config, _ := json.Marshal(map[string]any{"browser": map[string]any{"launchOptions": map[string]any{
		"args": []string{"--ignore-certificate-errors-spki-list=" + pin, "--disable-quic"},
	}}})
	if err = os.WriteFile(configFile, config, 0600); err != nil {
		scope.close()
		return nil, nil, err
	}
	args := append(command, "--config", configFile, "--isolated", "--block-service-workers", "--no-webmcp", "--snapshot-mode", "none",
		"--output-dir", filepath.Join(sessionDir, "output"), "--proxy-server", proxy, "--proxy-bypass", "<-loopback>", "--init-page", guardFile, "--init-script", initFile)
	return scope, args, nil
}

func browserDestination(address string) (string, string) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.Contains(u.Host, "%") {
		return "", ""
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	origin := u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" || u.ForceQuery {
		path += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		path += "#" + u.EscapedFragment()
	}
	return origin + path, origin
}

func browserProcessEnv(env []string) []string {
	return slices.DeleteFunc(env, func(entry string) bool {
		return strings.HasPrefix(entry, "PLAYWRIGHT_MCP_")
	})
}

func browserNetworkURL(address string) string {
	switch {
	case strings.HasPrefix(address, "wss://"):
		return "https://" + strings.TrimPrefix(address, "wss://")
	case strings.HasPrefix(address, "ws://"):
		return "http://" + strings.TrimPrefix(address, "ws://")
	default:
		return address
	}
}

func (s *browserScope) originInEpoch(origin, epoch string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return epoch != "" && s.sites[origin] == epoch
}

func (s *browserScope) allowNavigation(address, epoch string) {
	target, origin := browserDestination(address)
	if target == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, _, _ := strings.Cut(target, "#")
	s.docs[document] = epoch
	s.views[target] = epoch
	s.sites[origin] = epoch
}

func (s *browserScope) beginAction(ctx context.Context, call automationCall, epoch, click string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call.Capture = nil
	s.active = &browserAction{ctx: ctx, call: call, epoch: epoch, click: click, denials: make(map[string]bool)}
}

func (s *browserScope) finishAction() []string {
	s.mu.Lock()
	action := s.active
	s.active = nil
	s.mu.Unlock()
	// Chromium can return from a click while a request it initiated is still
	// paused for authorization. Drain those requests before the next tool call.
	action.pending.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for method := range s.lateDenials {
		action.denials[method] = true
	}
	clear(s.lateDenials)
	var denials []string
	for method := range action.denials {
		denials = append(denials, method)
	}
	slices.Sort(denials)
	return denials
}

func (s *browserScope) confirmClick(ctx context.Context, guard string, control AutomationControl, click string) bool {
	s.mu.RLock()
	action := s.active
	s.mu.RUnlock()
	return click != "" && action != nil && action.click == click && action.ctx.Err() == nil &&
		action.epoch != "" && action.epoch == currentAutomationEpoch(ctx, guard, control) && action.ctx.Err() == nil
}

func currentAutomationEpoch(ctx context.Context, guard string, control AutomationControl) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, guard, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Buddy-Automation-Revision", strconv.FormatInt(control.Revision, 10))
	client := http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return ""
	}
	return response.Header.Get(automationEpochHeader)
}

func (s *browserScope) verifyView(ctx context.Context, dir, guard string, control AutomationControl, observe func(string, json.RawMessage) (automationResult, bool)) bool {
	// Only the selected page's frames are part of an ordinary tool result.
	const code = "async page => page.frames().map(frame => frame.url())"
	var addresses []string
	if !browserProviderValue(observe, code, &addresses) || len(addresses) == 0 {
		return false
	}
	return s.allowViews(ctx, dir, guard, control, addresses)
}

func browserProviderValue(observe func(string, json.RawMessage) (automationResult, bool), code string, into any) bool {
	args, _ := json.Marshal(map[string]string{"code": code})
	result, ok := observe("browser_run_code_unsafe", args)
	if !ok || len(result.Result.Content) == 0 {
		return false
	}
	text := result.Result.Content[0].Text
	line, _, _ := strings.Cut(strings.TrimPrefix(text, "### Result\n"), "\n")
	return strings.HasPrefix(text, "### Result\n") && json.Unmarshal([]byte(line), into) == nil
}

func (s *browserScope) allowViews(ctx context.Context, dir, guard string, control AutomationControl, addresses []string) bool {
	for _, address := range addresses {
		switch address {
		case "about:blank", "about:srcdoc", "chrome-error://chromewebdata/":
			continue
		}
		if !s.checkDestination(ctx, dir, guard, address, control, false) {
			return false
		}
	}
	return true
}

func (s *browserScope) tabSummary(ctx context.Context, dir, guard string, control AutomationControl, observe func(string, json.RawMessage) (automationResult, bool)) (string, bool) {
	const code = `async page => await Promise.all(page.context().pages().map(async tab => {
		const readAddresses = () => tab.frames().map(frame => frame.url());
		const addresses = readAddresses();
		const title = await tab.title().catch(() => '');
		const stable = JSON.stringify(addresses) === JSON.stringify(readAddresses());
		return { current: tab === page, title, addresses: stable ? addresses : [] };
	}))`
	var tabs []struct {
		Current   bool
		Title     string
		Addresses []string
	}
	if !browserProviderValue(observe, code, &tabs) {
		return "", false
	}
	var summary strings.Builder
	summary.WriteString("### Result\n")
	for index, tab := range tabs {
		fmt.Fprintf(&summary, "- Tab %d", index)
		if tab.Current {
			summary.WriteString(" (current)")
		}
		if len(tab.Addresses) > 0 && s.allowViews(ctx, dir, guard, control, tab.Addresses) {
			fmt.Fprintf(&summary, " · %q (%s)\n", tab.Title, tab.Addresses[0])
		} else {
			summary.WriteString(" · Outside automation scope\n")
		}
	}
	return summary.String(), true
}

func (s *browserScope) checkDestination(ctx context.Context, dir, guard, address string, control AutomationControl, document bool) bool {
	target, origin := browserDestination(address)
	if target == "" {
		return false
	}
	if document {
		target, _, _ = strings.Cut(target, "#")
	}
	s.mu.RLock()
	cached := s.views[target]
	if document {
		cached = s.docs[target]
	}
	s.mu.RUnlock()
	if cached != "" && cached == currentAutomationEpoch(ctx, guard, control) {
		return true
	}
	args, _ := json.Marshal(map[string]string{"url": address})
	call := automationCall{Provider: "playwright", Name: "browser_navigate", Arguments: args, Revision: control.Revision}
	epoch, allowed := call.authorize(ctx, guard)
	if !allowed || epoch == "" {
		return false
	}
	latest, err := readAutomationControlFile(dir)
	if err != nil || latest != control {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if document {
		s.docs[target] = epoch
	} else {
		s.views[target] = epoch
	}
	s.sites[origin] = epoch
	return true
}

type browserSubmission struct {
	ctx    context.Context
	finish func()
	cancel context.CancelFunc
}

func (s *browserScope) checkSubmission(ctx context.Context, dir, guard string, request browserRequest, control AutomationControl, epoch string, opaque bool) (submission *browserSubmission) {
	s.mu.Lock()
	action := s.active
	if action != nil {
		action.pending.Add(1)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	var stop func() bool
	if action != nil {
		stop = context.AfterFunc(action.ctx, cancel)
	}
	finish := sync.OnceFunc(func() {
		if action != nil {
			stop()
			action.pending.Done()
		}
	})
	defer func() {
		if submission != nil {
			return
		}
		method := "HTTP request"
		switch request.Method {
		case "POST", "PUT", "PATCH", "DELETE":
			method = "HTTP " + request.Method
		case "SEND":
			method = "WebSocket message"
		}
		s.mu.Lock()
		if action != nil {
			action.denials[method] = true
		} else if s.historyEpoch == epoch {
			if s.lateDenials == nil {
				s.lateDenials = make(map[string]bool)
			}
			s.lateDenials[method] = true
		}
		s.mu.Unlock()
		finish()
		cancel()
	}()
	_, origin := browserDestination(browserNetworkURL(request.URL))
	if origin == "" || request.Method == "" || opaque || len(request.Body) > maxBrowserRequestBody {
		return nil
	}
	// Outside an active action the submission can use the inspection grant.
	call := automationCall{Provider: "playwright", Name: "browser_snapshot", Revision: control.Revision}
	if action != nil {
		if action.epoch != epoch || action.ctx.Err() != nil {
			return nil
		}
		call = action.call
	} else if !s.originInEpoch(origin, epoch) {
		return nil
	}
	if !authorizeSubmission(ctx, dir, guard, request, control, epoch, call) {
		return nil
	}
	return &browserSubmission{ctx: ctx, finish: finish, cancel: cancel}
}

func authorizeSubmission(ctx context.Context, dir, guard string, request browserRequest, control AutomationControl, epoch string, call automationCall) bool {
	call.Request = &request
	receipt, allowed := call.authorize(ctx, guard)
	latest, err := readAutomationControlFile(dir)
	return allowed && receipt == epoch && err == nil && latest == control && ctx.Err() == nil
}

// Diagnostics are retained only for the epoch in which this proxy saw them.
func (s *browserScope) noteHistoryEpoch(epoch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != "" && s.historyEpoch != epoch {
		s.historyEpoch, s.historyStart = epoch, time.Now().UnixMilli()
		clear(s.networkHistory)
		clear(s.lateDenials)
	}
}

func (s *browserScope) recordNetwork(epoch, method, address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.historyEpoch {
		return
	}
	if s.networkHistory == nil || len(s.networkHistory) >= 4096 {
		s.networkHistory = make(map[string]bool)
	}
	s.networkHistory[method+" "+address] = true
}

func (s *browserScope) diagnostics(call automationCall, epoch string, observe func(string, json.RawMessage) (automationResult, bool)) (string, bool) {
	options := struct {
		Network       bool   `json:"-"`
		All           bool   `json:"all"`
		IncludeStatic bool   `json:"static"`
		Filter        string `json:"filter"`
		Level         string `json:"level"`
	}{Network: call.Name == "browser_network_requests", Level: "info"}
	if len(call.Arguments) != 0 && json.Unmarshal(call.Arguments, &options) != nil {
		return "", false
	}
	input, _ := json.Marshal(map[string]any{"network": options.Network, "all": options.All,
		"includeStatic": options.IncludeStatic, "filter": options.Filter})
	code := "async page => page[Symbol.for('buddy.diagnostics')](" + string(input) + ")"
	var records []struct {
		At            int64
		URL           string
		Method        string
		Status        int
		Kind          string
		Text          string
		WorkerScripts []string
		WorkerStarted int64
	}
	if !browserProviderValue(observe, code, &records) {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if epoch == "" || s.historyEpoch != epoch {
		return "", false
	}
	var out strings.Builder
	for _, entry := range records {
		if entry.At <= s.historyStart || out.Len() > 24<<10 {
			continue
		}
		if options.Network {
			if s.networkHistory[entry.Method+" "+entry.URL] {
				fmt.Fprintf(&out, "%s %s", entry.Method, entry.URL)
				if entry.Status != 0 {
					fmt.Fprintf(&out, " [%d]", entry.Status)
				}
				out.WriteByte('\n')
			}
			continue
		}
		target, _ := browserDestination(entry.URL)
		document, _, fragment := strings.Cut(target, "#")
		if s.views[target] != epoch && (fragment || s.docs[document] != epoch) {
			continue
		}
		if len(entry.WorkerScripts) != 0 && (entry.WorkerStarted <= s.historyStart ||
			slices.ContainsFunc(entry.WorkerScripts, func(script string) bool {
				return !s.workerScriptAllowed(entry.URL, script, epoch)
			})) {
			continue
		}
		if browserConsoleLevel(entry.Kind) <= browserConsoleLevel(options.Level) {
			fmt.Fprintf(&out, "[%s] %s\n", strings.ToUpper(entry.Kind), entry.Text)
		}
	}
	if out.Len() == 0 {
		return "No browser diagnostics in the current authorization scope.", true
	}
	return out.String(), true
}

// The caller holds the scope lock while checking the recorded worker provenance.
func (s *browserScope) workerScriptAllowed(owner, script, epoch string) bool {
	if address, blob := strings.CutPrefix(script, "blob:"); blob {
		_, ownerOrigin := browserDestination(owner)
		_, workerOrigin := browserDestination(address)
		return ownerOrigin != "" && ownerOrigin == workerOrigin
	}
	_, origin := browserDestination(script)
	return origin != "" && s.sites[origin] == epoch && s.networkHistory["GET "+script]
}

func browserConsoleLevel(kind string) int {
	switch kind {
	case "error", "assert":
		return 0
	case "warning", "warn":
		return 1
	case "info", "log":
		return 2
	default:
		return 3
	}
}
