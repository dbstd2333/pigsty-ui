// Package api exposes the Pigsty Console HTTP API on top of huma v2.
package api

import (
	"fmt"

	"github.com/dbstd2333/pigsty-ui/console/internal/configstore"
	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/validate"
)

// Issue mirrors the API contract (see console/API.md).
type Issue = validate.Issue

// CommitInfo mirrors the API contract.
type CommitInfo = configstore.Commit

// ProblemError is an RFC 9457 problem+json error carrying the extension fields
// required by the API contract (409 conflict text, 422 issues).
type ProblemError struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`

	// CurrentText and CurrentHash are set on 409 conflict responses.
	CurrentText string `json:"currentText,omitempty"`
	CurrentHash string `json:"currentHash,omitempty"`

	// Issues is set on 422 validation failure responses.
	Issues []validate.Issue `json:"issues,omitempty"`

	// RunningJobID is set on 409 responses when another job holds the lock.
	RunningJobID string `json:"runningJobId,omitempty"`
}

// Error implements the error interface.
func (e *ProblemError) Error() string { return e.Detail }

// GetStatus implements huma.StatusError.
func (e *ProblemError) GetStatus() int { return e.Status }

// ContentType switches error responses to application/problem+json.
func (e *ProblemError) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	return ct
}

// problem builds a problem+json error for a status and message.
func problem(status int, detail string) *ProblemError {
	return &ProblemError{Title: titleFor(status), Status: status, Detail: detail}
}

// conflictProblem builds the 409 conflict response of a config save.
func conflictProblem(err *configstore.ConflictError) *ProblemError {
	return &ProblemError{
		Title:       titleFor(409),
		Status:      409,
		Detail:      "配置文件已被其他会话或手工修改，请基于最新版本重新应用改动",
		CurrentText: err.CurrentText,
		CurrentHash: err.CurrentHash,
	}
}

// validationProblem builds the 422 response of a failed save.
func validationProblem(err *configstore.ValidationError) *ProblemError {
	return &ProblemError{
		Title:  titleFor(422),
		Status: 422,
		Detail: fmt.Sprintf("配置校验失败，共 %d 个问题", len(err.Issues)),
		Issues: err.Issues,
	}
}

// titleFor returns the RFC 9457 title of a status code.
func titleFor(status int) string {
	switch status {
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 409:
		return "Conflict"
	case 422:
		return "Unprocessable Entity"
	case 500:
		return "Internal Server Error"
	default:
		return "Error"
	}
}

// Error responses shared by several operations.
var (
	errUnauthorized = problem(401, "请先登录")
	errForbidden    = problem(403, "请求来源校验失败")
)

// healthBody is the response of GET /health.
type healthBody struct {
	Status  string `json:"status" example:"ok"`
	Version string `json:"version"`
}

// metaBody is the response of GET /meta.
type metaBody struct {
	Version           string `json:"version"`
	PigstyVersion     string `json:"pigstyVersion"`
	PigstyHome        string `json:"pigstyHome"`
	InventoryEngine   string `json:"inventoryEngine" enum:"ansible,builtin"`
	ValidateAvailable bool   `json:"validateAvailable"`
	Simulate          bool   `json:"simulate"`
}

// userNameBody is used by the auth endpoints returning a username.
type userNameBody struct {
	Username string `json:"username"`
}

// configBody is the response of GET /config.
type configBody struct {
	Text   string      `json:"text"`
	Hash   string      `json:"hash" doc:"sha256 hex of text"`
	Commit *CommitInfo `json:"commit"`
}

// saveBody is the response of a successful save.
type saveBody struct {
	Hash   string     `json:"hash"`
	Commit CommitInfo `json:"commit"`
}

// validateBody is the response of POST /config/validate.
type validateBody struct {
	OK     bool    `json:"ok"`
	Issues []Issue `json:"issues"`
}

// historyBody is the response of GET /config/history.
type historyBody struct {
	Items []CommitInfo `json:"items"`
	Total int          `json:"total"`
}

// historyDetailBody is the response of GET /config/history/{hash}.
type historyDetailBody struct {
	Commit CommitInfo `json:"commit"`
	Text   string     `json:"text"`
}

// diffBody is the response of GET /config/diff.
type diffBody struct {
	From     string `json:"from"`
	To       string `json:"to"`
	FromText string `json:"fromText"`
	ToText   string `json:"toText"`
	Unified  string `json:"unified"`
}

// hostsBody is the response of GET /inventory/hosts.
type hostsBody struct {
	Hosts  []inventory.HostInfo  `json:"hosts"`
	Groups []inventory.GroupInfo `json:"groups"`
}

// effectiveBody is the response of GET /inventory/effective.
type effectiveBody struct {
	Host   string          `json:"host"`
	Engine string          `json:"engine" enum:"ansible,builtin"`
	Vars   []inventory.Var `json:"vars"`
}

// errStatuses lists the error responses documented in OpenAPI.
var errStatuses = []int{400, 401, 403, 404, 409, 422, 500}
