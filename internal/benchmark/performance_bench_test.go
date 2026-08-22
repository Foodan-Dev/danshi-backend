package benchmark_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	hertzjson "github.com/cloudwego/hertz/pkg/common/json"

	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/jwtx"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/money"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/passwordx"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

const benchmarkPasswordHash = "$2b$12$.tR4UmM4YnDt97LElAniw.6SCzecEr7vDX9lNteF5bDqWoJNW2.wq"

var (
	benchmarkBytes  []byte
	benchmarkClaims *jwtx.Claims
	benchmarkBool   bool
	benchmarkAmount money.Amount
	benchmarkString string
)

func TestJSONBackendSelection(t *testing.T) {
	t.Logf("go=%s hertz_json=%s sonic_api_kind=%d (std=%d sonic=%d)",
		runtime.Version(), hertzjson.Name, sonic.APIKind, sonic.UseStdJSON, sonic.UseSonicJSON)
}

func BenchmarkEnvelopeJSON(b *testing.B) {
	payload := envelope.OK("请求成功", map[string]any{
		"id": uint64(42), "status": "approved", "tags": []string{"食堂", "午餐", "性价比"},
	})
	encoded, err := hertzjson.Marshal(payload)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("encode", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			benchmarkBytes, err = hertzjson.Marshal(payload)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			var decoded envelope.Envelope[map[string]any]
			if err := hertzjson.Unmarshal(encoded, &decoded); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkRealResponseJSON(b *testing.B) {
	cases := []struct {
		name      string
		payload   any
		newTarget func() any
	}{
		{
			name: "post_list_20", payload: envelope.OK("请求成功", samplePostList(20)),
			newTarget: func() any { return &envelope.Envelope[service.PostList]{} },
		},
		{
			name: "post_detail", payload: envelope.OK("请求成功", samplePostDetail()),
			newTarget: func() any { return &envelope.Envelope[service.PostDetail]{} },
		},
		{
			name: "comment_list_20", payload: envelope.OK("请求成功", sampleCommentList(20)),
			newTarget: func() any { return &envelope.Envelope[service.CommentList]{} },
		},
	}
	for _, testCase := range cases {
		encoded, err := hertzjson.Marshal(testCase.payload)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(testCase.name+"/encode", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(encoded)))
			for b.Loop() {
				benchmarkBytes, err = hertzjson.Marshal(testCase.payload)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(testCase.name+"/decode", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(encoded)))
			for b.Loop() {
				if err := hertzjson.Unmarshal(encoded, testCase.newTarget()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkJWT(b *testing.B) {
	codec := jwtx.NewCodec("benchmark-secret-longer-than-thirty-two-bytes")
	now := time.Now().UTC()
	token, err := codec.SignAt(42, 84, jwtx.TypeAccess, now, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("sign", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkString, err = codec.SignAt(42, 84, jwtx.TypeAccess, now, time.Hour)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parse", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkClaims, err = codec.Parse(token, jwtx.TypeAccess)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkBcryptVerify(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		benchmarkBool = passwordx.Verify("danshi-test-password", benchmarkPasswordHash)
	}
	if !benchmarkBool {
		b.Fatal("benchmark password did not verify")
	}
}

func BenchmarkPaginationParse(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		params, err := pagination.Parse("12", "100")
		if err != nil || params.Offset() != 1100 {
			b.Fatalf("unexpected pagination result: %+v, %v", params, err)
		}
	}
}

func BenchmarkMoneyParse(b *testing.B) {
	var err error
	b.ReportAllocs()
	for b.Loop() {
		benchmarkAmount, err = money.Parse("12345678.90")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTimeFormat(b *testing.B) {
	value := time.Date(2026, time.August, 22, 12, 34, 56, 123456000, time.FixedZone("CST", 8*60*60))
	b.ReportAllocs()
	for b.Loop() {
		benchmarkString = ptime.Format(value)
	}
}

func samplePostList(size int) service.PostList {
	posts := make([]service.PostListItem, size)
	for index := range posts {
		posts[index] = samplePost(uint64(index + 1))
	}
	return service.PostList{
		Posts: posts,
		Pagination: pagination.Meta{
			Page: 1, Limit: size, Total: 243, TotalPages: (243 + size - 1) / size,
		},
	}
}

func samplePostDetail() service.PostDetail {
	item := samplePost(42)
	return service.PostDetail{
		PostListItem: item,
		BudgetRange:  &service.BudgetRangeView{Min: 15, Max: 35},
		Preferences: &service.PreferencesView{
			AvoidFlavors: []string{"太甜"}, PreferFlavors: []string{"微辣", "清淡"},
		},
	}
}

func samplePost(id uint64) service.PostListItem {
	shareType := model.ShareTypeRecommend
	cuisine := "本帮菜"
	floor := "二楼"
	avatar := "https://images.example.test/avatar/42.webp"
	following := id%2 == 0
	price, err := money.Parse("18.50")
	if err != nil {
		panic(err)
	}
	now := ptime.Time(time.Date(2026, time.August, 22, 12, 34, 56, 123456000, time.UTC))
	return service.PostListItem{
		ID: id, PostType: model.PostTypeShare, ShareType: &shareType,
		Title:         "江湾食堂午餐实测：窗口出餐快，价格稳定",
		Content:       "这是一段来自真实响应结构的帖子正文，包含中文、标点和足够接近线上列表的字段密度。",
		Category:      model.PostCategoryFood,
		Canteen:       &service.CanteenView{Code: "jiangwan", Name: "江湾食堂", Campus: "江湾校区"},
		CanteenWindow: &service.CanteenWindowView{ID: 17, Name: "风味小炒", Floor: &floor},
		Cuisine:       &cuisine, Flavors: []string{"微辣", "鲜香"}, Tags: []string{"午餐", "性价比", "江湾"},
		Price: &price,
		Images: []string{
			"https://images.example.test/posts/2026/08/a.webp",
			"https://images.example.test/posts/2026/08/b.webp",
		},
		Author: service.PostAuthorView{
			ID: 42, Name: "旦食用户", AvatarURL: &avatar, IsFollowing: &following,
		},
		Stats: service.PostStatsView{
			LikeCount: 128, FavoriteCount: 36, CommentCount: 24, ViewCount: 2048,
		},
		IsLiked: id%3 == 0, IsFavorited: id%5 == 0, IsEdited: true,
		Status: model.PostStatusApproved, CreatedAt: now, UpdatedAt: now,
	}
}

func sampleCommentList(size int) service.CommentList {
	comments := make([]service.CommentItem, size)
	for index := range comments {
		comments[index] = sampleComment(uint64(index+1), false)
		comments[index].ReplyCount = 7
		comments[index].Replies = []service.CommentItem{
			sampleComment(uint64(1000+index*2), true),
			sampleComment(uint64(1001+index*2), true),
		}
	}
	return service.CommentList{
		Comments: comments,
		Pagination: pagination.Meta{
			Page: 1, Limit: size, Total: 86, TotalPages: (86 + size - 1) / size,
		},
	}
}

func sampleComment(id uint64, reply bool) service.CommentItem {
	avatar := "https://images.example.test/avatar/commenter.webp"
	now := ptime.Time(time.Date(2026, time.August, 22, 13, 14, 15, 654321000, time.UTC))
	item := service.CommentItem{
		ID:      id,
		Content: "这是一条用于 JSON 实测的评论正文，长度和字段组合接近线上响应。",
		Author: service.CommentAuthorView{
			ID: 84, Name: "评论用户", AvatarURL: &avatar, IsFollowing: true,
		},
		MentionedUsers: []service.MentionedUserView{{ID: 42, Name: "旦食用户"}},
		LikeCount:      23, IsLiked: id%2 == 0, IsAuthor: false, IsEdited: true,
		CreatedAt: now, Replies: []service.CommentItem{},
	}
	if reply {
		item.ReplyTo = &service.ReplyToUserView{ID: 42, Name: "旦食用户"}
	}
	return item
}
