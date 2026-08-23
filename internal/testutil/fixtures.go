package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	appconfig "github.com/jingyijun/danshi_backend_go/internal/config"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/jwtx"
)

const fixturePasswordHash = "$2b$12$.tR4UmM4YnDt97LElAniw.6SCzecEr7vDX9lNteF5bDqWoJNW2.wq"

// Fixtures 是可组合、可覆写的真实 PostgreSQL 数据夹具入口。
type Fixtures struct {
	t  testing.TB
	db *gorm.DB

	mu       sync.Mutex
	sequence int
}

// NewFixtures 为一个已迁移数据库创建夹具入口。
func NewFixtures(t testing.TB, db *gorm.DB) *Fixtures {
	t.Helper()
	if db == nil {
		t.Fatal("夹具必须注入 GORM 数据库")
	}
	return &Fixtures{t: t, db: db}
}

// UserOverride 以闭包方式覆写默认用户模型。
type UserOverride func(*model.User)

// WithUserRole 覆写用户角色。
func WithUserRole(role model.UserRole) UserOverride {
	return func(user *model.User) { user.Roles = []model.UserRole{role} }
}

// WithUserRoles 覆写用户的多角色绑定。
func WithUserRoles(roles ...model.UserRole) UserOverride {
	return func(user *model.User) { user.Roles = append([]model.UserRole{}, roles...) }
}

// WithBannedUser 构造永久或限时封禁用户。
func WithBannedUser(
	bannedBy uint64,
	reason string,
	until *time.Time,
	permanent bool,
) UserOverride {
	return func(user *model.User) {
		user.BannedBy = &bannedBy
		user.BanReason = &reason
		user.BannedUntil = until
		user.BanIsPermanent = permanent
	}
}

// WithDeletedUser 构造已注销用户。
func WithDeletedUser(deletedAt time.Time) UserOverride {
	return func(user *model.User) { user.DeletedAt = &deletedAt }
}

// CreateUser 创建普通用户；所有字段都可由闭包叠加覆写。
func (f *Fixtures) CreateUser(overrides ...UserOverride) model.User {
	f.t.Helper()
	sequence := f.nextSequence()
	user := model.User{
		Email:        fmt.Sprintf("fixture-user-%04d@fdueat.com", sequence),
		PasswordHash: fixturePasswordHash,
		Name:         fmt.Sprintf("夹具用户 %04d", sequence),
	}
	for _, override := range overrides {
		override(&user)
	}
	f.mustCreate(&user, "创建用户夹具")
	for _, role := range user.Roles {
		f.mustCreate(&model.UserRoleBinding{
			UserID: user.ID, Role: role, GrantedAt: time.Now().UTC(),
		}, "创建用户角色夹具")
	}
	return user
}

// Actor 是带有效数据库会话和 access token 的用户夹具。
type Actor struct {
	User    model.User
	Session model.UserSession
	Token   string
}

// CreateActor 创建用户、会话和可用于 Harness.Engine 的 access token。
func (f *Fixtures) CreateActor(
	config appconfig.Config,
	overrides ...UserOverride,
) Actor {
	f.t.Helper()
	user := f.CreateUser(overrides...)
	now := time.Now().UTC()
	refreshTTL := config.RefreshTokenTTL()
	if refreshTTL <= 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	accessTTL := config.AccessTokenTTL()
	if accessTTL <= 0 {
		accessTTL = time.Hour
	}
	digestBytes := sha256.Sum256([]byte(fmt.Sprintf("fixture-session-%d", f.nextSequence())))
	device := "testutil fixture"
	session := model.UserSession{
		UserID:             user.ID,
		RefreshTokenDigest: hex.EncodeToString(digestBytes[:]),
		DeviceLabel:        &device,
		CreatedAt:          now,
		LastSeenAt:         now,
		ExpiresAt:          now.Add(refreshTTL),
	}
	f.mustCreate(&session, "创建会话夹具")
	token, err := jwtx.NewCodec(config.JWTSecretKey).SignAt(
		int64(user.ID), int64(session.ID), jwtx.TypeAccess, now, accessTTL,
	)
	if err != nil {
		f.t.Fatalf("签发夹具 access token 失败: %v", err)
	}
	return Actor{User: user, Session: session, Token: token}
}

