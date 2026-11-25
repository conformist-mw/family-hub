// Package moodle mirrors a pupil's grades and weekly progress from a Moodle
// site into the local database. It speaks the Moodle mobile web-service API: a
// username/password login at /login/token.php yields a token for the
// moodle_mobile_app service, and every read is a REST call to
// /webservice/rest/server.php with that token.
package moodle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// mobileService is the web service the Moodle app logs into. It is enabled on
// any site that lets pupils use the mobile app, so a plain account can reach
// the read functions this package needs without an admin-issued token.
const mobileService = "moodle_mobile_app"

// maxBody caps how much of one response is read. Course contents are the
// largest answer (a section per week, a module per lesson) and stay well under
// a megabyte; the cap only stops a misbehaving server from exhausting memory.
const maxBody = 16 << 20

// APIError is a failure Moodle reported in-band. The web service answers
// errors with HTTP 200 and a JSON body, so the status code alone cannot tell
// success from failure: the body has to be inspected for an exception (REST
// calls) or an error field (login).
type APIError struct {
	Function  string // wsfunction, or "login"
	Exception string // exception class; empty for login errors
	ErrorCode string // e.g. "invalidtoken", "invalidlogin"
	Message   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("moodle: %s: %s (%s)", e.Function, e.Message, e.ErrorCode)
}

// Client is one session against a Moodle site. It is not safe for concurrent
// use; the sync service serialises access to it. Login must succeed before any
// other call.
type Client struct {
	baseURL string
	http    *http.Client
	token   string
}

