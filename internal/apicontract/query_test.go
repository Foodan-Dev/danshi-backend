package apicontract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQueryFieldsFlattensGroupsAndPreservesOrder(t *testing.T) {
	type pagination struct {
		Page  string `query:"page"`
		Limit string `query:"limit"`
	}
	type query struct {
		Search string `query:"q,required"`
		Page   pagination
	}

	fields, err := QueryFields(query{})
	require.NoError(t, err)
	require.Len(t, fields, 3)
	require.Equal(t, []string{"q", "page", "limit"}, []string{
		fields[0].Name, fields[1].Name, fields[2].Name,
	})
	require.True(t, fields[0].Required)
	require.Equal(t, []int{1, 0}, fields[1].Index)
}

func TestQueryFieldsRejectsSilentOrDuplicateDeclarations(t *testing.T) {
	_, err := QueryFields(struct {
		Forgotten string
	}{})
	require.ErrorContains(t, err, "缺少 query tag")

	_, err = QueryFields(struct {
		First  string `query:"q"`
		Second string `query:"q"`
	}{})
	require.ErrorContains(t, err, "重复声明")
}

func TestNoQueryIsAnExplicitEmptyDeclaration(t *testing.T) {
	fields, err := QueryFields(NoQuery{})
	require.NoError(t, err)
	require.Empty(t, fields)
}
