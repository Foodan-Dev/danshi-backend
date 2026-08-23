package model_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

func TestPurgedImageURLRoundTrip(t *testing.T) {
	value := model.PurgedImageURL(42)
	require.Equal(t, "urn:danshi:image-asset:42:retired", value)
	require.True(t, model.IsPurgedImageURL(value))
	require.False(t, model.IsPurgedImageURL("https://img.example.test/42.jpg"))
}

func TestModelsAgainstPostgresSchema(t *testing.T) {
	gdb := openMigratedPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	canteen := &model.Canteen{
		Code: "model-test-canteen", Name: "模型测试食堂", Campus: "测试校区",
		SortOrder: 9999, IsActive: true,
	}
	insertAndSelect(t, gdb, canteen)

	floor := "1F"
	window := &model.CanteenWindow{
		CanteenID: canteen.ID, Name: "模型测试窗口", Floor: &floor,
		SortOrder: 10, IsActive: true,
	}
	insertAndSelect(t, gdb, window)

	cuisine := &model.Cuisine{Name: "模型测试菜系", SortOrder: 9999, IsActive: true}
	insertAndSelect(t, gdb, cuisine)

	flavor := &model.Flavor{Name: "模型测试口味", SortOrder: 9999, IsActive: true}
	insertAndSelect(t, gdb, flavor)

	tag := &model.Tag{Name: "模型测试", Moderation: model.ModerationStatusPending}
	insertAndSelect(t, gdb, tag)

	author := &model.User{
		Email: "model-author@fdueat.com", PasswordHash: "$2b$12$modeltest",
		Name: "模型作者",
	}
	insertAndSelect(t, gdb, author)

	actor := &model.User{
		Email: "model-actor@fdueat.com", PasswordHash: "$2b$12$modeltest",
		Name: "模型互动者", Gender: ptr(model.GenderOther), Bio: ptr("模型层集成测试"),
	}
	require.NoError(t, gdb.Create(actor).Error)

	roleBinding := &model.UserRoleBinding{
		UserID: actor.ID, Role: model.UserRoleModerator, GrantedBy: &actor.ID, GrantedAt: now,
	}
	insertAndSelect(t, gdb, roleBinding)
	roleRecord := &model.UserRoleRecord{
		UserID: actor.ID, Role: model.UserRoleModerator, Action: model.UserRoleActionGrant,
		ActorID: &actor.ID, CreatedAt: now,
	}
	insertAndSelect(t, gdb, roleRecord)
	banRecord := &model.UserBanRecord{
		UserID: actor.ID, Action: model.UserBanActionUnban, ActorID: &actor.ID, CreatedAt: now,
	}
	insertAndSelect(t, gdb, banRecord)

	size := int64(1024)
	asset := &model.ImageAsset{
		UploaderID: &author.ID, Purpose: model.ImagePurposePost,
		ObjectKey: "model/test/post.jpg", PublicURL: "https://img.example/model-test.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
		Moderation: model.ModerationStatusPass,
	}
	insertAndSelect(t, gdb, asset)

	price := decimal.RequireFromString("18.50")
	post := &model.Post{
		AuthorID: author.ID, PostType: model.PostTypeShare,
		ShareType: ptr(model.ShareTypeRecommend), Status: model.PostStatusApproved,
		Category: model.PostCategoryFood, Title: "模型层测试帖子", Content: "验证真实 schema",
		CanteenID: &canteen.ID, CanteenWindowID: &window.ID, CuisineID: &cuisine.ID,
		Price: &price,
	}
	gotPost := insertAndSelect(t, gdb, post).(*model.Post)
	require.True(t, gotPost.Price.Equal(price))

	postTag := &model.PostTag{PostID: post.ID, TagID: tag.ID}
	insertAndSelect(t, gdb, postTag)

	postFlavor := &model.PostFlavor{
		PostID: post.ID, FlavorID: flavor.ID,
		Stance: model.FlavorStanceHas, PostType: model.PostTypeShare,
	}
	insertAndSelect(t, gdb, postFlavor)

	postImage := &model.PostImage{PostID: post.ID, Position: 0, ImageAssetID: asset.ID}
	insertAndSelect(t, gdb, postImage)

	comment := &model.Comment{
		PostID: post.ID, AuthorID: actor.ID, ReplyToUserID: author.ID,
		Content: "模型层测试评论", Moderation: model.ModerationStatusPass,
	}
	gotComment := insertAndSelect(t, gdb, comment).(*model.Comment)
	require.Equal(t, comment.ID, gotComment.EffectiveRootID)
	require.Equal(t, post.ID, *gotComment.RootPostIDForAuthorCheck)

	mention := &model.CommentMention{CommentID: comment.ID, UserID: author.ID}
	insertAndSelect(t, gdb, mention)

	follow := &model.Follow{FollowerID: actor.ID, FollowingID: author.ID}
	insertAndSelect(t, gdb, follow)

	favorite := &model.Favorite{UserID: actor.ID, PostID: post.ID}
	insertAndSelect(t, gdb, favorite)

	postLike := &model.PostLike{UserID: actor.ID, PostID: post.ID}
	insertAndSelect(t, gdb, postLike)

	commentLike := &model.CommentLike{UserID: author.ID, CommentID: comment.ID}
	insertAndSelect(t, gdb, commentLike)

	preview := "评论正文预览"
	notification := &model.Notification{
		RecipientID: author.ID, SenderID: actor.ID, Type: model.NotificationTypeComment,
		RelatedPostID: &post.ID, Content: &preview,
	}
	insertAndSelect(t, gdb, notification)

	verification := &model.EmailVerificationCode{
		Email: "model-verify@fdueat.com", Purpose: model.VerificationPurposeRegistration,
		CodeDigest: "model-code-digest", ExpiresAt: now.Add(10 * time.Minute),
		LastSentAt: &now, SendWindowStartedAt: now, SendCount: 1,
	}
	insertAndSelect(t, gdb, verification)

	session := &model.UserSession{
		UserID: actor.ID, RefreshTokenDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DeviceLabel: ptr("model-test"), UserAgent: ptr("model-test/1.0"), IP: ptr("127.0.0.1"),
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	insertAndSelect(t, gdb, session)

	postHistory := &model.PostHistory{
		PostID: post.ID, Revision: 1, EditedBy: author.ID, EditedAt: now,
		Snapshot:   json.RawMessage(`{"title":"模型层测试帖子","content":"验证真实 schema"}`),
		EditReason: ptr("首次发布"),
	}
	insertAndSelect(t, gdb, postHistory)

	commentHistory := &model.CommentHistory{
		CommentID: comment.ID, Revision: 1, EditedBy: actor.ID,
		EditedAt: now, Content: comment.Content,
	}
	insertAndSelect(t, gdb, commentHistory)

	jobID := "model-job-1"
	score := decimal.RequireFromString("98.25")
	moderation := &model.ModerationRecord{
		PostID: &post.ID,
		Scene:  model.ModerationSceneText, Provider: model.ModerationProviderTencentCI,
		ProviderJobID: &jobID, Verdict: model.ModerationVerdictPass,
		Labels: pq.StringArray{"food", "safe"}, Score: &score,
		RawResponse: json.RawMessage(`{"request_id":"model-test"}`),
	}
	gotModeration := insertAndSelect(t, gdb, moderation).(*model.ModerationRecord)
	require.Equal(t, pq.StringArray{"food", "safe"}, gotModeration.Labels)
	require.JSONEq(t, `{"request_id":"model-test"}`, string(gotModeration.RawResponse))
	require.True(t, gotModeration.Score.Equal(score))

	suggestion := &model.DictionarySuggestion{
		Kind: model.SuggestionKindCuisine, ProposedName: "模型测试新菜系",
		ProposerID: actor.ID, PostID: &post.ID, Status: model.SuggestionStatusPending,
	}
	insertAndSelect(t, gdb, suggestion)

	assertSchemaColumnParity(t, gdb)
}

func TestDatabaseManagedFieldsAreReadOnly(t *testing.T) {
	cases := []struct {
		value  any
		fields []string
	}{
		{value: &model.Post{}, fields: []string{"LikeCount", "FavoriteCount", "CommentCount", "ViewCount"}},
		{value: &model.Comment{}, fields: []string{
			"LikeCount", "ReplyCount", "EffectiveRootID", "RootPostIDForAuthorCheck",
		}},
		{value: &model.DictionarySuggestion{}, fields: []string{"ParentSuggestionKind"}},
	}

	for _, tc := range cases {
		parsed, err := schema.Parse(tc.value, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		for _, name := range tc.fields {
			field := parsed.LookUpField(name)
			require.NotNil(t, field, "%T.%s 不存在", tc.value, name)
			require.True(t, field.Readable, "%T.%s 应可读取", tc.value, name)
			require.False(t, field.Creatable, "%T.%s 不应参与 INSERT", tc.value, name)
			require.False(t, field.Updatable, "%T.%s 不应参与 UPDATE", tc.value, name)
		}
	}
}

func openMigratedPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	container, err := tcpostgres.Run(
		ctx,
		"postgres:18",
		tcpostgres.WithDatabase("danshi_model_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	sqlDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, sqlDB.PingContext(ctx))
	require.NoError(t, dbinfra.Up(ctx, sqlDB))

	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return gdb.WithContext(context.Background())
}

func insertAndSelect(t *testing.T, gdb *gorm.DB, value any) any {
	t.Helper()
	var selected any

	t.Run(tableName(t, gdb, value), func(t *testing.T) {
		require.NoError(t, gdb.Create(value).Error)

		stmt := &gorm.Statement{DB: gdb}
		require.NoError(t, stmt.Parse(value))
		key := make(map[string]any, len(stmt.Schema.PrimaryFields))
		for _, field := range stmt.Schema.PrimaryFields {
			fieldValue, _ := field.ValueOf(context.Background(), reflect.ValueOf(value))
			key[field.DBName] = fieldValue
		}
		require.NotEmpty(t, key, "%s 模型没有声明主键", stmt.Schema.Table)

		selected = reflect.New(reflect.TypeOf(value).Elem()).Interface()
		require.NoError(t, gdb.Where(key).First(selected).Error)
	})

	return selected
}

func tableName(t *testing.T, gdb *gorm.DB, value any) string {
	t.Helper()
	stmt := &gorm.Statement{DB: gdb}
	require.NoError(t, stmt.Parse(value))
	return stmt.Schema.Table
}

func assertSchemaColumnParity(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	catalog := modelCatalog()
	require.Len(t, catalog, 27)

	var actualTables []string
	require.NoError(t, gdb.Raw(`
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_type = 'BASE TABLE'
		  AND table_name <> 'goose_db_version'
		ORDER BY table_name
	`).Scan(&actualTables).Error)

	expectedTables := make([]string, 0, len(catalog))
	for _, value := range catalog {
		stmt := &gorm.Statement{DB: gdb}
		require.NoError(t, stmt.Parse(value))
		expectedTables = append(expectedTables, stmt.Schema.Table)

		expectedColumns := make([]string, 0, len(stmt.Schema.Fields))
		for _, field := range stmt.Schema.Fields {
			if field.DBName != "" {
				expectedColumns = append(expectedColumns, field.DBName)
			}
		}
		sort.Strings(expectedColumns)

		var actualColumns []string
		require.NoError(t, gdb.Raw(`
			SELECT column_name
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = ?
			ORDER BY column_name
		`, stmt.Schema.Table).Scan(&actualColumns).Error)
		require.Equal(t, expectedColumns, actualColumns, "%s 的模型列与迁移不一致", stmt.Schema.Table)
	}

	sort.Strings(expectedTables)
	require.Equal(t, expectedTables, actualTables)
}

func modelCatalog() []any {
	return []any{
		&model.Canteen{}, &model.CanteenWindow{}, &model.Cuisine{}, &model.Flavor{},
		&model.Tag{}, &model.User{}, &model.UserRoleBinding{}, &model.UserBanRecord{},
		&model.UserRoleRecord{}, &model.ImageAsset{}, &model.Post{},
		&model.PostTag{}, &model.PostFlavor{}, &model.PostImage{}, &model.Comment{},
		&model.CommentMention{}, &model.Follow{}, &model.Favorite{}, &model.PostLike{},
		&model.CommentLike{}, &model.Notification{}, &model.EmailVerificationCode{},
		&model.UserSession{}, &model.PostHistory{}, &model.CommentHistory{},
		&model.ModerationRecord{}, &model.DictionarySuggestion{},
	}
}

func ptr[T any](value T) *T { return &value }
