package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

func TestDeriveImageTiersPreservesOrderAndKeys(t *testing.T) {
	t.Parallel()

	images := []string{
		"https://img.example.test/posts/first.jpg",
		"https://img.example.test/posts/second.png?version=7#preview",
	}
	displays, thumbs := deriveImageTiers(images)

	require.Equal(t, []string{
		"https://img.example.test/posts/first.jpg?" + imageDisplayProcessingQuery,
		"https://img.example.test/posts/second.png?version=7&" + imageDisplayProcessingQuery + "#preview",
	}, displays)
	require.Equal(t, []string{
		"https://img.example.test/posts/first.jpg?" + imageThumbProcessingQuery,
		"https://img.example.test/posts/second.png?version=7&" + imageThumbProcessingQuery + "#preview",
	}, thumbs)
	require.Len(t, displays, len(images))
	require.Len(t, thumbs, len(images))
}

func TestDeriveImageTiersLeavesNonPublicValuesUntouched(t *testing.T) {
	t.Parallel()

	purged := model.PurgedImageURL(42)
	signed := "https://bucket.cos.example.test/private.jpg?" +
		"q-sign-algorithm=sha1&q-ak=test&q-signature=deadbeef"
	// 本地存储签发的形状与 COS 不同：memory:// scheme + signature 参数。
	// 判据必须与供应商无关，只认「裸的 http(s) 地址」，否则守卫只防住一家。
	localSigned := "memory://signed/posts%2F7%2Fone.jpg?expires=1750000000&signature=3"
	values := []string{purged, signed, localSigned, "", "https://img.example.test/bad%url"}

	displays, thumbs := deriveImageTiers(values)

	require.Equal(t, values, displays, "墓碑、任意供应商的签名、空值和非法 URL 均不得追加处理参数")
	require.Equal(t, values, thumbs, "墓碑、任意供应商的签名、空值和非法 URL 均不得追加处理参数")
}

func TestBuildPostViewsExposeImageTiersAndAvatarThumb(t *testing.T) {
	t.Parallel()

	avatar := "https://img.example.test/avatars/author.jpg"
	record := repository.PostRecord{
		Post:       model.Post{ID: 7, AuthorID: 42},
		AuthorName: "图片作者",
		AvatarURL:  &avatar,
	}
	images := []string{
		"https://img.example.test/posts/7/one.jpg",
		"https://img.example.test/posts/7/two.jpg",
		"https://img.example.test/posts/7/three.jpg",
	}
	relations := repository.PostRelations{Images: map[uint64][]string{record.ID: images}}
	expectedDisplays, expectedThumbs := deriveImageTiers(images)

	post := buildPostListItem(&record, relations, 1)
	require.Equal(t, images, post.Images)
	require.Equal(t, expectedDisplays, post.ImageDisplays)
	require.Equal(t, expectedThumbs, post.ImageThumbs)
	require.NotNil(t, post.Author.AvatarThumbURL)
	require.Equal(t, avatar+"?"+imageThumbProcessingQuery, *post.Author.AvatarThumbURL)

	search := searchPostItem(&record, relations, "")
	require.Equal(t, images, search.Images)
	require.Equal(t, expectedDisplays, search.ImageDisplays)
	require.Equal(t, expectedThumbs, search.ImageThumbs)
	require.NotNil(t, search.Author.AvatarThumbURL)
	require.Equal(t, *post.Author.AvatarThumbURL, *search.Author.AvatarThumbURL)
}

func TestBuildPostViewsUseEmptyArraysAndNilAvatarThumb(t *testing.T) {
	t.Parallel()

	record := repository.PostRecord{
		Post:       model.Post{ID: 8, AuthorID: 43},
		AuthorName: "无图作者",
	}
	post := buildPostListItem(&record, repository.PostRelations{}, 1)

	require.NotNil(t, post.Images)
	require.NotNil(t, post.ImageDisplays)
	require.NotNil(t, post.ImageThumbs)
	require.Empty(t, post.Images)
	require.Empty(t, post.ImageDisplays)
	require.Empty(t, post.ImageThumbs)
	require.Nil(t, post.Author.AvatarURL)
	require.Nil(t, post.Author.AvatarThumbURL)

	encoded, err := json.Marshal(post)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))
	require.Equal(t, []any{}, payload["images"])
	require.Equal(t, []any{}, payload["image_displays"])
	require.Equal(t, []any{}, payload["image_thumbs"])

	search := searchPostItem(&record, repository.PostRelations{}, "")
	require.NotNil(t, search.Images)
	require.NotNil(t, search.ImageDisplays)
	require.NotNil(t, search.ImageThumbs)
	require.Nil(t, search.Author.AvatarThumbURL)
}
