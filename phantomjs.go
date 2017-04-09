package phantomjs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Default settings.
const (
	DefaultPort    = 20202
	DefaultBinPath = "phantomjs"
)

// Process represents a PhantomJS process.
type Process struct {
	path string
	cmd  *exec.Cmd

	// Path to the 'phantomjs' binary.
	BinPath string

	// HTTP port used to communicate with phantomjs.
	Port int

	// Output from the process.
	Stdout io.Writer
	Stderr io.Writer
}

// NewProcess returns a new instance of Process.
func NewProcess() *Process {
	return &Process{
		BinPath: DefaultBinPath,
		Port:    DefaultPort,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
}

// Path returns a temporary path that the process is run from.
func (p *Process) Path() string {
	return p.path
}

// Open start the phantomjs process with the shim script.
func (p *Process) Open() error {
	if err := func() error {
		// Generate temporary path to run script from.
		path, err := ioutil.TempDir("", "phantomjs-")
		if err != nil {
			return err
		}
		p.path = path

		// Write shim script.
		scriptPath := filepath.Join(path, "shim.js")
		if err := ioutil.WriteFile(scriptPath, []byte(shim), 0600); err != nil {
			return err
		}

		// Start external process.
		cmd := exec.Command(p.BinPath, scriptPath)
		cmd.Dir = p.Path()
		cmd.Env = []string{fmt.Sprintf("PORT=%d", p.Port)}
		cmd.Stdout = p.Stdout
		cmd.Stderr = p.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		p.cmd = cmd

		// Wait until process is available.
		if err := p.wait(); err != nil {
			return err
		}
		return nil

	}(); err != nil {
		p.Close()
		return err
	}

	return nil
}

// Close stops the process.
func (p *Process) Close() (err error) {
	// Kill process.
	if p.cmd != nil {
		if e := p.cmd.Process.Kill(); e != nil && err == nil {
			err = e
		}
		p.cmd.Wait()
	}

	// Remove shim file.
	if p.path != "" {
		if e := os.RemoveAll(p.path); e != nil && err == nil {
			err = e
		}
	}

	return err
}

// URL returns the process' API URL.
func (p *Process) URL() string {
	return fmt.Sprintf("http://localhost:%d", p.Port)
}

// wait continually checks the process until it gets a response or times out.
func (p *Process) wait() error {
	ticker := time.NewTicker(1000 * time.Millisecond)
	defer ticker.Stop()

	timer := time.NewTimer(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timer.C:
			return errors.New("timeout")
		case <-ticker.C:
			if err := p.ping(); err == nil {
				return nil
			}
		}
	}
}

// ping checks the process to see if it is up.
func (p *Process) ping() error {
	// Send request.
	resp, err := http.Get(p.URL() + "/ping")
	if err != nil {
		return err
	}
	resp.Body.Close()

	// Verify successful status code.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	return nil
}

// CreateWebPage returns a new instance of a "webpage".
func (p *Process) CreateWebPage() *WebPage {
	var resp struct {
		Ref refJSON `json:"ref"`
	}
	p.mustDoJSON("POST", "/webpage/Create", nil, &resp)
	return &WebPage{ref: newRef(p, resp.Ref.ID)}
}

// mustDoJSON sends an HTTP request to url and encodes and decodes the req/resp as JSON.
// This function will panic if it cannot communicate with the phantomjs API.
func (p *Process) mustDoJSON(method, path string, req, resp interface{}) {
	// Encode request.
	var r io.Reader
	if req != nil {
		buf, err := json.Marshal(req)
		if err != nil {
			panic(err)
		}
		r = bytes.NewReader(buf)
	}

	// Create request.
	httpRequest, err := http.NewRequest(method, p.URL()+path, r)
	if err != nil {
		panic(err)
	}

	// Send request.
	httpResponse, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		panic(err)
	}
	defer httpResponse.Body.Close()

	// Check response code.
	if httpResponse.StatusCode == http.StatusNotFound {
		panic(fmt.Errorf("not found: %s", path))
	} else if httpResponse.StatusCode == http.StatusInternalServerError {
		body, _ := ioutil.ReadAll(httpResponse.Body)
		panic(errors.New(string(body)))
	}

	// Decode response if reference passed in.
	if resp != nil {
		if buf, err := ioutil.ReadAll(httpResponse.Body); err != nil {
			panic(err)
		} else if err := json.Unmarshal(buf, resp); err != nil {
			panic(fmt.Errorf("unmarshal error: err=%s, buffer=%s", err, buf))
		}
	}
}

