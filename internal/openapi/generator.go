// Package openapi 从 Hertz 运行时路由表和显式契约注册表生成 OpenAPI。
package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/cloudwego/hertz/pkg/route"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3gen"
	"github.com/shopspring/decimal"

	"github.com/jingyijun/danshi_backend_go/internal/apicontract"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/money"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
)

const bearerScheme = "BearerAuth"

// Generate 生成、校验并以稳定格式编码 OpenAPI 3.0 文档。
func Generate(
	runtimeRoutes route.RoutesInfo,
	declarations []apicontract.Route,
	bindings []apicontract.TypedRoute,
) ([]byte, error) {
	if err := ValidateCoverage(runtimeRoutes, declarations, bindings); err != nil {
		return nil, err
	}
	indexed, err := apicontract.ByKey(declarations)
	if err != nil {
		return nil, err
	}
	typed, err := apicontract.BindingsByKey(bindings)
	if err != nil {
		return nil, err
	}

	document := newDocument()
	for _, runtimeRoute := range runtimeRoutes {
		key := apicontract.Key(runtimeRoute.Method, runtimeRoute.Path)
		declaration := indexed[key]
		path, parameters, err := HertzPath(runtimeRoute.Path)
		if err != nil {
			return nil, fmt.Errorf("转换路由 %s: %w", declaration.OperationKey(), err)
		}
		operation, err := typedOperation(declaration, typed[key], parameters, document.Components.Schemas)
		if err != nil {
			return nil, fmt.Errorf("生成路由 %s: %w", declaration.OperationKey(), err)
		}
		item := document.Paths.Value(path)
		if item == nil {
			item = &openapi3.PathItem{}
			document.Paths.Set(path, item)
		}
		item.SetOperation(strings.ToUpper(runtimeRoute.Method), operation)
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("编码 OpenAPI 文档: %w", err)
	}
	encoded = append(encoded, '\n')
	loaded, err := openapi3.NewLoader().LoadFromData(encoded)
	if err != nil {
		return nil, fmt.Errorf("重新加载生成的 OpenAPI 文档: %w", err)
	}
	if err := loaded.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("校验生成的 OpenAPI 文档: %w", err)
	}
	return encoded, nil
}

// ValidateCoverage 双向检查运行时路由与显式注册表，任一侧多出路由都失败。
func ValidateCoverage(
	runtimeRoutes route.RoutesInfo,
	declarations []apicontract.Route,
	bindings []apicontract.TypedRoute,
) error {
	registered, err := apicontract.ByKey(declarations)
	if err != nil {
		return err
	}
	actual := make(map[string]struct{}, len(runtimeRoutes))
	for _, runtimeRoute := range runtimeRoutes {
		key := apicontract.Key(runtimeRoute.Method, runtimeRoute.Path)
		if _, exists := actual[key]; exists {
			return fmt.Errorf("hertz 路由重复注册: %s", key)
		}
		actual[key] = struct{}{}
	}

	missing := setDifference(actual, registered)
	extra := setDifference(registered, actual)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("OpenAPI 覆盖门禁失败: 未登记路由=%v, 无运行时路由的登记=%v", missing, extra)
	}
	typed, err := apicontract.BindingsByKey(bindings)
	if err != nil {
		return err
	}
	missing = setDifference(actual, typed)
	extra = setDifference(typed, actual)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("OpenAPI 类型覆盖门禁失败: 未绑定类型=%v, 无运行时路由的绑定=%v", missing, extra)
	}
	return nil
}

// HertzPath 把 Hertz 的 :name 路径参数转成 OpenAPI 的 {name}，并返回参数名。
func HertzPath(hertzPath string) (string, []string, error) {
	segments := strings.Split(hertzPath, "/")
	parameters := make([]string, 0, 2)
	for index, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			name := strings.TrimPrefix(segment, ":")
			if name == "" || !validParameterName(name) {
				return "", nil, fmt.Errorf("非法路径参数 %q", segment)
			}
			segments[index] = "{" + name + "}"
			parameters = append(parameters, name)
			continue
		}
		if strings.HasPrefix(segment, "*") {
			return "", nil, fmt.Errorf("暂不支持 Hertz 通配路径 %q", segment)
		}
	}
	return strings.Join(segments, "/"), parameters, nil
}

