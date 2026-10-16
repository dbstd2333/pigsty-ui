package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/schema"
	schemafiles "github.com/dbstd2333/pigsty-ui/console/schema"
)

type schemaInput struct{}

type schemaOutput struct {
	Body json.RawMessage
}

// registerSchema registers the parameter schema endpoint.
func (s *Server) registerSchema() {
	huma.Register(s.api, huma.Operation{
		OperationID: "getSchema",
		Method:      http.MethodGet,
		Path:        "/schema",
		Summary:     "参数 Schema",
		Tags:        []string{"schema"},
		Errors:      []int{401, 500},
	}, func(_ context.Context, _ *schemaInput) (*schemaOutput, error) {
		data, err := s.schemaJSON()
		if err != nil {
			return nil, err
		}
		return &schemaOutput{Body: data}, nil
	})
}

// schemaJSON returns the raw schema document of the running Pigsty version.
func (s *Server) schemaJSON() ([]byte, error) {
	return schema.Read(schemafiles.Files, s.store.PigstyVersion())
}

// schemaSet returns the parsed schema of the running Pigsty version, cached
// until the configuration reports another version.
func (s *Server) schemaSet() (*schema.Set, error) {
	version := s.store.PigstyVersion()

	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schema != nil && s.schemaVersion == version {
		return s.schema, nil
	}
	set, err := schema.Load(schemafiles.Files, version)
	if err != nil {
		return nil, fmt.Errorf("load parameter schema: %w", err)
	}
	s.schema, s.schemaVersion = set, version
	return set, nil
}