// WebPage represents an object returned from "webpage.create()".
type WebPage struct {
	ref *Ref
}

// Open opens a URL.
func (p *WebPage) Open(url string) error {
	req := map[string]interface{}{
		"ref": p.ref.id,
		"url": url,
	}
	var resp struct {
		Status string `json:"status"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/Open", req, &resp)

	if resp.Status != "success" {
		return errors.New("failed")
	}
	return nil
}

// CanGoBack returns true if the page can be navigated back.
func (p *WebPage) CanGoBack() bool {
	var resp struct {
		Value bool `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/CanGoBack", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// CanGoForward returns true if the page can be navigated forward.
func (p *WebPage) CanGoForward() bool {
	var resp struct {
		Value bool `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/CanGoForward", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// ClipRect returns the clipping rectangle used when rendering.
// Returns nil if no clipping rectangle is set.
func (p *WebPage) ClipRect() Rect {
	var resp struct {
		Value rectJSON `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/ClipRect", map[string]interface{}{"ref": p.ref.id}, &resp)
	return Rect{
		Top:    resp.Value.Top,
		Left:   resp.Value.Left,
		Width:  resp.Value.Width,
		Height: resp.Value.Height,
	}
}

// SetClipRect sets the clipping rectangle used when rendering.
// Set to nil to render the entire webpage.
func (p *WebPage) SetClipRect(rect Rect) {
	req := map[string]interface{}{
		"ref": p.ref.id,
		"rect": rectJSON{
			Top:    rect.Top,
			Left:   rect.Left,
			Width:  rect.Width,
			Height: rect.Height,
		},
	}
	p.ref.process.mustDoJSON("POST", "/webpage/SetClipRect", req, nil)
}

// Content returns content of the webpage enclosed in an HTML/XML element.
func (p *WebPage) Content() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/Content", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// SetContent sets the content of the webpage.
func (p *WebPage) SetContent(content string) {
	p.ref.process.mustDoJSON("POST", "/webpage/SetContent", map[string]interface{}{"ref": p.ref.id, "content": content}, nil)
}

// Cookies returns a list of cookies visible to the current URL.
func (p *WebPage) Cookies() []*http.Cookie {
	var resp struct {
		Value []cookieJSON `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/Cookies", map[string]interface{}{"ref": p.ref.id}, &resp)

	a := make([]*http.Cookie, len(resp.Value))
	for i := range resp.Value {
		a[i] = decodeCookieJSON(resp.Value[i])
	}
	return a
}

// SetCookies sets a list of cookies visible to the current URL.
func (p *WebPage) SetCookies(cookies []*http.Cookie) {
	a := make([]cookieJSON, len(cookies))
	for i := range cookies {
		a[i] = encodeCookieJSON(cookies[i])
	}
	req := map[string]interface{}{"ref": p.ref.id, "cookies": a}
	p.ref.process.mustDoJSON("POST", "/webpage/SetCookies", req, nil)
}

// CustomHeaders returns a list of additional headers sent with the web page.
func (p *WebPage) CustomHeaders() http.Header {
	var resp struct {
		Value map[string]string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/CustomHeaders", map[string]interface{}{"ref": p.ref.id}, &resp)

	// Convert to a header object.
	hdr := make(http.Header)
	for key, value := range resp.Value {
		hdr.Set(key, value)
	}
	return hdr
}

// SetCustomHeaders sets a list of additional headers sent with the web page.
//
// This function does not support multiple headers with the same name. Only
// the first value for a header key will be used.
func (p *WebPage) SetCustomHeaders(header http.Header) {
	m := make(map[string]string)
	for key := range header {
		m[key] = header.Get(key)
	}
	req := map[string]interface{}{"ref": p.ref.id, "headers": m}
	p.ref.process.mustDoJSON("POST", "/webpage/SetCustomHeaders", req, nil)
}

// FocusedFrameName returns the name of the currently focused frame.
func (p *WebPage) FocusedFrameName() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/FocusedFrameName", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// FrameContent returns the content of the current frame.
func (p *WebPage) FrameContent() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/FrameContent", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// SetFrameContent sets the content of the current frame.
func (p *WebPage) SetFrameContent(content string) {
	p.ref.process.mustDoJSON("POST", "/webpage/SetFrameContent", map[string]interface{}{"ref": p.ref.id, "content": content}, nil)
}

// FrameName returns the name of the current frame.
func (p *WebPage) FrameName() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/FrameName", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// FramePlainText returns the plain text representation of the current frame content.
func (p *WebPage) FramePlainText() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/FramePlainText", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// FrameTitle returns the title of the current frame.
func (p *WebPage) FrameTitle() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/FrameTitle", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// FrameURL returns the URL of the current frame.
func (p *WebPage) FrameURL() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/FrameURL", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// FrameCount returns the total number of frames.
func (p *WebPage) FrameCount() int {
	var resp struct {
		Value int `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/FrameCount", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// FrameNames returns an list of frame names.
func (p *WebPage) FrameNames() []string {
	var resp struct {
		Value []string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/FrameNames", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// LibraryPath returns the path used by InjectJS() to resolve scripts.
// Initially it is set to Process.Path().
func (p *WebPage) LibraryPath() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/LibraryPath", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// SetLibraryPath sets the library path used by InjectJS().
func (p *WebPage) SetLibraryPath(path string) {
	p.ref.process.mustDoJSON("POST", "/webpage/SetLibraryPath", map[string]interface{}{"ref": p.ref.id, "path": path}, nil)
}

// NavigationLocked returns true if the navigation away from the page is disabled.
func (p *WebPage) NavigationLocked() bool {
	var resp struct {
		Value bool `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/NavigationLocked", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// SetNavigationLocked sets whether navigation away from the page should be disabled.
func (p *WebPage) SetNavigationLocked(value bool) {
	p.ref.process.mustDoJSON("POST", "/webpage/SetNavigationLocked", map[string]interface{}{"ref": p.ref.id, "value": value}, nil)
}

// OfflineStoragePath returns the path used by offline storage.
func (p *WebPage) OfflineStoragePath() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/OfflineStoragePath", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// OfflineStorageQuota returns the number of bytes that can be used for offline storage.
func (p *WebPage) OfflineStorageQuota() int {
	var resp struct {
		Value int `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/OfflineStorageQuota", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// OwnsPages returns true if this page owns pages opened in other windows.
func (p *WebPage) OwnsPages() bool {
	var resp struct {
		Value bool `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/OwnsPages", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// SetOwnsPages sets whether this page owns pages opened in other windows.
func (p *WebPage) SetOwnsPages(v bool) {
	p.ref.process.mustDoJSON("POST", "/webpage/SetOwnsPages", map[string]interface{}{"ref": p.ref.id, "value": v}, nil)
}

// PageWindowNames returns an list of owned window names.
func (p *WebPage) PageWindowNames() []string {
	var resp struct {
		Value []string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/PageWindowNames", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

// Pages returns a list of owned pages.
func (p *WebPage) Pages() []*WebPage {
	var resp struct {
		Refs []refJSON `json:"refs"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/Pages", map[string]interface{}{"ref": p.ref.id}, &resp)

	// Convert reference IDs to web pages.
	a := make([]*WebPage, len(resp.Refs))
	for i, ref := range resp.Refs {
		a[i] = &WebPage{ref: newRef(p.ref.process, ref.ID)}
	}
	return a
}

func (p *WebPage) PaperSize() string {
	panic("TODO")
}

func (p *WebPage) PlainText() string {
	panic("TODO")
}

func (p *WebPage) ScrollPosition() string {
	panic("TODO")
}

func (p *WebPage) Settings() string {
	panic("TODO")
}

func (p *WebPage) Title() string {
	panic("TODO")
}

// URL returns the current URL of the web page.
func (p *WebPage) URL() string {
	var resp struct {
		Value string `json:"value"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/URL", map[string]interface{}{"ref": p.ref.id}, &resp)
	return resp.Value
}

func (p *WebPage) ViewportSize() string {
	panic("TODO")
}

func (p *WebPage) WindowName() string {
	panic("TODO")
}

func (p *WebPage) ZoomFactor() string {
	panic("TODO")
}

func (p *WebPage) AddCookie() {
	panic("TODO")
}

func (p *WebPage) ChildFramesCount() {
	panic("TODO")
}

func (p *WebPage) ChildFramesName() {
	panic("TODO")
}

func (p *WebPage) ClearCookies() {
	panic("TODO")
}

// Close releases the web page and its resources.
func (p *WebPage) Close() {
	p.ref.process.mustDoJSON("POST", "/webpage/Close", map[string]interface{}{"ref": p.ref.id}, nil)
}

func (p *WebPage) CurrentFrameName() {
	panic("TODO")
}

func (p *WebPage) DeleteCookie() {
	panic("TODO")
}

func (p *WebPage) EvaluateAsync() {
	panic("TODO")
}

// EvaluateJavaScript executes a JavaScript function.
// Returns the value returned by the function.
func (p *WebPage) EvaluateJavaScript(script string) interface{} {
	var resp struct {
		ReturnValue interface{} `json:"returnValue"`
	}
	p.ref.process.mustDoJSON("POST", "/webpage/EvaluateJavaScript", map[string]interface{}{"ref": p.ref.id, "script": script}, &resp)
	return resp.ReturnValue
}

func (p *WebPage) Evaluate() {
	panic("TODO")
}

func (p *WebPage) GetPage() {
	panic("TODO")
}

func (p *WebPage) GoBack() {
	panic("TODO")
}

func (p *WebPage) GoForward() {
	panic("TODO")
}

func (p *WebPage) Go() {
	panic("TODO")
}

func (p *WebPage) IncludeJs() {
	panic("TODO")
}

func (p *WebPage) InjectJs() {
	panic("TODO")
}

func (p *WebPage) OpenUrl() {
	panic("TODO")
}

func (p *WebPage) Release() {
	panic("TODO")
}

func (p *WebPage) Reload() {
	panic("TODO")
}

func (p *WebPage) RenderBase64() {
	panic("TODO")
}

func (p *WebPage) RenderBuffer() {
	panic("TODO")
}

func (p *WebPage) Render() {
	panic("TODO")
}

func (p *WebPage) SendEvent() {
	panic("TODO")
}

func (p *WebPage) SetContentAndURL() {
	panic("TODO")
}

func (p *WebPage) Stop() {
	panic("TODO")
}

func (p *WebPage) SwitchToChildFrame() {
	panic("TODO")
}

func (p *WebPage) SwitchToFocusedFrame() {
	panic("TODO")
}

// SwitchToFrameName changes focus to the named frame.
func (p *WebPage) SwitchToFrameName(name string) {
	p.ref.process.mustDoJSON("POST", "/webpage/SwitchToFrameName", map[string]interface{}{"ref": p.ref.id, "name": name}, nil)
}

// SwitchToFramePosition changes focus to a frame at the given position.
func (p *WebPage) SwitchToFramePosition(pos int) {
	p.ref.process.mustDoJSON("POST", "/webpage/SwitchToFramePosition", map[string]interface{}{"ref": p.ref.id, "position": pos}, nil)
}

func (p *WebPage) SwitchToMainFrame() {
	panic("TODO")
}

func (p *WebPage) SwitchToParentFrame() {
	panic("TODO")
}

func (p *WebPage) UploadFile() {
	panic("TODO")
}

// OpenWebPageSettings represents the settings object passed to WebPage.Open().
type OpenWebPageSettings struct {
	Method string `json:"method"`
}

// Ref represents a reference to an object in phantomjs.
type Ref struct {
	process *Process
	id      string
}

// newRef returns a new instance of a referenced object within the process.
func newRef(p *Process, id string) *Ref {
	return &Ref{process: p, id: id}
}

// ID returns the reference identifier.
func (r *Ref) ID() string {
	return r.id
}

// refJSON is a struct for encoding refs as JSON.
type refJSON struct {
	ID string `json:"id"`
}

// Rect represents a rectangle used by WebPage.ClipRect().
type Rect struct {
	Top    int
	Left   int
	Width  int
	Height int
}

// rectJSON is a struct for encoding rects as JSON.
type rectJSON struct {
	Top    int `json:"top"`
	Left   int `json:"left"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// cookieJSON is a struct for encoding http.Cookie objects as JSON.
type cookieJSON struct {
	Domain   string `json:"domain"`
	Expires  string `json:"expires"`
	Expiry   int    `json:"expiry"`
	HttpOnly bool   `json:"httponly"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	Value    string `json:"value"`
}

func encodeCookieJSON(v *http.Cookie) cookieJSON {
	out := cookieJSON{
		Domain:   v.Domain,
		HttpOnly: v.HttpOnly,
		Name:     v.Name,
		Path:     v.Path,
		Secure:   v.Secure,
		Value:    v.Value,
	}

	if !v.Expires.IsZero() {
		out.Expires = v.Expires.UTC().Format(http.TimeFormat)
	}
	return out
}

func decodeCookieJSON(v cookieJSON) *http.Cookie {
	out := &http.Cookie{
		Domain:     v.Domain,
		RawExpires: v.Expires,
		HttpOnly:   v.HttpOnly,
		Name:       v.Name,
		Path:       v.Path,
		Secure:     v.Secure,
		Value:      v.Value,
	}

	if v.Expires != "" {
		expires, err := time.Parse(http.TimeFormat, v.Expires)
		if err != nil {
			panic(err)
		}
		out.Expires = expires
		out.RawExpires = v.Expires
	}

	return out
}

// shim is the included javascript used to communicate with PhantomJS.
const shim = `
var system = require("system")
var webpage = require('webpage');
var webserver = require('webserver');

/*
 * HTTP API
 */

// Serves RPC API.
var server = webserver.create();
server.listen(system.env["PORT"], function(request, response) {
	try {
		switch (request.url) {
			case '/ping': return handlePing(request, response);
			case '/webpage/CanGoBack': return handleWebpageCanGoBack(request, response);
			case '/webpage/CanGoForward': return handleWebpageCanGoForward(request, response);
			case '/webpage/ClipRect': return handleWebpageClipRect(request, response);
			case '/webpage/SetClipRect': return handleWebpageSetClipRect(request, response);
			case '/webpage/Cookies': return handleWebpageCookies(request, response);
			case '/webpage/SetCookies': return handleWebpageSetCookies(request, response);
			case '/webpage/CustomHeaders': return handleWebpageCustomHeaders(request, response);
			case '/webpage/SetCustomHeaders': return handleWebpageSetCustomHeaders(request, response);
			case '/webpage/Create': return handleWebpageCreate(request, response);
			case '/webpage/Content': return handleWebpageContent(request, response);
			case '/webpage/SetContent': return handleWebpageSetContent(request, response);
			case '/webpage/FocusedFrameName': return handleWebpageFocusedFrameName(request, response);
			case '/webpage/FrameContent': return handleWebpageFrameContent(request, response);
			case '/webpage/SetFrameContent': return handleWebpageSetFrameContent(request, response);
			case '/webpage/FrameName': return handleWebpageFrameName(request, response);
			case '/webpage/FramePlainText': return handleWebpageFramePlainText(request, response);
			case '/webpage/FrameTitle': return handleWebpageFrameTitle(request, response);
			case '/webpage/FrameURL': return handleWebpageFrameURL(request, response);
			case '/webpage/FrameCount': return handleWebpageFrameCount(request, response);
			case '/webpage/FrameNames': return handleWebpageFrameNames(request, response);
			case '/webpage/LibraryPath': return handleWebpageLibraryPath(request, response);
			case '/webpage/SetLibraryPath': return handleWebpageSetLibraryPath(request, response);
			case '/webpage/NavigationLocked': return handleWebpageNavigationLocked(request, response);
			case '/webpage/SetNavigationLocked': return handleWebpageSetNavigationLocked(request, response);
			case '/webpage/OfflineStoragePath': return handleWebpageOfflineStoragePath(request, response);
			case '/webpage/OfflineStorageQuota': return handleWebpageOfflineStorageQuota(request, response);
			case '/webpage/OwnsPages': return handleWebpageOwnsPages(request, response);
			case '/webpage/SetOwnsPages': return handleWebpageSetOwnsPages(request, response);
			case '/webpage/PageWindowNames': return handleWebpagePageWindowNames(request, response);
			case '/webpage/Pages': return handleWebpagePages(request, response);

			case '/webpage/URL': return handleWebpageURL(request, response);
			
			case '/webpage/SwitchToFrameName': return handleWebpageSwitchToFrameName(request, response);
			case '/webpage/SwitchToFramePosition': return handleWebpageSwitchToFramePosition(request, response);
			case '/webpage/Open': return handleWebpageOpen(request, response);
			case '/webpage/Close': return handleWebpageClose(request, response);
			case '/webpage/EvaluateJavaScript': return handleWebpageEvaluateJavaScript(request, response);
			default: return handleNotFound(request, response);
		}
	} catch(e) {
		response.statusCode = 500;
		response.write(request.url + ": " + e.message);
		response.closeGracefully();
	}
});

function handlePing(request, response) {
	response.statusCode = 200;
	response.write('ok');
	response.closeGracefully();
}

function handleWebpageCanGoBack(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.canGoBack}));
	response.closeGracefully();
}

function handleWebpageCanGoForward(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.canGoForward}));
	response.closeGracefully();
}

function handleWebpageClipRect(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.clipRect}));
	response.closeGracefully();
}

function handleWebpageSetClipRect(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.clipRect = msg.rect;
	response.closeGracefully();
}

function handleWebpageCookies(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.cookies}));
	response.closeGracefully();
}

function handleWebpageSetCookies(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.cookies = msg.cookies;
	response.closeGracefully();
}

function handleWebpageCustomHeaders(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.customHeaders}));
	response.closeGracefully();
}