func newDocument() *openapi3.T {
	components := openapi3.NewComponents()
	components.Schemas = openapi3.Schemas{
		"Time":    {Value: openapi3.NewDateTimeSchema()},
		"Amount":  {Value: openapi3.NewStringSchema()},
		"Decimal": {Value: openapi3.NewStringSchema()},
	}
	components.SecuritySchemes = openapi3.SecuritySchemes{
		bearerScheme: {Value: openapi3.NewJWTSecurityScheme()},
	}
	return &openapi3.T{
		OpenAPI: "3.0.3",
		Info: &openapi3.Info{
			Title:       "旦食 API",
			Description: "由 Go 运行时路由表与类型化注册表生成；请勿手工编辑 api/openapi.json。",
			Version:     "2.0.0",
		},
		Components: &components,
		Paths:      openapi3.NewPaths(),
	}
}

func typedOperation(
	declaration apicontract.Route,
	binding apicontract.TypeBinding,
	pathParameters []string,
	schemas openapi3.Schemas,
) (*openapi3.Operation, error) {
	operation := openapi3.NewOperation()
	operation.OperationID = operationID(declaration.Method, declaration.Path)
	operation.Summary = declaration.Method + " " + declaration.Path
	operation.Tags = []string{operationTag(declaration.Path)}
	operation.Responses = openapi3.NewResponses()
	successData, err := schemaForValue(binding.Response, schemas)
	if err != nil {
		return nil, fmt.Errorf("生成成功响应 schema: %w", err)
	}
	for _, status := range declaration.ResponseStatuses() {
		responseSchema, err := responseEnvelopeSchema(declaration.Path, status, successData, schemas)
		if err != nil {
			return nil, err
		}
		operation.AddResponse(status, openapi3.NewResponse().
			WithDescription(http.StatusText(status)).
			WithJSONSchemaRef(&openapi3.SchemaRef{Value: responseSchema}))
	}
	for _, name := range pathParameters {
		parameter := openapi3.NewPathParameter(name)
		parameter.Schema = &openapi3.SchemaRef{Value: openapi3.NewInt64Schema().WithMin(1)}
		operation.AddParameter(parameter)
	}
	if binding.Request != nil {
		requestSchema, err := schemaForValue(binding.Request, schemas)
		if err != nil {
			return nil, fmt.Errorf("生成请求 schema: %w", err)
		}
		operation.RequestBody = &openapi3.RequestBodyRef{Value: openapi3.NewRequestBody().
			WithRequired(true).
			WithJSONSchemaRef(requestSchema)}
	}
	if declaration.BearerAuth {
		requirements := openapi3.NewSecurityRequirements().With(
			openapi3.NewSecurityRequirement().Authenticate(bearerScheme),
		)
		operation.Security = requirements
	}
	return operation, nil
}

func responseEnvelopeSchema(
	path string,
	status int,
	successData *openapi3.SchemaRef,
	schemas openapi3.Schemas,
) (*openapi3.Schema, error) {
	data := successData
	isError := status >= http.StatusBadRequest
	if path == "/ready" && status == http.StatusServiceUnavailable {
		isError = false
	}
	if isError {
		var err error
		switch status {
		case http.StatusUnprocessableEntity:
			data, err = schemaForValue(envelope.ErrorData{}, schemas)
		case http.StatusInternalServerError:
			data, err = schemaForValue(envelope.ErrorIDData{}, schemas)
		default:
			data = nullableDataSchema()
		}
		if err != nil {
			return nil, fmt.Errorf("生成 %d 错误 data schema: %w", status, err)
		}
	}
	if data == nil {
		data = nullableDataSchema()
	}
	schema := openapi3.NewObjectSchema().
		WithProperty("code", openapi3.NewIntegerSchema()).
		WithProperty("message", openapi3.NewStringSchema()).
		WithPropertyRef("data", data)
	required := []string{"code", "message", "data"}
	if isError {
		schema.WithProperty("error_code", openapi3.NewStringSchema())
		required = append(required, "error_code")
	}
	return schema.WithRequired(required), nil
}

