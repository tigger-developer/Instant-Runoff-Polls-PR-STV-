// ABOUTME: Verifies bounded and allowlisted workflow form decoding.
// ABOUTME: It rejects ambiguous scalar values, unknown fields, and wrong media types.
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDecodeFormAcceptsDeclaredScalarAndRepeatedFields(t *testing.T) {
	values := url.Values{"csrf": {"token"}, "version": {"2"}, "rank": {"a", "b"}}
	request := httptest.NewRequest(http.MethodPost, "/poll", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	got, problem := DecodeForm(httptest.NewRecorder(), request, FormSchema{Scalars: []string{"csrf", "version"}, Repeated: []string{"rank"}}, 1024)
	if problem != nil {
		t.Fatal(problem)
	}
	if got.Get("csrf") != "token" || len(got["rank"]) != 2 {
		t.Fatalf("decoded form = %#v", got)
	}
}

func TestDecodeFormRejectsWrongTypeUnknownDuplicateAndOversize(t *testing.T) {
	cases := []struct {
		name, contentType, body string
		max                     int64
		want                    int
	}{
		{name: "media", contentType: "application/json", body: `{}`, max: 20, want: http.StatusUnsupportedMediaType},
		{name: "unknown", contentType: "application/x-www-form-urlencoded", body: "other=x", max: 20, want: http.StatusUnprocessableEntity},
		{name: "duplicate scalar", contentType: "application/x-www-form-urlencoded", body: "csrf=a&csrf=b", max: 30, want: http.StatusUnprocessableEntity},
		{name: "oversize", contentType: "application/x-www-form-urlencoded", body: "csrf=123456789", max: 5, want: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/poll", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)
			_, problem := DecodeForm(httptest.NewRecorder(), request, FormSchema{Scalars: []string{"csrf"}}, tc.max)
			if problem == nil || problem.Status != tc.want {
				t.Fatalf("problem = %#v, want status %d", problem, tc.want)
			}
		})
	}
}
