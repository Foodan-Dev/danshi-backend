// Package openapi 从 Hertz 运行时路由表和显式契约注册表生成 OpenAPI。
package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
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

// CodeCatalog 是从 apierr/codes.go 解析出的稳定错误码全集。
type CodeCatalog struct {
	FieldCodes []string
	BizCodes   []string
}

// Generate 生成、校验并以稳定格式编码 OpenAPI 3.0 文档。
func Generate(
	runtimeRoutes route.RoutesInfo,
	declarations []apicontract.Route,
	bindings []apicontract.TypedRoute,
	codes CodeCatalog,
) ([]byte, error) {
	if len(codes.FieldCodes) == 0 || len(codes.BizCodes) == 0 {
		return nil, fmt.Errorf("错误码目录必须同时包含 FieldCode 与 BizCode")
	}
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

	document := newDocument(codes)
	for _, runtimeRoute := range runtimeRoutes {
		key := apicontract.Key(runtimeRoute.Method, runtimeRoute.Path)
		declaration := indexed[key]
		path, parameters, err := HertzPath(runtimeRoute.Path)
		if err != nil {
			return nil, fmt.Errorf("转换路由 %s: %w", declaration.OperationKey(), err)
		}
		operation, err := typedOperation(
			declaration, typed[key], parameters, document.Components.Schemas, codes,
		)
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
	undeclaredQuery := make([]string, 0)
	for _, runtimeRoute := range runtimeRoutes {
		key := apicontract.Key(runtimeRoute.Method, runtimeRoute.Path)
		binding := typed[key]
		if runtimeRoute.Method == http.MethodGet && binding.Query == nil {
			undeclaredQuery = append(undeclaredQuery, key)
			continue
		}
		if binding.Query != nil {
			if _, err := apicontract.QueryFields(binding.Query); err != nil {
				return fmt.Errorf("OpenAPI query 参数门禁失败: %s: %w", key, err)
			}
		}
	}
	if len(undeclaredQuery) > 0 {
		slices.Sort(undeclaredQuery)
		return fmt.Errorf("OpenAPI query 参数门禁失败: 未声明 query 参数=%v", undeclaredQuery)
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

func newDocument(codes CodeCatalog) *openapi3.T {
	components := openapi3.NewComponents()
	components.Schemas = openapi3.Schemas{
		"Time":      {Value: openapi3.NewDateTimeSchema()},
		"Amount":    {Value: openapi3.NewStringSchema()},
		"Decimal":   {Value: openapi3.NewStringSchema()},
		"BizCode":   {Value: codeEnumSchema(codes.BizCodes)},
		"FieldCode": {Value: codeEnumSchema(codes.FieldCodes)},
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
	codes CodeCatalog,
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
	statuses, err := responseStatuses(declaration, binding, pathParameters)
	if err != nil {
		return nil, err
	}
	for _, status := range statuses {
		responseSchema, err := responseEnvelopeSchema(
			declaration.Path, status, successData, schemas, codes,
		)
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
	queryParameters, err := queryParametersForValue(binding.Query)
	if err != nil {
		return nil, fmt.Errorf("生成 query 参数: %w", err)
	}
	for _, parameter := range queryParameters {
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

func queryParametersForValue(query any) ([]*openapi3.Parameter, error) {
	if query == nil {
		return nil, nil
	}
	fields, err := apicontract.QueryFields(query)
	if err != nil {
		return nil, err
	}
	parameters := make([]*openapi3.Parameter, 0, len(fields))
	for _, field := range fields {
		schema, err := querySchema(field)
		if err != nil {
			return nil, fmt.Errorf("参数 %s: %w", field.Name, err)
		}
		parameter := openapi3.NewQueryParameter(field.Name).WithRequired(field.Required)
		parameter.Schema = &openapi3.SchemaRef{Value: schema}
		if rawExplode, exists := field.Tag.Lookup("query_explode"); exists {
			explode, err := strconv.ParseBool(rawExplode)
			if err != nil {
				return nil, fmt.Errorf("参数 %s 的 query_explode 必须是布尔值", field.Name)
			}
			parameter.Style = "form"
			parameter.Explode = &explode
		}
		parameters = append(parameters, parameter)
	}
	return parameters, nil
}

func querySchema(field apicontract.QueryField) (*openapi3.Schema, error) {
	valueType := field.Type
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	schema, schemaType, err := queryBaseSchema(valueType, field.Tag.Get("query_type"))
	if err != nil {
		return nil, err
	}

	if rawEnum := field.Tag.Get("query_enum"); rawEnum != "" {
		if schemaType != openapi3.TypeString {
			return nil, fmt.Errorf("query_enum 只支持字符串参数")
		}
		values := strings.Split(rawEnum, ",")
		enums := make([]any, 0, len(values))
		for _, value := range values {
			if value == "" {
				return nil, fmt.Errorf("query_enum 不能包含空值")
			}
			enums = append(enums, value)
		}
		schema.Enum = enums
	}
	if rawDefault := field.Tag.Get("query_default"); rawDefault != "" {
		value, err := queryLiteral(rawDefault, schemaType)
		if err != nil {
			return nil, fmt.Errorf("非法 query_default %q: %w", rawDefault, err)
		}
		schema.Default = value
	}
	if rawMinimum := field.Tag.Get("query_min"); rawMinimum != "" {
		minimum, err := strconv.ParseFloat(rawMinimum, 64)
		if err != nil || schemaType != openapi3.TypeInteger && schemaType != openapi3.TypeNumber {
			return nil, fmt.Errorf("非法 query_min %q", rawMinimum)
		}
		schema.Min = &minimum
	}
	if rawMaximum := field.Tag.Get("query_max"); rawMaximum != "" {
		maximum, err := strconv.ParseFloat(rawMaximum, 64)
		if err != nil || schemaType != openapi3.TypeInteger && schemaType != openapi3.TypeNumber {
			return nil, fmt.Errorf("非法 query_max %q", rawMaximum)
		}
		schema.Max = &maximum
	}
	if pattern := field.Tag.Get("query_pattern"); pattern != "" {
		if schemaType != openapi3.TypeString {
			return nil, fmt.Errorf("query_pattern 只支持字符串参数")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("非法 query_pattern %q: %w", pattern, err)
		}
		schema.Pattern = pattern
	}
	return schema, nil
}

func queryBaseSchema(valueType reflect.Type, override string) (*openapi3.Schema, string, error) {
	if valueType.Kind() != reflect.Slice && valueType.Kind() != reflect.Array {
		return queryScalarSchema(valueType, override)
	}
	if override != "" {
		return nil, "", fmt.Errorf("数组参数不能覆盖 query_type")
	}
	itemSchema, _, err := queryScalarSchema(valueType.Elem(), "")
	if err != nil {
		return nil, "", err
	}
	return openapi3.NewArraySchema().WithItems(itemSchema), openapi3.TypeArray, nil
}

func queryScalarSchema(valueType reflect.Type, override string) (*openapi3.Schema, string, error) {
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	schemaType := override
	if schemaType == "" {
		switch valueType.Kind() {
		case reflect.String:
			schemaType = openapi3.TypeString
		case reflect.Bool:
			schemaType = openapi3.TypeBoolean
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			schemaType = openapi3.TypeInteger
		case reflect.Float32, reflect.Float64:
			schemaType = openapi3.TypeNumber
		default:
			return nil, "", fmt.Errorf("不支持的 query 字段类型 %s", valueType)
		}
	}
	var schema *openapi3.Schema
	switch schemaType {
	case openapi3.TypeString:
		schema = openapi3.NewStringSchema()
	case openapi3.TypeBoolean:
		schema = openapi3.NewBoolSchema()
	case openapi3.TypeInteger:
		schema = openapi3.NewInt64Schema()
	case openapi3.TypeNumber:
		schema = openapi3.NewFloat64Schema()
	default:
		return nil, "", fmt.Errorf("不支持的 query_type %q", schemaType)
	}
	if values := enumValues(valueType); len(values) > 0 {
		if schemaType != openapi3.TypeString {
			return nil, "", fmt.Errorf("枚举类型 %s 必须生成字符串参数", valueType)
		}
		schema.Enum = values
	}
	return schema, schemaType, nil
}

func queryLiteral(raw, schemaType string) (any, error) {
	switch schemaType {
	case openapi3.TypeString:
		return raw, nil
	case openapi3.TypeBoolean:
		return strconv.ParseBool(raw)
	case openapi3.TypeInteger:
		return strconv.ParseInt(raw, 10, 64)
	case openapi3.TypeNumber:
		return strconv.ParseFloat(raw, 64)
	default:
		return nil, fmt.Errorf("%s 参数不支持默认值", schemaType)
	}
}

func responseStatuses(
	declaration apicontract.Route,
	binding apicontract.TypeBinding,
	pathParameters []string,
) ([]int, error) {
	inferred := map[int]struct{}{
		http.StatusOK:                  {},
		http.StatusInternalServerError: {},
	}
	if declaration.BearerAuth {
		inferred[http.StatusUnauthorized] = struct{}{}
	}
	if hasStandardRequestBody(binding.Request) || len(pathParameters) > 0 {
		inferred[http.StatusUnprocessableEntity] = struct{}{}
	}
	if len(pathParameters) > 0 {
		inferred[http.StatusNotFound] = struct{}{}
	}
	if strings.HasPrefix(declaration.Path, "/api/v2/admin/") {
		inferred[http.StatusForbidden] = struct{}{}
	}
	if declaration.Path == "/ready" {
		inferred[http.StatusServiceUnavailable] = struct{}{}
	}
	for _, status := range binding.AdditionalErrorStatuses {
		if status < http.StatusBadRequest || status > 599 || http.StatusText(status) == "" {
			return nil, fmt.Errorf("额外错误状态 %d 不是已知的 4xx/5xx 状态", status)
		}
		if _, exists := inferred[status]; exists {
			return nil, fmt.Errorf("额外错误状态 %d 已能由通用规则推导，不应重复声明", status)
		}
		inferred[status] = struct{}{}
	}
	statuses := make([]int, 0, len(inferred))
	for status := range inferred {
		statuses = append(statuses, status)
	}
	slices.Sort(statuses)
	return statuses, nil
}

func hasStandardRequestBody(request any) bool {
	return request != nil && reflect.TypeOf(request) != reflect.TypeFor[json.RawMessage]()
}

func responseEnvelopeSchema(
	path string,
	status int,
	successData *openapi3.SchemaRef,
	schemas openapi3.Schemas,
	codes CodeCatalog,
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
		ensureCodeSchemas(schemas, codes)
		schema.WithPropertyRef("error_code", &openapi3.SchemaRef{Ref: "#/components/schemas/BizCode"})
		required = append(required, "error_code")
	}
	return schema.WithRequired(required), nil
}

func ensureCodeSchemas(schemas openapi3.Schemas, codes CodeCatalog) {
	if schemas["BizCode"] == nil {
		schemas["BizCode"] = &openapi3.SchemaRef{Value: codeEnumSchema(codes.BizCodes)}
	}
	if schemas["FieldCode"] == nil {
		schemas["FieldCode"] = &openapi3.SchemaRef{Value: codeEnumSchema(codes.FieldCodes)}
	}
	if fieldError := schemas["FieldError"]; fieldError != nil && fieldError.Value != nil {
		fieldError.Value.Properties["code"] = &openapi3.SchemaRef{Ref: "#/components/schemas/FieldCode"}
	}
}

func codeEnumSchema[T ~string](codes []T) *openapi3.Schema {
	values := make([]any, 0, len(codes))
	for _, code := range codes {
		values = append(values, string(code))
	}
	return openapi3.NewStringSchema().WithEnum(values...)
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
		reflect.TypeFor[model.UserRole]():          {"dict_reviewer", "moderator", "super_admin"},
		reflect.TypeFor[model.UserRoleAction]():    {"grant", "revoke"},
		reflect.TypeFor[model.UserBanAction]():     {"ban", "unban"},
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
