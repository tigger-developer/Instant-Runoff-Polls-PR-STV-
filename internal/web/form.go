// ABOUTME: Decodes bounded allowlisted workflow forms at the HTTP boundary.
// ABOUTME: It rejects ambiguous scalar fields before application mutation.
package web

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
)

type FormSchema struct {
	Scalars  []string
	Repeated []string
}

type FormProblem struct {
	Status int
	Err    error
}

func (problem *FormProblem) Error() string { return problem.Err.Error() }

func DecodeForm(response http.ResponseWriter, request *http.Request, schema FormSchema, maximumBytes int64) (url.Values, *FormProblem) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return nil, &FormProblem{Status: http.StatusUnsupportedMediaType, Err: errors.New("form content type is required")}
	}
	if maximumBytes <= 0 || request.ContentLength > maximumBytes {
		return nil, &FormProblem{Status: http.StatusRequestEntityTooLarge, Err: errors.New("form body exceeds limit")}
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &FormProblem{Status: http.StatusRequestEntityTooLarge, Err: errors.New("form body exceeds limit")}
		}
		return nil, &FormProblem{Status: http.StatusUnprocessableEntity, Err: errors.New("form body is invalid")}
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, &FormProblem{Status: http.StatusUnprocessableEntity, Err: errors.New("form body is invalid")}
	}
	scalars := make(map[string]struct{}, len(schema.Scalars))
	repeated := make(map[string]struct{}, len(schema.Repeated))
	for _, name := range schema.Scalars {
		scalars[name] = struct{}{}
	}
	for _, name := range schema.Repeated {
		repeated[name] = struct{}{}
	}
	for name, entries := range values {
		if _, allowed := repeated[name]; allowed {
			continue
		}
		if _, allowed := scalars[name]; !allowed || len(entries) != 1 {
			return nil, &FormProblem{Status: http.StatusUnprocessableEntity, Err: errors.New("form contains an unknown or duplicated field")}
		}
	}
	return values, nil
}