function handleWebpageSetCustomHeaders(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.customHeaders = msg.headers;
	response.closeGracefully();
}

function handleWebpageCreate(request, response) {
	var ref = createRef(webpage.create());
	response.statusCode = 200;
	response.write(JSON.stringify({ref: ref}));
	response.closeGracefully();
}

function handleWebpageOpen(request, response) {
	var msg = JSON.parse(request.post)
	var page = ref(msg.ref)
	page.open(msg.url, function(status) {
		response.write(JSON.stringify({status: status}));
		response.closeGracefully();
	})
}

function handleWebpageContent(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.content}));
	response.closeGracefully();
}

function handleWebpageSetContent(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.content = msg.content;
	response.closeGracefully();
}

function handleWebpageFocusedFrameName(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.focusedFrameName}));
	response.closeGracefully();
}

function handleWebpageFrameContent(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.frameContent}));
	response.closeGracefully();
}

function handleWebpageSetFrameContent(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.frameContent = msg.content;
	response.closeGracefully();
}

function handleWebpageFrameName(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.frameName}));
	response.closeGracefully();
}

function handleWebpageFramePlainText(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.framePlainText}));
	response.closeGracefully();
}

function handleWebpageFrameTitle(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.frameTitle}));
	response.closeGracefully();
}

function handleWebpageFrameURL(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.frameUrl}));
	response.closeGracefully();
}

