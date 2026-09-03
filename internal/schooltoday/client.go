// Package schooltoday mirrors a child's academic timetable from the
// school-today.com pupil portal into the local database, and serves it to the
// ICS feed. The portal publishes a documented Open API, but only to an
// admin-issued X-API-Key the family does not hold; a parent login, however,
// reaches the same data through the portal's own internal endpoints. This
// package drives that: it authenticates like the browser does and reads the
// timetable JSON the web UI reads.
package schooltoday

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// identityCookie is the auth cookie ASP.NET Core Identity sets on a successful
// form login. Its presence in the jar is how a login is judged to have worked:
// a wrong password re-renders the login page with HTTP 200, so the status code
// alone cannot tell success from failure.
const identityCookie = ".AspNetCore.Identity.Application"

// portalDateFormat is how the timetable endpoint wants the week anchor: the
// American MM.DD.YYYY the portal's own JavaScript sends.
const portalDateFormat = "01.02.2006"

// tokenRe pulls the antiforgery token out of the login form. The hidden input
// is rendered name-then-value; the class of `[^>]*` keeps it matching if other
// attributes are ever inserted between them.
var tokenRe = regexp.MustCompile(`name="__RequestVerificationToken"[^>]*value="([^"]+)"`)

// Client is a logged-in session against one portal host. It is not safe for
// concurrent use — the sync loop drives it from a single goroutine — and holds
// its auth in a cookie jar, so Login must be called before Timetable.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a client for baseURL (e.g. "https://school-today.com") with
// its own cookie jar. The jar is what carries the session between Login and the
// timetable calls; the 30s timeout bounds a portal that occasionally stalls.
func NewClient(baseURL string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Jar: jar, Timeout: 30 * time.Second},
	}
}

// Login performs the portal's antiforgery-protected form login: fetch the form
// for a token (and the matching cookie the jar keeps), then POST the
// credentials. Step=Login is the field the server branches on — omit it and the
// POST falls through and never authenticates. The privacy-policy checkbox the
// page shows is client-side only and is not submitted.
func (c *Client) Login(ctx context.Context, email, password string) error {
	token, err := c.antiforgeryToken(ctx)
	if err != nil {
		return err
	}

	form := url.Values{
		"__RequestVerificationToken": {token},
		"Step":                       {"Login"},
		"Email":                      {email},
		"Password":                   {password},
		"RememberMe":                 {"true"},
		"Verification":               {""},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/Account/Login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("school-today: login POST: %w", err)
	}
	// The body is the redirect target or the re-rendered form; either way it is
	// drained and closed so the connection can be reused.
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if !c.authenticated() {
		return fmt.Errorf("school-today: login failed for %s (check credentials)", email)
	}
	return nil
}

// antiforgeryToken GETs the login form and extracts the __RequestVerificationToken.
// The same request seeds the jar with the antiforgery cookie the token is bound
// to; the POST is rejected if the two do not arrive together.
func (c *Client) antiforgeryToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/Account/Login", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("school-today: fetch login form: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	m := tokenRe.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("school-today: no antiforgery token in login form (portal layout changed?)")
	}
	return string(m[1]), nil
}

// authenticated reports whether the jar holds the Identity cookie for the base
// host — the signal that the last login POST was accepted.
func (c *Client) authenticated() bool {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return false
	}
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == identityCookie && ck.Value != "" {
			return true
		}
	}
	return false
}

// Event is one timetable slot as the portal's TimetableApi returns it. Only the
// fields this package uses are decoded; the portal sends many more.
type Event struct {
	EventID int64  `json:"eventID"`
	Subject string `json:"subject"`
	// Type is what kind of slot this is, and the lesson detail endpoint wants
	// it back as lessonType. Academic lessons are 1; meals and after-school
	// care are 0 and 3, and asking for their detail is a 404.
	Type       int     `json:"type"`
	Start      string  `json:"start"` // 2006-01-02T15:04:05, naive local
	End        string  `json:"end"`
	Topic      *string `json:"topic"` // null until a teacher fills it in
	ThemeColor string  `json:"themeColor"`
	HasMarks   bool    `json:"hasMarks"`
	IsFullDay  bool    `json:"isFullDay"`
	IsDeleted  bool    `json:"isDeleted"`
	IsCanceled bool    `json:"isCanceled"`
}

// timetableResponse is the envelope around the week's events.
type timetableResponse struct {
	Events []Event `json:"events"`
}

// Timetable returns the pupil's events for the week containing weekStart. The
// portal keys the week off the date passed and returns Monday–Sunday; the
// caller steps weekStart in 7-day strides to cover a horizon. A 401 here means
// the session lapsed — surfaced as an error so the sync re-logs in rather than
// caching an empty week.
func (c *Client) Timetable(ctx context.Context, pupilID int64, weekStart time.Time) ([]Event, error) {
	q := url.Values{
		"pupilId": {fmt.Sprint(pupilID)},
		"date":    {weekStart.Format(portalDateFormat)},
	}
	endpoint := c.baseURL + "/api/TimetableApi/GetTimetableByPupil?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("school-today: timetable fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("school-today: timetable for pupil %d week %s: HTTP %d",
			pupilID, weekStart.Format(portalDateFormat), resp.StatusCode)
	}

	var tt timetableResponse
	if err := json.NewDecoder(resp.Body).Decode(&tt); err != nil {
		return nil, fmt.Errorf("school-today: decode timetable: %w", err)
	}
	return tt.Events, nil
}

// ErrNotALesson is what LessonView returns for a slot the portal has no detail
// page for. The timetable carries meals, recess and after-school care as
// ordinary events, and asking for their detail is a 404 — an expected answer,
// not a failure, so the collector can skip them quietly instead of reporting a
// week full of errors.
var ErrNotALesson = errors.New("school-today: event has no lesson detail")

// ErrSessionExpired means the portal answered with its login form instead of
// the page asked for. It is worth its own error because the portal never says
// 401: the auth filter redirects, the client follows the redirect, and the
// login page comes back as a perfectly ordinary 200. Without this check an
// expired session reads as "the lesson had no topic" and the week's review
// quietly comes back empty.
var ErrSessionExpired = errors.New("school-today: session expired (portal served the login form)")

// LessonView returns the raw HTML of one lesson's detail page — the topic, the
// teacher's notes, the homework and the marks, none of which appear in the
// timetable JSON. lessonType is the event's own Type field.
//
// POST, with no body: the portal's own UI loads this into a modal that way,
// and a GET is refused with 405.
func (c *Client) LessonView(ctx context.Context, eventID int64, lessonType int) ([]byte, error) {
	q := url.Values{
		"lessonID":   {fmt.Sprint(eventID)},
		"lessonType": {fmt.Sprint(lessonType)},
	}
	endpoint := c.baseURL + "/Timetable/LessonView?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("school-today: lesson %d detail: %w", eventID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotALesson
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("school-today: lesson %d detail: HTTP %d", eventID, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("school-today: read lesson %d detail: %w", eventID, err)
	}
	if isLoginPage(body) {
		return nil, ErrSessionExpired
	}
	return body, nil
}

// isLoginPage reports whether a response body is the portal's login form
// rather than the page requested. The antiforgery token is the marker: it is
// rendered into the login form and appears nowhere in the authenticated pages
// this package reads.
func isLoginPage(body []byte) bool {
	return bytes.Contains(body, []byte("__RequestVerificationToken"))
}
