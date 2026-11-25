package moodle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	testUser     = "pupil"
	testPassword = "s3cret-pass"
	testToken    = "0123456789abcdef0123456789abcdef"
)

// fixtureFor maps each web-service function to its canned answer.
var fixtureFor = map[string]string{
	"core_webservice_get_site_info":          "site_info.json",
	"core_enrol_get_users_courses":           "courses.json",
	"gradereport_overview_get_course_grades": "course_grades.json",
	"gradereport_user_get_grade_items":       "grade_items.json",
	"core_course_get_contents":               "contents.json",
}

type recordedRequest struct {
	method   string
	path     string
	rawQuery string
	form     map[string]string
}

// fakeMoodle answers like a Moodle site: a token for the right credentials, an
// error object for the wrong ones, and canned fixtures for each REST function.
// status and body let a test override the answer to one function.
type fakeMoodle struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []recordedRequest
	status map[string]int    // wsfunction → HTTP status
	body   map[string]string // wsfunction → raw body
}

func (f *fakeMoodle) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			f.t.Errorf("parse form: %v", err)
		}
		rec := recordedRequest{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, form: map[string]string{}}
		for k := range r.PostForm {
			rec.form[k] = r.PostForm.Get(k)
		}
		f.mu.Lock()
		f.calls = append(f.calls, rec)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/token.php":
			if rec.form["username"] == testUser && rec.form["password"] == testPassword && rec.form["service"] == mobileService {
				f.serveFile(w, "login_ok.json")
			} else {
				f.serveFile(w, "login_bad.json")
			}
		case "/webservice/rest/server.php":
			fn := rec.form["wsfunction"]
			if code, ok := f.status[fn]; ok {
				w.WriteHeader(code)
				return
			}
			if raw, ok := f.body[fn]; ok {
				_, _ = w.Write([]byte(raw))
				return
			}
			if rec.form["wstoken"] != testToken {
				f.serveFile(w, "exception.json")
				return
			}
			name, ok := fixtureFor[fn]
			if !ok {
				f.t.Errorf("unexpected wsfunction %q", fn)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.serveFile(w, name)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *fakeMoodle) serveFile(w http.ResponseWriter, name string) {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		f.t.Fatalf("fixture %s: %v", name, err)
	}
	_, _ = w.Write(b)
}

func newFake(t *testing.T) (*fakeMoodle, *Client) {
	t.Helper()
	f := &fakeMoodle{t: t, status: map[string]int{}, body: map[string]string{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, NewClient(srv.URL + "/")
}

func loggedIn(t *testing.T) (*fakeMoodle, *Client) {
	t.Helper()
	f, c := newFake(t)
	if err := c.Login(context.Background(), testUser, testPassword); err != nil {
		t.Fatalf("login: %v", err)
	}
	return f, c
}

func TestLoginStoresToken(t *testing.T) {
	_, c := loggedIn(t)
	if c.token != testToken {
		t.Fatalf("token = %q, want %q", c.token, testToken)
	}
}

// A wrong password is HTTP 200 with an error object. The error has to carry
// Moodle's errorcode so the log says why, and must not leak the credentials.
func TestLoginWrongPassword(t *testing.T) {
	_, c := newFake(t)
	err := c.Login(context.Background(), testUser, "wrong-password")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.ErrorCode != "invalidlogin" {
		t.Fatalf("errorcode = %q, want invalidlogin", apiErr.ErrorCode)
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalidlogin") {
		t.Fatalf("error %q does not name the errorcode", msg)
	}
	for _, secret := range []string{testUser, "wrong-password"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("error %q leaks %q", msg, secret)
		}
	}
	if c.token != "" {
		t.Fatal("failed login left a token behind")
	}
}

// An answer with neither a token nor an error is a failed login too, not a
// session with an empty token.
func TestLoginWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	err := c.Login(context.Background(), testUser, testPassword)
	if err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("err = %v, want a no-token error", err)
	}
	if c.token != "" {
		t.Fatalf("token = %q after a tokenless answer", c.token)
	}
}

func TestCallBeforeLoginFails(t *testing.T) {
	f, c := newFake(t)
	if _, err := c.SiteInfo(context.Background()); err == nil {
		t.Fatal("call without a token succeeded")
	}
	if len(f.calls) != 0 {
		t.Fatalf("made %d requests without a token", len(f.calls))
	}
}