func nullableDataSchema() *openapi3.SchemaRef {
	return &openapi3.SchemaRef{Value: openapi3.NewObjectSchema().WithNullable()}
}

func schemaForValue(value any, schemas openapi3.Schemas) (*openapi3.SchemaRef, error) {
	if value == nil {
		return nil, nil
	}
	return openapi3gen.NewSchemaRefForValue(
		value,
		schemas,
		openapi3gen.CreateComponentSchemas(openapi3gen.ExportComponentSchemasOptions{
			ExportComponentSchemas: true,
			ExportTopLevelSchema:   true,
			ExportGenerics:         true,
		}),
		openapi3gen.SchemaCustomizer(customizeSchema),
	)
}

func customizeSchema(name string, valueType reflect.Type, _ reflect.StructTag, schema *openapi3.Schema) error {
	nullable := schema.Nullable
	switch valueType {
	case reflect.TypeFor[ptime.Time]():
		*schema = *openapi3.NewDateTimeSchema()
	case reflect.TypeFor[money.Amount](), reflect.TypeFor[decimal.Decimal]():
		*schema = *openapi3.NewStringSchema()
	case reflect.TypeFor[json.RawMessage]():
		*schema = openapi3.Schema{}
		if name == "price" {
			*schema = *openapi3.NewStringSchema()
			schema.Pattern = `^(?:0|[0-9]+)(?:\.[0-9]{1,2})?$`
			nullable = true
		}
	default:
		if values := enumValues(valueType); len(values) > 0 {
			schema.Type = &openapi3.Types{openapi3.TypeString}
			schema.Enum = values
		}
	}
	schema.Nullable = nullable
	return nil
}

func enumValues(valueType reflect.Type) []any {
	values := map[reflect.Type][]any{
		reflect.TypeFor[model.UserRole]():          {"user", "admin", "super_admin"},
		reflect.TypeFor[model.Gender]():            {"male", "female", "other"},
		reflect.TypeFor[model.ModerationStatus]():  {"pending", "pass", "review", "block"},
		reflect.TypeFor[model.ImagePurpose]():      {"post", "avatar"},
		reflect.TypeFor[model.ImageStatus]():       {"pending", "ready", "retired"},
		reflect.TypeFor[model.PostType]():          {"share", "seeking"},
		reflect.TypeFor[model.ShareType]():         {"recommend", "warning"},
		reflect.TypeFor[model.PostStatus]():        {"draft", "pending", "approved", "rejected"},
		reflect.TypeFor[model.PostCategory]():      {"food", "recipe"},
		reflect.TypeFor[model.DeleteReason]():      {"author", "admin", "moderation"},
		reflect.TypeFor[model.FlavorStance]():      {"has", "prefer", "avoid"},
		reflect.TypeFor[model.NotificationType]():  {"like_post", "like_comment", "comment", "reply", "mention", "follow"},
		reflect.TypeFor[model.ModerationScene]():   {"text", "image"},
		reflect.TypeFor[model.ModerationVerdict](): {"pass", "review", "block"},
		reflect.TypeFor[model.ModerationField]():   {"name", "bio", "title", "content"},
		reflect.TypeFor[model.SuggestionKind]():    {"flavor", "cuisine", "canteen", "canteen_window"},
		reflect.TypeFor[model.SuggestionStatus]():  {"pending", "approved", "rejected"},
	}
	return values[valueType]
}

func setDifference[L, R any](left map[string]L, right map[string]R) []string {
	difference := make([]string, 0)
	for key := range left {
		if _, exists := right[key]; !exists {
			difference = append(difference, key)
		}
	}
	slices.Sort(difference)
	return difference
}

func validParameterName(name string) bool {
	for index, r := range name {
		if unicode.IsLetter(r) || r == '_' || index > 0 && unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}

func operationID(method, path string) string {
	value := strings.ToLower(method) + "_" + strings.Trim(path, "/")
	var builder strings.Builder
	underscore := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			underscore = false
			continue
		}
		if !underscore {
			builder.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
}

func operationTag(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) >= 3 && segments[0] == "api" && segments[1] == "v2" {
		return segments[2]
	}
	return "runtime"
}