function handleWebpageFrameCount(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.framesCount}));
	response.closeGracefully();
}

function handleWebpageFrameNames(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.framesName}));
	response.closeGracefully();
}

function handleWebpageLibraryPath(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.libraryPath}));
	response.closeGracefully();
}

function handleWebpageSetLibraryPath(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.libraryPath = msg.path;
	response.closeGracefully();
}

function handleWebpageNavigationLocked(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.navigationLocked}));
	response.closeGracefully();
}

function handleWebpageSetNavigationLocked(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.navigationLocked = msg.value;
	response.closeGracefully();
}

function handleWebpageOfflineStoragePath(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.offlineStoragePath}));
	response.closeGracefully();
}

function handleWebpageOfflineStorageQuota(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.offlineStorageQuota}));
	response.closeGracefully();
}

function handleWebpageOwnsPages(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.ownsPages}));
	response.closeGracefully();
}

function handleWebpageSetOwnsPages(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.ownsPages = msg.value;
	response.closeGracefully();
}

function handleWebpagePageWindowNames(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.pagesWindowName}));
	response.closeGracefully();
}

function handleWebpagePages(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	var refs = page.pages.map(function(p) { return createRef(p); })
	response.write(JSON.stringify({refs: refs}));
	response.closeGracefully();
}


