package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
)

func TestQueryBindingPreservesRawParsingSemantics(t *testing.T) {
	c := app.NewContext(0)
	c.Request.SetRequestURI("/?q=&flavors=辣,甜&tags=&page=2&limit=100")

	query, err := bindQuery[searchPostsQuery](c)
	require.NoError(t, err)
	require.NoError(t, validateRequiredQuery(c, query.Search))
	require.Empty(t, query.Search.Query)
	require.Equal(t, []string{"辣", "甜"}, query.Filters.Flavors)
	require.Nil(t, query.Filters.Tags)
	params, err := query.Pagination.params()
	require.NoError(t, err)
	require.Equal(t, 2, params.Page)
	require.Equal(t, 100, params.Limit)
}

func TestRequiredQueryStillDistinguishesMissingFromEmpty(t *testing.T) {
	c := app.NewContext(0)
	c.Request.SetRequestURI("/")
	query, err := bindQuery[searchUsersQuery](c)
	require.NoError(t, err)
	err = validateRequiredQuery(c, query.Search)
	require.Error(t, err)
	var validation *apierr.Error
	require.ErrorAs(t, err, &validation)
	require.Equal(t, "q", validation.Fields[0].Field)
	require.Equal(t, apierr.FieldRequired, validation.Fields[0].Code)

	c.Request.SetRequestURI("/?q=")
	query, err = bindQuery[searchUsersQuery](c)
	require.NoError(t, err)
	require.NoError(t, validateRequiredQuery(c, query.Search))
}

func TestHandlersCannotBypassTypedQueryBinder(t *testing.T) {
	fileSet := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			name == "query.go" {
			continue
		}
		file, parseErr := parser.ParseFile(fileSet, filepath.Clean(name), nil, 0)
		require.NoError(t, parseErr)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && (selector.Sel.Name == "Query" || selector.Sel.Name == "QueryArgs") {
				t.Errorf("%s: handler 必须通过类型化 bindQuery 读取 query，不得直接调用 %s",
					fileSet.Position(call.Pos()), selector.Sel.Name)
			}
			return true
		})
	}
}