// DictionarySpec 是餐厅、窗口、菜系和口味的组合输入。
type DictionarySpec struct {
	CanteenCode string
	CanteenName string
	Campus      string
	WindowName  string
	Floor       *string
	CuisineName string
	FlavorNames []string
	IsActive    bool
}

// DictionaryOverride 以闭包方式覆写默认词表组合。
type DictionaryOverride func(*DictionarySpec)

// DictionaryFixture 是一套互相一致的词表数据。
type DictionaryFixture struct {
	Canteen model.Canteen
	Window  model.CanteenWindow
	Cuisine model.Cuisine
	Flavors []model.Flavor
}

// CreateDictionaries 创建一套可用于帖子夹具的词表。
func (f *Fixtures) CreateDictionaries(overrides ...DictionaryOverride) DictionaryFixture {
	f.t.Helper()
	sequence := f.nextSequence()
	floor := "1F"
	spec := DictionarySpec{
		CanteenCode: fmt.Sprintf("fixture-%04d", sequence),
		CanteenName: fmt.Sprintf("夹具餐厅 %04d", sequence),
		Campus:      "夹具校区",
		WindowName:  fmt.Sprintf("夹具窗口 %04d", sequence),
		Floor:       &floor,
		CuisineName: fmt.Sprintf("夹具菜系 %04d", sequence),
		FlavorNames: []string{
			fmt.Sprintf("夹具口味A%04d", sequence),
			fmt.Sprintf("夹具口味B%04d", sequence),
		},
		IsActive: true,
	}
	for _, override := range overrides {
		override(&spec)
	}
	result := DictionaryFixture{
		Canteen: model.Canteen{
			Code: spec.CanteenCode, Name: spec.CanteenName,
			Campus: spec.Campus, IsActive: spec.IsActive,
		},
		Cuisine: model.Cuisine{Name: spec.CuisineName, IsActive: spec.IsActive},
	}
	f.mustCreate(&result.Canteen, "创建餐厅夹具")
	result.Window = model.CanteenWindow{
		CanteenID: result.Canteen.ID, Name: spec.WindowName,
		Floor: spec.Floor, IsActive: spec.IsActive,
	}
	f.mustCreate(&result.Window, "创建窗口夹具")
	f.mustCreate(&result.Cuisine, "创建菜系夹具")
	result.Flavors = make([]model.Flavor, len(spec.FlavorNames))
	for index, name := range spec.FlavorNames {
		result.Flavors[index] = model.Flavor{Name: name, IsActive: spec.IsActive}
		f.mustCreate(&result.Flavors[index], "创建口味夹具")
	}
	return result
}

// ImageOverride 以闭包方式覆写图片资产模型。
type ImageOverride func(*model.ImageAsset)

// CreateImage 创建默认 ready/pass 的图片资产。
func (f *Fixtures) CreateImage(uploaderID uint64, overrides ...ImageOverride) model.ImageAsset {
	f.t.Helper()
	sequence := f.nextSequence()
	size := int64(1024)
	image := model.ImageAsset{
		UploaderID:  &uploaderID,
		Purpose:     model.ImagePurposePost,
		ObjectKey:   fmt.Sprintf("fixtures/%04d.jpg", sequence),
		PublicURL:   fmt.Sprintf("https://image.example.test/fixtures/%04d.jpg", sequence),
		ContentType: "image/jpeg",
		Size:        &size,
		Status:      model.ImageStatusReady,
		Moderation:  model.ModerationStatusPass,
	}
	for _, override := range overrides {
		override(&image)
	}
	f.mustCreate(&image, "创建图片夹具")
	return image
}

// PostFlavorFixture 描述一个帖子口味关联。
type PostFlavorFixture struct {
	Flavor model.Flavor
	Stance model.FlavorStance
}

// PostSpec 是帖子主体与关联数据的可覆写输入。
type PostSpec struct {
	Post         model.Post
	TagNames     []string
	Flavors      []PostFlavorFixture
	Images       []model.ImageAsset
	WriteHistory bool
}