function handleWebpageURL(request, response) {
	var page = ref(JSON.parse(request.post).ref);
	response.write(JSON.stringify({value: page.url}));
	response.closeGracefully();
}


function handleWebpageSwitchToFrameName(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.switchToFrame(msg.name);
	response.closeGracefully();
}

function handleWebpageSwitchToFramePosition(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	page.switchToFrame(msg.position);
	response.closeGracefully();
}

function handleWebpageClose(request, response) {
	var msg = JSON.parse(request.post);

	// Close page.
	var page = ref(msg.ref);
	page.close();
	delete(refs, msg.ref);

	// Close and dereference owned pages.
	for (var i = 0; i < page.pages.length; i++) {
		page.pages[i].close();
		deleteRef(page.pages[i]);
	}

	response.statusCode = 200;
	response.closeGracefully();
}

function handleWebpageEvaluateJavaScript(request, response) {
	var msg = JSON.parse(request.post);
	var page = ref(msg.ref);
	var returnValue = page.evaluateJavaScript(msg.script);
	response.statusCode = 200;
	response.write(JSON.stringify({returnValue: returnValue}));
	response.closeGracefully();
}

function handleNotFound(request, response) {
	response.statusCode = 404;
	response.write('not found');
	response.closeGracefully();
}


/*
 * REFS
 */

// Holds references to remote objects.
var refID = 0;
var refs = {};

// Adds an object to the reference map and a ref object.
function createRef(value) {
	// Return existing reference, if one exists.
	for (var key in refs) {
		if (refs.hasOwnProperty(key)) {
			if (refs[key] === value) {
				return key
			}
		}
	}

	// Generate a new id for new references.
	refID++;
	refs[refID.toString()] = value;
	return {id: refID.toString()};
}

// Removes a reference to a value, if any.
function deleteRef(value) {
	for (var key in refs) {
		if (refs.hasOwnProperty(key)) {
			if (refs[key] === value) {
				delete(refs, key);
			}
		}
	}
}

// Returns a reference object by ID.
function ref(id) {
	return refs[id];
}
`