func TestSiteInfo(t *testing.T) {
	_, c := loggedIn(t)
	info, err := c.SiteInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.UserID != 1001 {
		t.Fatalf("userid = %d, want 1001", info.UserID)
	}
}

func TestCourses(t *testing.T) {
	f, c := loggedIn(t)
	courses, err := c.Courses(context.Background(), 1001)
	if err != nil {
		t.Fatal(err)
	}
	want := []Course{{ID: 201, FullName: "Алгебра 9"}, {ID: 202, FullName: "Фізика 9"}}
	if len(courses) != len(want) {
		t.Fatalf("got %d courses, want %d", len(courses), len(want))
	}
	for i := range want {
		if courses[i].ID != want[i].ID || courses[i].FullName != want[i].FullName || !courses[i].GradesShown() {
			t.Errorf("course %d = %+v, want %+v with grades shown", i, courses[i], want[i])
		}
	}
	if got := f.last().form["userid"]; got != "1001" {
		t.Fatalf("userid param = %q, want 1001", got)
	}
}

// A course can hide its gradebook from pupils; a site that does not send the
// setting at all is taken to show it.
func TestCourseGradesShown(t *testing.T) {
	f, c := loggedIn(t)
	f.body["core_enrol_get_users_courses"] = `[
 {"id":1,"fullname":"A","showgrades":true},
 {"id":2,"fullname":"B","showgrades":false},
 {"id":3,"fullname":"C"}
]`
	courses, err := c.Courses(context.Background(), 1001)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, true}
	for i, c := range courses {
		if c.GradesShown() != want[i] {
			t.Errorf("course %d: GradesShown = %v, want %v", c.ID, c.GradesShown(), want[i])
		}
	}
}

func TestCourseAverages(t *testing.T) {
	_, c := loggedIn(t)
	avgs, err := c.CourseAverages(context.Background(), 1001)
	if err != nil {
		t.Fatal(err)
	}
	if len(avgs) != 2 {
		t.Fatalf("got %d averages, want 2", len(avgs))
	}
	if a := avgs[0]; a.CourseID != 201 || a.Grade != "5,5" || !a.RawGrade.Valid || a.RawGrade.V != 5.5 {
		t.Errorf("graded course = %+v", a)
	}
	if a := avgs[1]; a.CourseID != 202 || a.Grade != "-" || a.RawGrade.Valid {
		t.Errorf("ungraded course = %+v", a)
	}
}

func TestOptFloat(t *testing.T) {
	cases := []struct {
		in    string
		v     float64
		valid bool
	}{
		{`null`, 0, false},
		{`""`, 0, false},
		{`"-"`, 0, false},
		{`7.25`, 7.25, true},
		{`"5.50000"`, 5.5, true},
		{`"5,5"`, 5.5, true},
		{`0`, 0, true},
	}
	for _, tc := range cases {
		var f OptFloat
		if err := f.UnmarshalJSON([]byte(tc.in)); err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if f.Valid != tc.valid || f.V != tc.v {
			t.Errorf("%s = %+v, want {%v %v}", tc.in, f, tc.v, tc.valid)
		}
	}
	var f OptFloat
	if err := f.UnmarshalJSON([]byte(`"abc"`)); err == nil {
		t.Error("non-numeric string decoded without error")
	}
}