// NewClient builds a client for baseURL (the site root, without a trailing
// /webservice). The 30s timeout bounds a site that stalls mid-answer.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Login exchanges the credentials for a web-service token. The credentials go
// in the POST body and never appear in a returned error, which only carries
// Moodle's errorcode and message.
func (c *Client) Login(ctx context.Context, username, password string) error {
	form := url.Values{
		"username": {username},
		"password": {password},
		"service":  {mobileService},
	}
	body, err := c.post(ctx, "/login/token.php", form, "login")
	if err != nil {
		return err
	}
	var resp struct {
		Token     string `json:"token"`
		Error     string `json:"error"`
		ErrorCode string `json:"errorcode"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("moodle: login: decode response: %w", err)
	}
	if resp.Error != "" || resp.ErrorCode != "" {
		return &APIError{Function: "login", ErrorCode: resp.ErrorCode, Message: resp.Error}
	}
	if resp.Token == "" {
		return errors.New("moodle: login: response carried no token")
	}
	c.token = resp.Token
	return nil
}

// call invokes one web-service function and decodes its JSON answer into out.
// The token and every parameter travel in the POST body, never the query
// string: an *url.Error from net/http quotes the request URL, so anything in
// the query would end up in logs the moment the network hiccups.
func (c *Client) call(ctx context.Context, fn string, params url.Values, out any) error {
	if c.token == "" {
		return fmt.Errorf("moodle: %s: not logged in", fn)
	}
	form := url.Values{}
	for k, v := range params {
		form[k] = v
	}
	form.Set("wstoken", c.token)
	form.Set("wsfunction", fn)
	form.Set("moodlewsrestformat", "json")

	body, err := c.post(ctx, "/webservice/rest/server.php", form, fn)
	if err != nil {
		return err
	}
	// Errors come back as an object with an exception field. Successful answers
	// may be arrays, so the probe only runs on objects.
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '{' {
		var exc struct {
			Exception string `json:"exception"`
			ErrorCode string `json:"errorcode"`
			Message   string `json:"message"`
		}
		if err := json.Unmarshal(trimmed, &exc); err == nil && exc.Exception != "" {
			return &APIError{Function: fn, Exception: exc.Exception, ErrorCode: exc.ErrorCode, Message: exc.Message}
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("moodle: %s: decode response: %w", fn, err)
	}
	return nil
}

// post sends a form-encoded POST to path and returns the body of a 200 answer.
// label names the operation in errors.
func (c *Client) post(ctx context.Context, path string, form url.Values, label string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("moodle: %s: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moodle: %s: %w", label, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("moodle: %s: read response: %w", label, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("moodle: %s: HTTP %d", label, resp.StatusCode)
	}
	return body, nil
}

// SiteInfo is the part of core_webservice_get_site_info this package needs:
// whose token it is.
type SiteInfo struct {
	UserID int64 `json:"userid"`
}

// SiteInfo returns the logged-in user's id, which every per-user call takes.
func (c *Client) SiteInfo(ctx context.Context) (SiteInfo, error) {
	var info SiteInfo
	err := c.call(ctx, "core_webservice_get_site_info", nil, &info)
	return info, err
}

// Course is one enrolled course.
type Course struct {
	ID       int64  `json:"id"`
	FullName string `json:"fullname"`
}

// Courses lists the courses userID is enrolled in, in Moodle's own order.
func (c *Client) Courses(ctx context.Context, userID int64) ([]Course, error) {
	var courses []Course
	err := c.call(ctx, "core_enrol_get_users_courses", userParams(userID), &courses)
	return courses, err
}

// CourseAverage is one course's overall grade from the overview report. Grade
// is formatted for display ("5,5", or "-" when nothing is graded yet); RawGrade
// is the number behind it, not Valid when there is none.
type CourseAverage struct {
	CourseID int64    `json:"courseid"`
	Grade    string   `json:"grade"`
	RawGrade OptFloat `json:"rawgrade"`
}

// CourseAverages returns the overall grade of every course userID is enrolled
// in.
func (c *Client) CourseAverages(ctx context.Context, userID int64) ([]CourseAverage, error) {
	var resp struct {
		Grades []CourseAverage `json:"grades"`
	}
	err := c.call(ctx, "gradereport_overview_get_course_grades", userParams(userID), &resp)
	return resp.Grades, err
}

// GradeItem is one row of a course's gradebook. ItemType "mod" is an activity;
// "course" and "category" are computed totals. GradeRaw is nil until a teacher
// grades the item, and GradeDateGraded is 0 when Moodle has no date for it.
type GradeItem struct {
	ID              int64    `json:"id"`
	ItemName        string   `json:"itemname"`
	ItemType        string   `json:"itemtype"`
	ItemModule      string   `json:"itemmodule"`
	ItemInstance    int64    `json:"iteminstance"`
	GradeRaw        *float64 `json:"graderaw"`
	GradeFormatted  string   `json:"gradeformatted"`
	GradeDateGraded int64    `json:"gradedategraded"`
}

// GradeItems returns the gradebook of one course for userID.
func (c *Client) GradeItems(ctx context.Context, courseID, userID int64) ([]GradeItem, error) {
	params := userParams(userID)
	params.Set("courseid", strconv.FormatInt(courseID, 10))
	var resp struct {
		UserGrades []struct {
			GradeItems []GradeItem `json:"gradeitems"`
		} `json:"usergrades"`
	}
	if err := c.call(ctx, "gradereport_user_get_grade_items", params, &resp); err != nil {
		return nil, err
	}
	var items []GradeItem
	for _, ug := range resp.UserGrades {
		items = append(items, ug.GradeItems...)
	}
	return items, nil
}

// Section is one section of a course page; on this site each is a week
// ("Тиждень N").
type Section struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Number  int      `json:"section"`
	Modules []Module `json:"modules"`
}

// Module is one activity or resource in a section. ID is the course-module id
// (cmid); Instance is the activity's own id, which a grade item's iteminstance
// refers to.
type Module struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Instance int64  `json:"instance"`
	ModName  string `json:"modname"`
}

// Contents returns the sections of one course with their modules.
func (c *Client) Contents(ctx context.Context, courseID int64) ([]Section, error) {
	params := url.Values{"courseid": {strconv.FormatInt(courseID, 10)}}
	var sections []Section
	err := c.call(ctx, "core_course_get_contents", params, &sections)
	return sections, err
}

func userParams(userID int64) url.Values {
	return url.Values{"userid": {strconv.FormatInt(userID, 10)}}
}

// OptFloat is a number Moodle may send as a JSON number, a numeric string, or
// null. Report fields declared PARAM_RAW server-side arrive as strings
// ("5.50000"), and "", "-" or null mean there is no value; Valid tells that
// apart from a real zero.
type OptFloat struct {
	V     float64
	Valid bool
}

// UnmarshalJSON accepts a number, a numeric string (dot or comma decimal), or
// null/empty/"-" for no value.
func (f *OptFloat) UnmarshalJSON(b []byte) error {
	*f = OptFloat{}
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.ReplaceAll(strings.TrimSpace(str), ",", ".")
		if s == "" || s == "-" {
			return nil
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("moodle: not a number: %q", s)
	}
	*f = OptFloat{V: v, Valid: true}
	return nil
}
