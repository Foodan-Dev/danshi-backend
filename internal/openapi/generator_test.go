package openapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/route"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"

	"github.com/jingyijun/danshi_backend_go/internal/apicontract"
)

func TestOpenAPIPathConvertsAndDeclaresParameters(t *testing.T) {
	path, parameters, err := HertzPath("/api/v2/posts/:post_id/comments/:comment_id")
	require.NoError(t, err)
	require.Equal(t, "/api/v2/posts/{post_id}/comments/{comment_id}", path)
	require.Equal(t, []string{"post_id", "comment_id"}, parameters)
}

func TestMinimalEnvelopeSchemaShape(t *testing.T) {
	schemas := openapi3.Schemas{}
	schema, err := responseEnvelopeSchema("/example", http.StatusUnprocessableEntity, nil, schemas)
	require.NoError(t, err)
	require.Equal(t, []string{"code", "message", "data", "error_code"}, schema.Required)
	require.Contains(t, schema.Properties, "error_code")
	require.Contains(t, schemas, "ErrorData")

	schema, err = responseEnvelopeSchema("/example", http.StatusInternalServerError, nil, schemas)
	require.NoError(t, err)
	require.NotNil(t, schema)
	require.Contains(t, schemas, "ErrorIDData")
}

func TestCoverageGateFailsWhenRuntimeRouteIsMissingFromRegistry(t *testing.T) {
	runtimeRoutes := route.RoutesInfo{{Method: http.MethodGet, Path: "/registered"}, {
		Method: http.MethodPost, Path: "/forgotten",
	}}
	declarations := []apicontract.Route{{
		Method: http.MethodGet, Path: "/registered", ExpectedStatus: http.StatusOK,
	}}
	bindings := []apicontract.TypedRoute{{
		Method: http.MethodGet, Path: "/registered",
	}}
	err := ValidateCoverage(runtimeRoutes, declarations, bindings)
	require.ErrorContains(t, err, "POST /forgotten")
}

func TestCoverageGateFailsWhenRouteHasNoTypeBinding(t *testing.T) {
	runtimeRoutes := route.RoutesInfo{{Method: http.MethodGet, Path: "/registered"}}
	declarations := []apicontract.Route{{
		Method: http.MethodGet, Path: "/registered", ExpectedStatus: http.StatusOK,
	}}
	err := ValidateCoverage(runtimeRoutes, declarations, nil)
	require.ErrorContains(t, err, "GET /registered")
}

func TestGenerateProducesValidImportableMinimum(t *testing.T) {
	runtimeRoutes := route.RoutesInfo{{Method: http.MethodGet, Path: "/things/:thing_id"}}
	declarations := []apicontract.Route{{
		Method: http.MethodGet, Path: "/things/:thing_id", BearerAuth: true,
		ExpectedStatus: http.StatusUnauthorized,
	}}
	bindings := []apicontract.TypedRoute{{
		Method: http.MethodGet, Path: "/things/:thing_id",
		TypeBinding: apicontract.TypeBinding{Response: struct {
			ID uint64 `json:"id"`
		}{}},
	}}
	encoded, err := Generate(runtimeRoutes, declarations, bindings)
	require.NoError(t, err)

	document, err := openapi3.NewLoader().LoadFromData(encoded)
	require.NoError(t, err)
	require.NoError(t, document.Validate(context.Background()))
	operation := document.Paths.Value("/things/{thing_id}").Get
	require.NotNil(t, operation)
	require.NotNil(t, operation.Security)
	require.Equal(t, "thing_id", operation.Parameters[0].Value.Name)
	require.Equal(t, "integer", operation.Parameters[0].Value.Schema.Value.Type.Slice()[0])
}