func TestGradeItems(t *testing.T) {
	f, c := loggedIn(t)
	items, err := c.GradeItems(context.Background(), 201, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	graded := items[0]
	if graded.ID != 3001 || graded.ItemName != "Оцінка вчителя 10" || graded.ItemType != "mod" ||
		graded.ItemModule != "lesson" || graded.ItemInstance != 501 || graded.GradeRaw == nil ||
		*graded.GradeRaw != 7.8 || graded.GradeFormatted != "7,8" || graded.GradeDateGraded != 1794643200 {
		t.Errorf("graded item = %+v", graded)
	}
	ungraded := items[1]
	if ungraded.GradeRaw != nil || ungraded.GradeDateGraded != 0 || ungraded.GradeFormatted != "-" {
		t.Errorf("ungraded item = %+v", ungraded)
	}
	total := items[2]
	if total.ItemType != "course" || total.ItemName != "" || total.ItemModule != "" {
		t.Errorf("course total = %+v", total)
	}
	last := f.last()
	if last.form["courseid"] != "201" || last.form["userid"] != "1001" {
		t.Fatalf("params = %v", last.form)
	}
}

func TestContents(t *testing.T) {
	f, c := loggedIn(t)
	sections, err := c.Contents(context.Background(), 201)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(sections))
	}
	week := sections[1]
	if week.Name != "Тиждень 12. Квадратні рівняння" || len(week.Modules) != 3 {
		t.Fatalf("week section = %+v", week)
	}
	if m := week.Modules[0]; m != (Module{Instance: 501, ModName: "lesson"}) {
		t.Errorf("module = %+v", m)
	}
	if m := week.Modules[2]; m.ModName != "label" {
		t.Errorf("label module = %+v", m)
	}
	if got := f.last().form["courseid"]; got != "201" {
		t.Fatalf("courseid param = %q", got)
	}
}

// Every request is a POST with an empty query string: the token, the
// credentials and the function name all travel in the body, so a transport
// error quoting the URL cannot leak them.
func TestSecretsNeverInURL(t *testing.T) {
	f, c := loggedIn(t)
	ctx := context.Background()
	if _, err := c.SiteInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GradeItems(ctx, 201, 1001); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("got %d requests, want 3", len(f.calls))
	}
	for _, r := range f.calls {
		if r.method != http.MethodPost {
			t.Errorf("%s used %s, want POST", r.path, r.method)
		}
		if r.rawQuery != "" {
			t.Errorf("%s carried query %q", r.path, r.rawQuery)
		}
	}
	for _, r := range f.calls[1:] {
		if r.form["wstoken"] != testToken || r.form["moodlewsrestformat"] != "json" {
			t.Errorf("REST call form = %v", r.form)
		}
	}
}

// A transport failure quotes the URL in its error; with the token in the body
// that URL has nothing secret in it.
func TestTransportErrorDoesNotLeakToken(t *testing.T) {
	_, c := loggedIn(t)
	c.baseURL = "http://127.0.0.1:1" // nothing listens here
	_, err := c.SiteInfo(context.Background())
	if err == nil {
		t.Fatal("call to a dead host succeeded")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error %q leaks the token", err)
	}
}

func TestExceptionIsTypedError(t *testing.T) {
	_, c := loggedIn(t)
	c.token = "stale-token"
	_, err := c.SiteInfo(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Function != "core_webservice_get_site_info" || apiErr.ErrorCode != "invalidtoken" {
		t.Fatalf("api error = %+v", apiErr)
	}
	if strings.Contains(err.Error(), "stale-token") {
		t.Fatalf("error %q leaks the token", err)
	}
}

// An exception on a function whose success answer is an array must still be
// recognised, not reported as a JSON type mismatch.
func TestExceptionOnArrayFunction(t *testing.T) {
	f, c := loggedIn(t)
	f.body["core_enrol_get_users_courses"] = `{"exception":"required_capability_exception","errorcode":"nopermissions","message":"Sorry"}`
	_, err := c.Courses(context.Background(), 1001)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != "nopermissions" {
		t.Fatalf("err = %v, want nopermissions APIError", err)
	}
}

func TestHTTPServerError(t *testing.T) {
	f, c := loggedIn(t)
	f.status["gradereport_user_get_grade_items"] = http.StatusBadGateway
	_, err := c.GradeItems(context.Background(), 201, 1001)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want HTTP 502", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatal("HTTP failure mistaken for an API error")
	}
}

func TestBrokenJSON(t *testing.T) {
	f, c := loggedIn(t)
	f.body["core_course_get_contents"] = `[{"id": 1, "name": `
	_, err := c.Contents(context.Background(), 201)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err = %v, want a decode error", err)
	}
}

func TestLoginServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	err := NewClient(srv.URL).Login(context.Background(), testUser, testPassword)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want HTTP 503", err)
	}
	for _, secret := range []string{testUser, testPassword} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaks %q", err, secret)
		}
	}
}

func (f *fakeMoodle) last() recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}
