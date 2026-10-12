package api

import "net/http"

// ProblemFromBody builds a Problem from a non-2xx answer seen outside the
// client (the SQL endpoint's proxy answers with problem+json too).
func ProblemFromBody(status int, header http.Header, body []byte) *Problem {
	if header == nil {
		header = http.Header{}
	}
	return parseProblem(&http.Response{StatusCode: status, Header: header}, body)
}
