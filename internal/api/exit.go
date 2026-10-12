package api

import "fmt"

// Exit codes are part of the CLI contract: scripts and agents branch on them.
const (
	ExitOK          = 0
	ExitFailure     = 1   // unexpected / internal
	ExitUsage       = 2   // wrong flags, missing arguments, refused prompt
	ExitAuth        = 3   // no or invalid identity
	ExitNotFound    = 4   // the named resource does not exist
	ExitPermission  = 5   // identity known, action not allowed
	ExitInvalid     = 6   // the request itself is wrong (validation)
	ExitConflict    = 7   // state does not allow it
	ExitFailed      = 8   // a query, flow or job failed
	ExitRetryable   = 9   // transient; the same request may succeed later
	ExitInterrupted = 130 // Ctrl-C
)

// ExitError carries an exit code with a message for the user.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Exit wraps err (or a formatted message) with an exit code.
func Exit(code int, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

// exitByCode maps the gateway's stable problem codes to exit codes.
var exitByCode = map[string]int{
	"AUTHENTICATION_REQUIRED": ExitAuth,
	"API_KEY_INVALID":         ExitAuth,
	"SESSION_EXPIRED":         ExitAuth,
	"TWO_FACTOR_REQUIRED":     ExitAuth,
	"IMPERSONATION_ENDED":     ExitAuth,
	"INVALID_CREDENTIALS":     ExitAuth,
	"ACCOUNT_NOT_VERIFIED":    ExitAuth,

	"NOT_FOUND":       ExitNotFound,
	"ROUTE_NOT_FOUND": ExitNotFound,

	"PROJECT_ACCESS_DENIED": ExitPermission,
	"PERMISSION_REQUIRED":   ExitPermission,
	"OUTSIDE_KEY_SCOPE":     ExitPermission,
	"LICENSE_REQUIRED":      ExitPermission,

	"METHOD_NOT_ALLOWED":      ExitInvalid,
	"NOT_ACCEPTABLE":          ExitInvalid,
	"VALIDATION_FAILED":       ExitInvalid,
	"MALFORMED_REQUEST":       ExitInvalid,
	"UNSUPPORTED_MEDIA_TYPE":  ExitInvalid,
	"REQUEST_FAILED":          ExitInvalid,
	"UNKNOWN_CONNECTION_TYPE": ExitInvalid,
	"MISSING_CONNECTION_TYPE": ExitInvalid,
	"STATEMENT_NOT_SAVABLE":   ExitInvalid,
	"STATEMENT_NEEDS_SESSION": ExitInvalid,
	"SEMANTIC_REFUSAL":        ExitInvalid,
	"TABLE_NOT_CACHEABLE":     ExitInvalid,

	"CONFLICT":                     ExitConflict,
	"EMAIL_ALREADY_REGISTERED":     ExitConflict,
	"SEMANTIC_TABLE_CONFLICT":      ExitConflict,
	"MATERIALIZED_QUERY_NOT_READY": ExitConflict,

	"QUERY_FAILED":        ExitFailed,
	"QUERY_TIMEOUT":       ExitFailed,
	"FLOW_FAILED":         ExitFailed,
	"FLOW_STEP_FAILED":    ExitFailed,
	"CACHE_FAILED":        ExitFailed,
	"CONNECTOR_FAILED":    ExitFailed,
	"AI_NO_SQL_GENERATED": ExitFailed,
	"AI_RETRY_LIMIT":      ExitFailed,

	"PLAN_LIMIT_REACHED":   ExitRetryable,
	"RATE_LIMITED":         ExitRetryable,
	"UPSTREAM_UNAVAILABLE": ExitRetryable,

	"INTERNAL_ERROR": ExitFailure,
}

// hintByCode is the one line the CLI adds under a problem; the detail itself is the API's.
var hintByCode = map[string]string{
	"AUTHENTICATION_REQUIRED": "run `peaka auth login`, or set PEAKA_API_KEY",
	"API_KEY_INVALID":         "the key is unknown, expired or deactivated: `peaka auth list`, then `peaka auth login`",
	"SESSION_EXPIRED":         "run `peaka auth login` again",
	"OUTSIDE_KEY_SCOPE":       "this key is bound to another project: `peaka me` shows its scope",
	"PROJECT_ACCESS_DENIED":   "`peaka projects list` shows the projects this identity reaches",
	"PERMISSION_REQUIRED":     "ask an organization admin for a role that includes this permission",
	"VALIDATION_FAILED":       "see the field errors above",
	"NOT_FOUND":               "check the id or name; `--project` may point at another project",
	"RATE_LIMITED":            "wait and retry; `retryAfter` says how long",
}