// PostOverride 以闭包方式组合帖子状态、图片、标签和口味。
type PostOverride func(*PostSpec)

// WithPostStatus 覆写帖子状态。
func WithPostStatus(status model.PostStatus) PostOverride {
	return func(spec *PostSpec) { spec.Post.Status = status }
}

// WithPostTitle 覆写帖子标题。
func WithPostTitle(title string) PostOverride {
	return func(spec *PostSpec) { spec.Post.Title = title }
}

// WithPostTags 覆写帖子自由标签。
func WithPostTags(names ...string) PostOverride {
	return func(spec *PostSpec) { spec.TagNames = append([]string{}, names...) }
}

// WithPostFlavors 覆写帖子口味关联。
func WithPostFlavors(flavors ...PostFlavorFixture) PostOverride {
	return func(spec *PostSpec) { spec.Flavors = append([]PostFlavorFixture{}, flavors...) }
}

// WithPostImages 覆写帖子图片关联。
func WithPostImages(images ...model.ImageAsset) PostOverride {
	return func(spec *PostSpec) { spec.Images = append([]model.ImageAsset{}, images...) }
}

// PostFixture 是完整帖子主体、关联和 revision 1。
type PostFixture struct {
	Post    model.Post
	Tags    []model.Tag
	Flavors []model.Flavor
	Images  []model.ImageAsset
	History *model.PostHistory
}

// CreatePost 创建默认 approved 分享帖，可组合覆写状态和全部关联。
func (f *Fixtures) CreatePost(authorID uint64, overrides ...PostOverride) PostFixture {
	f.t.Helper()
	sequence := f.nextSequence()
	shareType := model.ShareTypeRecommend
	spec := PostSpec{
		Post: model.Post{
			AuthorID: authorID, PostType: model.PostTypeShare,
			ShareType: &shareType, Status: model.PostStatusApproved,
			Category: model.PostCategoryFood,
			Title:    fmt.Sprintf("夹具帖子 %04d", sequence),
			Content:  "夹具帖子正文",
		},
		WriteHistory: true,
	}
	for _, override := range overrides {
		override(&spec)
	}
	var result PostFixture
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&spec.Post).Error; err != nil {
			return err
		}
		result.Post = spec.Post
		for _, name := range spec.TagNames {
			tag := model.Tag{Name: name, Moderation: model.ModerationStatusPass}
			query := tx.Where("lower(name) = lower(?)", name).First(&tag)
			if query.Error != nil {
				if !errors.Is(query.Error, gorm.ErrRecordNotFound) {
					return query.Error
				}
				if err := tx.Create(&tag).Error; err != nil {
					return err
				}
			}
			if err := tx.Create(&model.PostTag{PostID: spec.Post.ID, TagID: tag.ID}).Error; err != nil {
				return err
			}
			result.Tags = append(result.Tags, tag)
		}
		for _, flavor := range spec.Flavors {
			relation := model.PostFlavor{
				PostID: spec.Post.ID, FlavorID: flavor.Flavor.ID,
				Stance: flavor.Stance, PostType: spec.Post.PostType,
			}
			if err := tx.Create(&relation).Error; err != nil {
				return err
			}
			result.Flavors = append(result.Flavors, flavor.Flavor)
		}
		for index, image := range spec.Images {
			relation := model.PostImage{
				PostID: spec.Post.ID, Position: int16(index), ImageAssetID: image.ID,
			}
			if err := tx.Create(&relation).Error; err != nil {
				return err
			}
			result.Images = append(result.Images, image)
		}
		if spec.WriteHistory {
			snapshot, err := postFixtureSnapshot(spec)
			if err != nil {
				return err
			}
			history := model.PostHistory{
				PostID: spec.Post.ID, Revision: 1, EditedBy: authorID,
				EditedAt: time.Now().UTC(), Snapshot: snapshot,
			}
			if err := tx.Create(&history).Error; err != nil {
				return err
			}
			result.History = &history
		}
		return nil
	})
	if err != nil {
		f.t.Fatalf("创建帖子夹具失败: %v", err)
	}
	return result
}

// CommentSpec 是评论主体和 revision 1 的可覆写输入。
type CommentSpec struct {
	Comment      model.Comment
	WriteHistory bool
}

// CommentOverride 以闭包方式组合楼主、回复与深层回复。
type CommentOverride func(*CommentSpec)

// WithCommentContent 覆写评论正文。
func WithCommentContent(content string) CommentOverride {
	return func(spec *CommentSpec) { spec.Comment.Content = content }
}

// WithCommentParent 根据父评论自动设置 parent/root/reply_to 三个约束字段。
func WithCommentParent(parent model.Comment) CommentOverride {
	return func(spec *CommentSpec) {
		rootID := parent.ID
		if parent.RootID != nil {
			rootID = *parent.RootID
		}
		spec.Comment.ParentID = &parent.ID
		spec.Comment.RootID = &rootID
		spec.Comment.ReplyToUserID = parent.AuthorID
	}
}

// CommentFixture 是评论主体和 revision 1。
type CommentFixture struct {
	Comment model.Comment
	History *model.CommentHistory
}

// CreateComment 创建楼主评论；WithCommentParent 可把它组合成任意深度回复。
func (f *Fixtures) CreateComment(
	post model.Post,
	authorID uint64,
	overrides ...CommentOverride,
) CommentFixture {
	f.t.Helper()
	sequence := f.nextSequence()
	spec := CommentSpec{
		Comment: model.Comment{
			PostID: post.ID, AuthorID: authorID,
			ReplyToUserID: post.AuthorID,
			Content:       fmt.Sprintf("夹具评论 %04d", sequence),
		},
		WriteHistory: true,
	}
	for _, override := range overrides {
		override(&spec)
	}
	var result CommentFixture
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&spec.Comment).Error; err != nil {
			return err
		}
		result.Comment = spec.Comment
		if spec.WriteHistory {
			history := model.CommentHistory{
				CommentID: spec.Comment.ID, Revision: 1, EditedBy: authorID,
				EditedAt: time.Now().UTC(), Content: spec.Comment.Content,
			}
			if err := tx.Create(&history).Error; err != nil {
				return err
			}
			result.History = &history
		}
		return nil
	})
	if err != nil {
		f.t.Fatalf("创建评论夹具失败: %v", err)
	}
	return result
}

// CompleteWorld 是一次性可用的用户、词表、帖子、图片和评论世界。
type CompleteWorld struct {
	Users        WorldUsers
	Dictionaries DictionaryFixture
	Images       WorldImages
	Posts        WorldPosts
	Comments     WorldComments
}

// WorldUsers 覆盖普通、管理员、超级管理员、封禁和注销状态。
type WorldUsers struct {
	Ordinary   Actor
	Admin      Actor
	SuperAdmin Actor
	Banned     Actor
	Deleted    Actor
}

// WorldImages 覆盖已通过和待审图片。
type WorldImages struct {
	Passed  model.ImageAsset
	Pending model.ImageAsset
}

// WorldPosts 覆盖各种状态、带图/不带图及标签口味。
type WorldPosts struct {
	Draft     PostFixture
	Pending   PostFixture
	Approved  PostFixture
	Rejected  PostFixture
	WithImage PostFixture
}

// WorldComments 覆盖楼主、回复与深层回复。
type WorldComments struct {
	Root  CommentFixture
	Reply CommentFixture
	Deep  CommentFixture
}

// CompleteWorld 一次性创建后续域测试可直接复用的完整世界。
func (f *Fixtures) CompleteWorld(config appconfig.Config) *CompleteWorld {
	f.t.Helper()
	world := &CompleteWorld{}
	world.Users.Ordinary = f.CreateActor(config)
	world.Users.Admin = f.CreateActor(config, WithUserRole(model.UserRoleModerator))
	world.Users.SuperAdmin = f.CreateActor(config, WithUserRole(model.UserRoleSuperAdmin))
	until := time.Now().UTC().Add(24 * time.Hour)
	world.Users.Banned = f.CreateActor(config, WithBannedUser(
		world.Users.Admin.User.ID, "夹具限时封禁", &until, false,
	))
	world.Users.Deleted = f.CreateActor(config, WithDeletedUser(time.Now().UTC()))
	world.Dictionaries = f.CreateDictionaries()
	world.Images.Passed = f.CreateImage(world.Users.Ordinary.User.ID)
	world.Images.Pending = f.CreateImage(world.Users.Ordinary.User.ID, func(image *model.ImageAsset) {
		image.Moderation = model.ModerationStatusPending
	})
	flavor := PostFlavorFixture{
		Flavor: world.Dictionaries.Flavors[0], Stance: model.FlavorStanceHas,
	}
	withDictionary := func(spec *PostSpec) {
		spec.Post.CanteenID = &world.Dictionaries.Canteen.ID
		spec.Post.CanteenWindowID = &world.Dictionaries.Window.ID
		spec.Post.CuisineID = &world.Dictionaries.Cuisine.ID
	}
	world.Posts.Draft = f.CreatePost(world.Users.Ordinary.User.ID,
		WithPostStatus(model.PostStatusDraft))
	world.Posts.Pending = f.CreatePost(world.Users.Ordinary.User.ID,
		WithPostStatus(model.PostStatusPending),
		WithPostImages(world.Images.Pending))
	world.Posts.Approved = f.CreatePost(world.Users.Ordinary.User.ID,
		withDictionary,
		WithPostTags(fmt.Sprintf("夹签%04d", f.nextSequence())),
		WithPostFlavors(flavor))
	world.Posts.Rejected = f.CreatePost(world.Users.Ordinary.User.ID,
		WithPostStatus(model.PostStatusRejected))
	world.Posts.WithImage = f.CreatePost(world.Users.Ordinary.User.ID,
		WithPostImages(world.Images.Passed))
	world.Comments.Root = f.CreateComment(
		world.Posts.Approved.Post, world.Users.Admin.User.ID,
		WithCommentContent("夹具楼主评论"),
	)
	world.Comments.Reply = f.CreateComment(
		world.Posts.Approved.Post, world.Users.SuperAdmin.User.ID,
		WithCommentParent(world.Comments.Root.Comment),
		WithCommentContent("夹具回复"),
	)
	world.Comments.Deep = f.CreateComment(
		world.Posts.Approved.Post, world.Users.Ordinary.User.ID,
		WithCommentParent(world.Comments.Reply.Comment),
		WithCommentContent("夹具深层回复"),
	)
	return world
}

// CompleteWorld 用统一 Harness 的配置和数据库创建完整世界。
func (h *Harness) CompleteWorld() *CompleteWorld {
	return h.Fixtures.CompleteWorld(h.Config)
}

func postFixtureSnapshot(spec PostSpec) (json.RawMessage, error) {
	flavors := make([]map[string]any, 0, len(spec.Flavors))
	for _, flavor := range spec.Flavors {
		flavors = append(flavors, map[string]any{
			"id": flavor.Flavor.ID, "stance": flavor.Stance,
		})
	}
	tags := append([]string{}, spec.TagNames...)
	images := make([]string, len(spec.Images))
	for index := range spec.Images {
		images[index] = spec.Images[index].PublicURL
	}
	encoded, err := json.Marshal(map[string]any{
		"post_type": spec.Post.PostType, "share_type": spec.Post.ShareType,
		"status": spec.Post.Status, "category": spec.Post.Category,
		"title": spec.Post.Title, "content": spec.Post.Content,
		"canteen_id":        spec.Post.CanteenID,
		"canteen_window_id": spec.Post.CanteenWindowID,
		"cuisine_id":        spec.Post.CuisineID,
		"price":             spec.Post.Price, "budget_min": spec.Post.BudgetMin,
		"budget_max": spec.Post.BudgetMax,
		"tags":       tags, "flavors": flavors, "images": images,
	})
	return json.RawMessage(encoded), err
}

func (f *Fixtures) mustCreate(value any, description string) {
	f.t.Helper()
	if err := f.db.Create(value).Error; err != nil {
		f.t.Fatalf("%s失败: %v", description, err)
	}
}

func (f *Fixtures) nextSequence() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence++
	return f.sequence
}
